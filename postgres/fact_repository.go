package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/yeomyeonggeori/bluememo"
)

type FactRepository struct {
	database *sql.DB
}

func NewFactRepository(database *sql.DB) FactRepository {
	return FactRepository{database: database}
}

const factColumns = `
  f.fact_id, f.episode_id, f.owner_person_id, f.subject_person_id, f.kind, f.content,
  f.embedding_model, f.security_level_rank, COALESCE(array_to_json(f.required_classes)::text, '[]'),
  f.valid_from, f.valid_until, COALESCE(f.superseded_by, ''), f.reinforcement_count,
  f.last_recalled_at, f.forgotten_at, COALESCE(f.forget_reason, ''), f.created_at,
  COALESCE((SELECT array_to_json(array_agg(c.circle_id ORDER BY c.circle_id)) FROM memory_fact_circle c WHERE c.fact_id = f.fact_id)::text, '[]')`

const readableFactFilter = `
  (
    f.owner_person_id = $1
    OR (
      EXISTS (SELECT 1 FROM memory_fact_circle rc WHERE rc.fact_id = f.fact_id AND rc.circle_id = ANY($2::text[]))
      AND f.security_level_rank <= $3
      AND f.required_classes <@ $4::text[]
    )
  )
  AND f.superseded_by IS NULL
  AND f.forgotten_at IS NULL
  AND (f.valid_until IS NULL OR f.valid_until > $5)`

const lexicalSimilarityFloorLiteral = "0.2"

func (repository FactRepository) HasVectorSearch(ctx context.Context) (bool, error) {
	var hasVectorSearch bool
	errorValue := repository.database.QueryRowContext(ctx, `SELECT to_regclass('public.memory_fact_embedding') IS NOT NULL`).Scan(&hasVectorSearch)
	return hasVectorSearch, errorValue
}

func (repository FactRepository) SaveEpisode(ctx context.Context, write bluememo.EpisodeWrite) error {
	if errorValue := validateEpisodeWrite(write); errorValue != nil {
		return errorValue
	}
	hasVectorSearch, errorValue := repository.HasVectorSearch(ctx)
	if errorValue != nil {
		return errorValue
	}
	transaction, errorValue := repository.database.BeginTx(ctx, nil)
	if errorValue != nil {
		return errorValue
	}
	defer transaction.Rollback()
	isInserted, errorValue := insertEpisode(ctx, transaction, write)
	if errorValue != nil || !isInserted {
		return errorValue
	}
	for _, factWrite := range write.Facts {
		if errorValue := applyFactWrite(ctx, transaction, factWrite, hasVectorSearch); errorValue != nil {
			_ = transaction.Rollback()
			return errorValue
		}
		if errorValue := enqueueFactProfiles(ctx, transaction, factWrite, write.Episode.OccurredAt); errorValue != nil {
			return errorValue
		}
	}
	return transaction.Commit()
}

func validateEpisodeWrite(write bluememo.EpisodeWrite) error {
	if errorValue := bluememo.ValidateEpisode(write.Episode); errorValue != nil {
		return errorValue
	}
	for _, factWrite := range write.Facts {
		if factWrite.ReinforcesFactID != "" {
			if factWrite.SupersedesFactID != "" {
				return errors.New("a fact write reinforces or supersedes, never both")
			}
			continue
		}
		if factWrite.Fact.EpisodeID != write.Episode.EpisodeID {
			return fmt.Errorf("fact %s belongs to episode %s, not %s", factWrite.Fact.FactID, factWrite.Fact.EpisodeID, write.Episode.EpisodeID)
		}
		if errorValue := bluememo.ValidateFact(factWrite.Fact); errorValue != nil {
			return errorValue
		}
		if len(factWrite.Embedding) > 0 {
			if errorValue := bluememo.ValidateEmbedding(factWrite.Embedding); errorValue != nil {
				return errorValue
			}
		}
	}
	return nil
}

func insertEpisode(ctx context.Context, transaction *sql.Tx, write bluememo.EpisodeWrite) (bool, error) {
	episode := write.Episode
	receipt, errorValue := json.Marshal(bluememo.ReceiptForWrite(write))
	if errorValue != nil {
		return false, errorValue
	}
	result, errorValue := transaction.ExecContext(ctx, `
INSERT INTO memory_episode (episode_id, source_kind, source_id, requester_person_id, conversation_id, content, occurred_at, receipt)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8::jsonb)
ON CONFLICT (source_kind, source_id) DO NOTHING`,
		episode.EpisodeID, episode.SourceKind, episode.SourceID, episode.RequesterPersonID,
		episode.ConversationID, episode.Content, episode.OccurredAt.UTC(), string(receipt),
	)
	if errorValue != nil {
		return false, errorValue
	}
	insertedCount, errorValue := result.RowsAffected()
	if errorValue != nil || insertedCount > 0 {
		return insertedCount > 0, errorValue
	}
	_, _, errorValue = findEpisodeReceipt(ctx, transaction, episode)
	return false, errorValue
}

type episodeQueryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func (repository FactRepository) FindEpisodeReceipt(ctx context.Context, episode bluememo.Episode) (bluememo.EpisodeReceipt, bool, error) {
	return findEpisodeReceipt(ctx, repository.database, episode)
}

func findEpisodeReceipt(ctx context.Context, queryer episodeQueryer, requested bluememo.Episode) (bluememo.EpisodeReceipt, bool, error) {
	var stored bluememo.Episode
	var document []byte
	errorValue := queryer.QueryRowContext(ctx, `SELECT requester_person_id, conversation_id, content, receipt FROM memory_episode WHERE source_kind = $1 AND source_id = $2`, requested.SourceKind, requested.SourceID).Scan(&stored.RequesterPersonID, &stored.ConversationID, &stored.Content, &document)
	if errors.Is(errorValue, sql.ErrNoRows) {
		return bluememo.EpisodeReceipt{}, false, nil
	}
	if errorValue != nil {
		return bluememo.EpisodeReceipt{}, false, errorValue
	}
	if errorValue := bluememo.ValidateEpisodeReplay(stored, requested); errorValue != nil {
		return bluememo.EpisodeReceipt{}, false, errorValue
	}
	var receipt bluememo.EpisodeReceipt
	errorValue = json.Unmarshal(document, &receipt)
	return receipt, true, errorValue
}

func enqueueFactProfiles(ctx context.Context, transaction *sql.Tx, write bluememo.FactWrite, now time.Time) error {
	subjects := map[string]bool{write.Fact.SubjectPersonID: true}
	for _, factID := range []string{write.SupersedesFactID, write.ReinforcesFactID} {
		if factID == "" {
			continue
		}
		var personID string
		if errorValue := transaction.QueryRowContext(ctx, `SELECT subject_person_id FROM memory_fact WHERE fact_id = $1`, factID).Scan(&personID); errorValue != nil {
			return errorValue
		}
		subjects[personID] = true
	}
	return enqueueProfiles(ctx, transaction, subjects, now)
}

func enqueueProfiles(ctx context.Context, transaction *sql.Tx, subjects map[string]bool, now time.Time) error {
	for personID := range subjects {
		if personID == "" {
			continue
		}
		if _, _, errorValue := enqueueJob(ctx, transaction, bluememo.JobKindProfile, personID, now); errorValue != nil {
			return errorValue
		}
	}
	return nil
}

func applyFactWrite(ctx context.Context, transaction *sql.Tx, factWrite bluememo.FactWrite, hasVectorSearch bool) error {
	if factWrite.ReinforcesFactID != "" {
		return reinforceFact(ctx, transaction, factWrite.ReinforcesFactID)
	}
	if errorValue := insertFact(ctx, transaction, factWrite.Fact); errorValue != nil {
		return errorValue
	}
	if hasVectorSearch && len(factWrite.Embedding) > 0 {
		if errorValue := insertFactEmbedding(ctx, transaction, factWrite.Fact.FactID, factWrite.Embedding); errorValue != nil {
			return errorValue
		}
	}
	if factWrite.SupersedesFactID != "" {
		return supersedeFact(ctx, transaction, factWrite.SupersedesFactID, factWrite.Fact.FactID)
	}
	return nil
}

func insertFact(ctx context.Context, transaction *sql.Tx, fact bluememo.Fact) error {
	_, errorValue := transaction.ExecContext(ctx, `
INSERT INTO memory_fact (
  fact_id, episode_id, owner_person_id, subject_person_id, kind, content, embedding_model,
  security_level_rank, required_classes, valid_from, valid_until, reinforcement_count
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9::text[], $10, $11, GREATEST($12, 1))`,
		fact.FactID, fact.EpisodeID, fact.OwnerPersonID, fact.SubjectPersonID, fact.Kind,
		strings.TrimSpace(fact.Content), fact.EmbeddingModel, fact.SecurityLevelRank, nonNilStrings(fact.RequiredClasses),
		fact.ValidFrom.UTC(), nullableTime(fact.ValidUntil), fact.ReinforcementCount,
	)
	if errorValue != nil {
		return errorValue
	}
	for _, circleID := range bluememo.NormalizeCircleIDs(fact.CircleIDs) {
		if _, errorValue := transaction.ExecContext(ctx, `INSERT INTO memory_fact_circle (fact_id, circle_id) VALUES ($1, $2)`, fact.FactID, circleID); errorValue != nil {
			return errorValue
		}
	}
	return nil
}

func insertFactEmbedding(ctx context.Context, transaction *sql.Tx, factID string, embedding []float32) error {
	_, errorValue := transaction.ExecContext(ctx, `
INSERT INTO memory_fact_embedding (fact_id, embedding) VALUES ($1, $2::vector)`, factID, vectorLiteral(embedding))
	return errorValue
}

func supersedeFact(ctx context.Context, transaction *sql.Tx, oldFactID string, newFactID string) error {
	result, errorValue := transaction.ExecContext(ctx, `
UPDATE memory_fact SET superseded_by = $2
WHERE fact_id = $1 AND superseded_by IS NULL AND forgotten_at IS NULL`, oldFactID, newFactID)
	if errorValue != nil {
		return errorValue
	}
	return requireOneRow(result, fmt.Sprintf("fact %s is not live and cannot be superseded", oldFactID))
}

func reinforceFact(ctx context.Context, transaction *sql.Tx, factID string) error {
	result, errorValue := transaction.ExecContext(ctx, `
UPDATE memory_fact SET reinforcement_count = reinforcement_count + 1
WHERE fact_id = $1 AND superseded_by IS NULL AND forgotten_at IS NULL`, factID)
	if errorValue != nil {
		return errorValue
	}
	return requireOneRow(result, fmt.Sprintf("fact %s is not live and cannot be reinforced", factID))
}

func requireOneRow(result sql.Result, message string) error {
	affectedRows, errorValue := result.RowsAffected()
	if errorValue != nil {
		return errorValue
	}
	if affectedRows != 1 {
		return errors.New(message)
	}
	return nil
}

func (repository FactRepository) SearchFacts(ctx context.Context, query bluememo.FactSearchQuery) ([]bluememo.RankedFact, error) {
	arguments := readerArguments(query.Reader, query.ReferenceTime)
	arguments = append(arguments, query.Text, candidateLimit(query.CandidateLimit))
	searchSQL := lexicalFactSearchSQL
	if len(query.Embedding) > 0 {
		hasVectorSearch, errorValue := repository.HasVectorSearch(ctx)
		if errorValue != nil {
			return nil, errorValue
		}
		if hasVectorSearch {
			searchSQL = hybridFactSearchSQL
			arguments = append(arguments, vectorLiteral(query.Embedding), query.EmbeddingModel)
		}
	}
	rows, errorValue := repository.database.QueryContext(ctx, searchSQL, arguments...)
	if errorValue != nil {
		return nil, errorValue
	}
	defer rows.Close()
	return scanRankedFacts(rows)
}

const hybridFactSearchSQL = `
WITH readable AS (
  SELECT f.fact_id, f.content, f.valid_from, f.embedding_model FROM memory_fact f WHERE` + readableFactFilter + `
), vector_hits AS (
  SELECT r.fact_id, row_number() OVER (ORDER BY e.embedding <=> $8::vector) AS vector_rank
  FROM readable r JOIN memory_fact_embedding e ON e.fact_id = r.fact_id
  WHERE r.embedding_model = $9
  ORDER BY e.embedding <=> $8::vector
  LIMIT $7
), lexical_hits AS (
  SELECT r.fact_id, row_number() OVER (ORDER BY word_similarity($6, r.content) DESC, r.valid_from DESC) AS lexical_rank
  FROM readable r
  WHERE word_similarity($6, r.content) >= ` + lexicalSimilarityFloorLiteral + `
  ORDER BY word_similarity($6, r.content) DESC, r.valid_from DESC
  LIMIT $7
)
SELECT` + factColumns + `, COALESCE(v.vector_rank, 0), COALESCE(l.lexical_rank, 0)
FROM memory_fact f
JOIN (SELECT fact_id FROM vector_hits UNION SELECT fact_id FROM lexical_hits) hits ON hits.fact_id = f.fact_id
LEFT JOIN vector_hits v ON v.fact_id = f.fact_id
LEFT JOIN lexical_hits l ON l.fact_id = f.fact_id`

const lexicalFactSearchSQL = `
WITH readable AS (
  SELECT f.fact_id, f.content, f.valid_from FROM memory_fact f WHERE` + readableFactFilter + `
), lexical_hits AS (
  SELECT r.fact_id, row_number() OVER (ORDER BY word_similarity($6, r.content) DESC, r.valid_from DESC) AS lexical_rank
  FROM readable r
  WHERE word_similarity($6, r.content) >= ` + lexicalSimilarityFloorLiteral + `
  ORDER BY word_similarity($6, r.content) DESC, r.valid_from DESC
  LIMIT $7
)
SELECT` + factColumns + `, 0, l.lexical_rank
FROM memory_fact f
JOIN lexical_hits l ON l.fact_id = f.fact_id`

func (repository FactRepository) ListLiveFactsToRehearse(ctx context.Context, episodeID string, referenceTime time.Time) ([]bluememo.Fact, error) {
	rows, errorValue := repository.database.QueryContext(ctx, `
SELECT`+factColumns+`
FROM memory_fact f
WHERE f.episode_id = $1
  AND f.superseded_by IS NULL
  AND f.forgotten_at IS NULL
  AND (f.valid_until IS NULL OR f.valid_until > $2)
ORDER BY f.valid_from DESC`, episodeID, referenceTime.UTC())
	if errorValue != nil {
		return nil, errorValue
	}
	defer rows.Close()
	return scanFacts(rows)
}

func (repository FactRepository) ListLiveFactsNotEmbeddedWith(ctx context.Context, embeddingModel string, limit int, referenceTime time.Time) ([]bluememo.Fact, error) {
	rows, errorValue := repository.database.QueryContext(ctx, `
SELECT`+factColumns+`
FROM memory_fact f
WHERE f.embedding_model <> $1
  AND f.superseded_by IS NULL
  AND f.forgotten_at IS NULL
  AND (f.valid_until IS NULL OR f.valid_until > $2)
ORDER BY f.created_at
LIMIT $3`, embeddingModel, referenceTime.UTC(), candidateLimit(limit))
	if errorValue != nil {
		return nil, errorValue
	}
	defer rows.Close()
	return scanFacts(rows)
}

func (repository FactRepository) ReplaceFactEmbedding(ctx context.Context, factID string, embeddingModel string, embedding []float32) error {
	hasVectorSearch, errorValue := repository.HasVectorSearch(ctx)
	if errorValue != nil {
		return errorValue
	}
	transaction, errorValue := repository.database.BeginTx(ctx, nil)
	if errorValue != nil {
		return errorValue
	}
	defer transaction.Rollback()
	result, errorValue := transaction.ExecContext(ctx, `UPDATE memory_fact SET embedding_model = $2 WHERE fact_id = $1`, factID, embeddingModel)
	if errorValue != nil {
		return errorValue
	}
	if updatedRows, _ := result.RowsAffected(); updatedRows == 0 {
		return errors.New("memory fact " + factID + " does not exist")
	}
	if hasVectorSearch {
		if _, errorValue := transaction.ExecContext(ctx, `
INSERT INTO memory_fact_embedding (fact_id, embedding) VALUES ($1, $2::vector)
ON CONFLICT (fact_id) DO UPDATE SET embedding = EXCLUDED.embedding`, factID, vectorLiteral(embedding)); errorValue != nil {
			return errorValue
		}
	}
	return transaction.Commit()
}

func (repository FactRepository) ListFactsByID(ctx context.Context, reader bluememo.Reader, factIDs []string, referenceTime time.Time) ([]bluememo.Fact, error) {
	if len(factIDs) == 0 {
		return []bluememo.Fact{}, nil
	}
	arguments := append(readerArguments(reader, referenceTime), nonNilStrings(factIDs))
	rows, errorValue := repository.database.QueryContext(ctx, `
SELECT`+factColumns+`
FROM memory_fact f
WHERE`+readableFactFilter+`
  AND f.fact_id = ANY($6::text[])
ORDER BY f.valid_from DESC`, arguments...)
	if errorValue != nil {
		return nil, errorValue
	}
	defer rows.Close()
	return scanFacts(rows)
}

func (repository FactRepository) ListReadableFacts(ctx context.Context, reader bluememo.Reader, limit int, referenceTime time.Time) ([]bluememo.Fact, error) {
	if limit <= 0 {
		limit = bluememo.DefaultAdminFactListLimit
	}
	arguments := append(readerArguments(reader, referenceTime), limit)
	rows, errorValue := repository.database.QueryContext(ctx, `
SELECT`+factColumns+`
FROM memory_fact f
WHERE`+readableFactFilter+`
ORDER BY f.valid_from DESC, f.fact_id
LIMIT $6`, arguments...)
	if errorValue != nil {
		return nil, errorValue
	}
	defer rows.Close()
	return scanFacts(rows)
}

func (repository FactRepository) ListLiveFactsAboutPerson(ctx context.Context, reader bluememo.Reader, personID string, referenceTime time.Time) ([]bluememo.Fact, error) {
	arguments := append(readerArguments(reader, referenceTime), personID)
	rows, errorValue := repository.database.QueryContext(ctx, `
SELECT`+factColumns+`
FROM memory_fact f
WHERE`+readableFactFilter+`
  AND f.subject_person_id = $6
ORDER BY f.valid_from DESC`, arguments...)
	if errorValue != nil {
		return nil, errorValue
	}
	defer rows.Close()
	return scanFacts(rows)
}

func (repository FactRepository) ListLiveFactsFromEpisode(ctx context.Context, reader bluememo.Reader, episodeID string, referenceTime time.Time) ([]bluememo.Fact, error) {
	arguments := append(readerArguments(reader, referenceTime), episodeID)
	rows, errorValue := repository.database.QueryContext(ctx, `
SELECT`+factColumns+`
FROM memory_fact f
WHERE`+readableFactFilter+`
  AND f.episode_id = $6
ORDER BY f.valid_from DESC`, arguments...)
	if errorValue != nil {
		return nil, errorValue
	}
	defer rows.Close()
	return scanFacts(rows)
}

func (repository FactRepository) MarkFactsRecalled(ctx context.Context, factIDs []string, recalledAt time.Time) error {
	if len(factIDs) == 0 {
		return nil
	}
	_, errorValue := repository.database.ExecContext(ctx, `
UPDATE memory_fact SET last_recalled_at = $2 WHERE fact_id = ANY($1::text[])`, nonNilStrings(factIDs), recalledAt.UTC())
	return errorValue
}

func (repository FactRepository) ForgetFacts(ctx context.Context, reader bluememo.Reader, factIDs []string, reason string, forgottenAt time.Time) ([]string, error) {
	if len(factIDs) == 0 {
		return []string{}, nil
	}
	arguments := append(readerArguments(reader, forgottenAt), nonNilStrings(factIDs), strings.TrimSpace(reason), forgottenAt.UTC())
	transaction, errorValue := repository.database.BeginTx(ctx, nil)
	if errorValue != nil {
		return nil, errorValue
	}
	defer transaction.Rollback()
	rows, errorValue := transaction.QueryContext(ctx, `
UPDATE memory_fact f SET forgotten_at = $8, forget_reason = NULLIF($7, '')
WHERE`+readableFactFilter+`
  AND f.fact_id = ANY($6::text[])
RETURNING f.fact_id, f.subject_person_id`, arguments...)
	if errorValue != nil {
		return nil, errorValue
	}
	defer rows.Close()
	forgottenFactIDs := []string{}
	subjects := map[string]bool{}
	for rows.Next() {
		var factID, personID string
		if errorValue := rows.Scan(&factID, &personID); errorValue != nil {
			return nil, errorValue
		}
		forgottenFactIDs = append(forgottenFactIDs, factID)
		subjects[personID] = true
	}
	if errorValue := rows.Err(); errorValue != nil {
		return nil, errorValue
	}
	if errorValue := rows.Close(); errorValue != nil {
		return nil, errorValue
	}
	if errorValue := enqueueProfiles(ctx, transaction, subjects, forgottenAt); errorValue != nil {
		return nil, errorValue
	}
	return forgottenFactIDs, transaction.Commit()
}

func readerArguments(reader bluememo.Reader, referenceTime time.Time) []any {
	return []any{
		reader.PersonID,
		nonNilStrings(reader.ReadableCircleIDs),
		reader.SecurityLevelRank,
		nonNilStrings(reader.GrantedClasses),
		referenceTime.UTC(),
	}
}

func candidateLimit(limit int) int {
	if limit > 0 {
		return limit
	}
	return bluememo.DefaultSearchCandidateLimit
}

func scanRankedFacts(rows *sql.Rows) ([]bluememo.RankedFact, error) {
	rankedFacts := []bluememo.RankedFact{}
	for rows.Next() {
		var rankedFact bluememo.RankedFact
		fact, errorValue := scanFact(rows, &rankedFact.VectorRank, &rankedFact.LexicalRank)
		if errorValue != nil {
			return nil, errorValue
		}
		rankedFact.Fact = fact
		rankedFacts = append(rankedFacts, rankedFact)
	}
	return rankedFacts, rows.Err()
}

func scanFacts(rows *sql.Rows) ([]bluememo.Fact, error) {
	facts := []bluememo.Fact{}
	for rows.Next() {
		fact, errorValue := scanFact(rows)
		if errorValue != nil {
			return nil, errorValue
		}
		facts = append(facts, fact)
	}
	return facts, rows.Err()
}

func scanFact(rows *sql.Rows, trailingTargets ...any) (bluememo.Fact, error) {
	var fact bluememo.Fact
	var requiredClassesDocument, circleIDsDocument string
	var validUntil, lastRecalledAt, forgottenAt sql.NullTime
	targets := []any{
		&fact.FactID, &fact.EpisodeID, &fact.OwnerPersonID, &fact.SubjectPersonID, &fact.Kind, &fact.Content,
		&fact.EmbeddingModel, &fact.SecurityLevelRank, &requiredClassesDocument,
		&fact.ValidFrom, &validUntil, &fact.SupersededBy, &fact.ReinforcementCount,
		&lastRecalledAt, &forgottenAt, &fact.ForgetReason, &fact.CreatedAt, &circleIDsDocument,
	}
	targets = append(targets, trailingTargets...)
	if errorValue := rows.Scan(targets...); errorValue != nil {
		return bluememo.Fact{}, errorValue
	}
	fact.RequiredClasses = stringSliceFromDocument(requiredClassesDocument)
	fact.CircleIDs = stringSliceFromDocument(circleIDsDocument)
	if len(fact.CircleIDs) == 0 {
		fact.CircleIDs = nil
	}
	fact.ValidUntil = timeFromNullable(validUntil)
	fact.LastRecalledAt = timeFromNullable(lastRecalledAt)
	fact.ForgottenAt = timeFromNullable(forgottenAt)
	return fact, nil
}

func stringSliceFromDocument(document string) []string {
	values := []string{}
	if strings.TrimSpace(document) == "" {
		return values
	}
	if errorValue := json.Unmarshal([]byte(document), &values); errorValue != nil {
		return []string{}
	}
	return values
}

func timeFromNullable(value sql.NullTime) time.Time {
	if !value.Valid {
		return time.Time{}
	}
	return value.Time.UTC()
}

func nullableTime(value time.Time) sql.NullTime {
	if value.IsZero() {
		return sql.NullTime{}
	}
	return sql.NullTime{Time: value.UTC(), Valid: true}
}

func nonNilStrings(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}

func vectorLiteral(embedding []float32) string {
	var builder strings.Builder
	builder.Grow(len(embedding) * 10)
	builder.WriteByte('[')
	for index, value := range embedding {
		if index > 0 {
			builder.WriteByte(',')
		}
		builder.WriteString(strconv.FormatFloat(float64(value), 'g', -1, 32))
	}
	builder.WriteByte(']')
	return builder.String()
}
