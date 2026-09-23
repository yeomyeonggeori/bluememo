package bluememo

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

const (
	SettleCandidateLimit     = 5
	RehearsalImportanceFloor = 3
)

var (
	ErrNoLanguageModel = errors.New("settling needs a language model")
	ErrNoJudge         = errors.New("settling needs a judge")
	ErrNoEmbedder      = errors.New("settling needs an embedder")
)

type SettleReport struct {
	Groups              int `json:"groups"`
	Proposed            int `json:"proposed"`
	Inserted            int `json:"inserted"`
	Reinforced          int `json:"reinforced"`
	Superseded          int `json:"superseded"`
	Extended            int `json:"extended"`
	Dropped             int `json:"dropped"`
	Rejected            int `json:"rejected"`
	RehearsalFailures   int `json:"rehearsalFailures"`
	DroppedTriggerCount int `json:"droppedTriggerCount"`
}

type pendingNote struct {
	body        string
	speakerName string
	isExplicit  bool
	arrivedAt   time.Time
}

type pendingGroup struct {
	groupID     string
	claimToken  string
	speakerName string
	isExplicit  bool
	arrivedAt   time.Time
	notes       []pendingNote
}

type candidate struct {
	memory    Memory
	embedding []float32
}

func (store *Store) Settle(ctx context.Context) (SettleReport, error) {
	report := SettleReport{}
	if errorValue := store.checkSettleDependencies(); errorValue != nil {
		return report, errorValue
	}
	groupIDs, errorValue := store.unclaimedGroupIDs(ctx)
	if errorValue != nil {
		return report, errorValue
	}
	for _, groupID := range groupIDs {
		group, isClaimed, errorValue := store.claimGroup(ctx, groupID)
		if errorValue != nil {
			return report, errorValue
		}
		if !isClaimed {
			continue
		}
		if errorValue := store.settleGroup(ctx, group, &report); errorValue != nil {
			return report, fmt.Errorf("settle group %s: %w", groupID, errorValue)
		}
	}
	return report, nil
}

func (store *Store) checkSettleDependencies() error {
	if store.configuration.Model == nil {
		return ErrNoLanguageModel
	}
	if store.configuration.Judge == nil {
		return ErrNoJudge
	}
	if store.configuration.Embedder == nil {
		return ErrNoEmbedder
	}
	return nil
}

func (store *Store) unclaimedGroupIDs(ctx context.Context) ([]string, error) {
	rows, errorValue := store.database.QueryContext(ctx, `
		select group_id from pending_note
		where settled_at is null and (claimed_until is null or claimed_until < ?)
		group by group_id order by min(arrived_at)`, toMilliseconds(store.now()))
	if errorValue != nil {
		return nil, errorValue
	}
	defer rows.Close()
	groupIDs := []string{}
	for rows.Next() {
		var groupID string
		if errorValue := rows.Scan(&groupID); errorValue != nil {
			return nil, errorValue
		}
		groupIDs = append(groupIDs, groupID)
	}
	return groupIDs, rows.Err()
}

func (store *Store) claimGroup(ctx context.Context, groupID string) (pendingGroup, bool, error) {
	now := store.now()
	claimToken := NewIdentifier()
	result, errorValue := store.database.ExecContext(ctx, `
		update pending_note set claim_token = ?, claimed_until = ?
		where group_id = ? and settled_at is null and (claimed_until is null or claimed_until < ?)`,
		claimToken, toMilliseconds(now.Add(store.configuration.ClaimDuration)), groupID, toMilliseconds(now))
	if errorValue != nil {
		return pendingGroup{}, false, errorValue
	}
	claimedCount, errorValue := result.RowsAffected()
	if errorValue != nil || claimedCount == 0 {
		return pendingGroup{}, false, errorValue
	}
	group, errorValue := store.loadClaimedGroup(ctx, groupID, claimToken)
	return group, errorValue == nil, errorValue
}

func (store *Store) loadClaimedGroup(ctx context.Context, groupID string, claimToken string) (pendingGroup, error) {
	rows, errorValue := store.database.QueryContext(ctx, `
		select body, speaker_name, is_explicit, arrived_at from pending_note
		where group_id = ? and claim_token = ? order by arrived_at, note_id`, groupID, claimToken)
	if errorValue != nil {
		return pendingGroup{}, errorValue
	}
	defer rows.Close()
	group := pendingGroup{groupID: groupID, claimToken: claimToken}
	for rows.Next() {
		var note pendingNote
		var arrivedAt int64
		if errorValue := rows.Scan(&note.body, &note.speakerName, &note.isExplicit, &arrivedAt); errorValue != nil {
			return pendingGroup{}, errorValue
		}
		note.arrivedAt = fromMilliseconds(arrivedAt)
		group.notes = append(group.notes, note)
		group.isExplicit = group.isExplicit || note.isExplicit
	}
	if len(group.notes) > 0 {
		group.speakerName = group.notes[0].speakerName
		group.arrivedAt = group.notes[0].arrivedAt
	}
	return group, rows.Err()
}

func (store *Store) settleGroup(ctx context.Context, group pendingGroup, report *SettleReport) error {
	report.Groups++
	propositions, errorValue := store.decompose(ctx, group)
	if errorValue != nil {
		return errorValue
	}
	originID := NewIdentifier()
	for _, proposition := range propositions {
		if errorValue := store.settleProposition(ctx, group, proposition, originID, report); errorValue != nil {
			return errorValue
		}
	}
	return store.finishGroup(ctx, group)
}

func (store *Store) finishGroup(ctx context.Context, group pendingGroup) error {
	_, errorValue := store.database.ExecContext(ctx,
		`update pending_note set settled_at = ? where group_id = ? and claim_token = ?`,
		toMilliseconds(store.now()), group.groupID, group.claimToken)
	return errorValue
}

func (store *Store) settleProposition(ctx context.Context, group pendingGroup, proposition Proposition, originID string, report *SettleReport) error {
	proposition.Content = strings.TrimSpace(proposition.Content)
	dates, errorValue := resolveDates(proposition, group.arrivedAt, store.configuration.Location)
	if errorValue != nil {
		report.Rejected++
		store.configuration.Logger.WarnContext(ctx, "memory.settle.proposition_rejected", "groupID", group.groupID, "error", errorValue.Error())
		return nil
	}
	report.Proposed++
	embedding, errorValue := store.embedDocument(ctx, proposition.Content)
	if errorValue != nil {
		return errorValue
	}
	candidates, errorValue := store.nearestLive(ctx, embedding, SettleCandidateLimit)
	if errorValue != nil {
		return errorValue
	}
	judgement, errorValue := store.configuration.Judge.Judge(ctx, proposition.Content, candidateContents(candidates))
	if errorValue != nil {
		return errorValue
	}
	fresh := store.newMemory(proposition, dates, originID, group, judgement.Importance)
	return store.applyJudgement(ctx, fresh, embedding, candidates, judgement, report)
}

func (store *Store) newMemory(proposition Proposition, dates dated, originID string, group pendingGroup, importance int) Memory {
	storageStrength := InitialStorageStrength
	if group.isExplicit {
		storageStrength = ExplicitStorageStrength
	}
	if importance < 1 || importance > 5 {
		importance = DefaultImportance
	}
	memory := Memory{
		MemoryID:        NewIdentifier(),
		Content:         proposition.Content,
		IsStatic:        proposition.IsStatic,
		OccurredAt:      dates.occurredAt,
		ValidUntil:      dates.validUntil,
		OriginID:        originID,
		Importance:      importance,
		StorageStrength: storageStrength,
		CreatedAt:       store.now(),
	}
	memory.ResolvedEntityIDs, memory.UnresolvedNames = store.resolveEntities(memory.Content)
	return memory
}

func (store *Store) applyJudgement(ctx context.Context, fresh Memory, embedding []float32, candidates []candidate, judgement Judgement, report *SettleReport) error {
	relation := judgement.Relation
	hasTarget := judgement.TargetIndex >= 0 && judgement.TargetIndex < len(candidates)
	if !hasTarget && relation != RelationNoise {
		relation = RelationUnrelated
	}
	switch relation {
	case RelationNoise:
		report.Dropped++
		return nil
	case RelationSame:
		report.Reinforced++
		return store.keepTheBetterOne(ctx, fresh, embedding, candidates[judgement.TargetIndex].memory, report)
	case RelationUpdates:
		report.Superseded++
		return store.insertAndRelate(ctx, fresh, embedding, candidates[judgement.TargetIndex].memory, EdgeUpdates, report)
	case RelationExtends:
		report.Extended++
		return store.insertAndRelate(ctx, fresh, embedding, candidates[judgement.TargetIndex].memory, EdgeExtends, report)
	}
	report.Inserted++
	return store.insertAndRehearse(ctx, fresh, embedding, func(*sql.Tx) error { return nil }, report)
}

func (store *Store) keepTheBetterOne(ctx context.Context, fresh Memory, embedding []float32, held Memory, report *SettleReport) error {
	now := store.now()
	gain := RecallReinforcement(held, store.configuration.HalfLife, now)
	if fresh.Importance <= held.Importance {
		_, errorValue := store.database.ExecContext(ctx,
			`update memory set storage_strength = storage_strength + ? where memory_id = ?`, gain, held.MemoryID)
		return errorValue
	}
	fresh.StorageStrength = max(fresh.StorageStrength, held.StorageStrength+gain)
	fresh.IsStatic = fresh.IsStatic || held.IsStatic
	return store.insertAndRehearse(ctx, fresh, embedding, func(transaction *sql.Tx) error {
		return supersede(ctx, transaction, held.MemoryID, fresh.MemoryID, now)
	}, report)
}

func (store *Store) insertAndRelate(ctx context.Context, fresh Memory, embedding []float32, target Memory, edge Edge, report *SettleReport) error {
	now := store.now()
	return store.insertAndRehearse(ctx, fresh, embedding, func(transaction *sql.Tx) error {
		if _, errorValue := transaction.ExecContext(ctx,
			`insert or ignore into memory_relation (from_memory_id, to_memory_id, edge, created_at) values (?, ?, ?, ?)`,
			fresh.MemoryID, target.MemoryID, edge, toMilliseconds(now)); errorValue != nil {
			return errorValue
		}
		if edge != EdgeUpdates {
			return nil
		}
		return supersede(ctx, transaction, target.MemoryID, fresh.MemoryID, now)
	}, report)
}

func supersede(ctx context.Context, transaction *sql.Tx, loserID string, winnerID string, now time.Time) error {
	_, errorValue := transaction.ExecContext(ctx, `
		update memory set superseded_by = ?, cold_since = ?, cold_reason = ?
		where memory_id = ?`, winnerID, toMilliseconds(now), ColdReasonSuperseded, loserID)
	return errorValue
}

func (store *Store) insertAndRehearse(ctx context.Context, fresh Memory, embedding []float32, alsoApply func(*sql.Tx) error, report *SettleReport) error {
	transaction, errorValue := store.database.BeginTx(ctx, nil)
	if errorValue != nil {
		return errorValue
	}
	defer transaction.Rollback()
	if errorValue := insertMemory(ctx, transaction, fresh, store.configuration.EmbeddingModel, embedding); errorValue != nil {
		return errorValue
	}
	if errorValue := alsoApply(transaction); errorValue != nil {
		return errorValue
	}
	if errorValue := transaction.Commit(); errorValue != nil {
		return errorValue
	}
	if fresh.Importance < RehearsalImportanceFloor {
		return nil
	}
	droppedCount, errorValue := store.rehearse(ctx, fresh, embedding)
	report.DroppedTriggerCount += droppedCount
	if errorValue != nil {
		report.RehearsalFailures++
		store.configuration.Logger.WarnContext(ctx, "memory.settle.rehearsal_failed", "memoryID", fresh.MemoryID, "error", errorValue.Error())
	}
	return nil
}

func insertMemory(ctx context.Context, transaction *sql.Tx, memory Memory, embeddingModel string, embedding []float32) error {
	resolvedJSON, errorValue := json.Marshal(nonNil(memory.ResolvedEntityIDs))
	if errorValue != nil {
		return errorValue
	}
	unresolvedJSON, errorValue := json.Marshal(nonNil(memory.UnresolvedNames))
	if errorValue != nil {
		return errorValue
	}
	_, errorValue = transaction.ExecContext(ctx, `
		insert into memory (memory_id, content, is_static, occurred_at, valid_until, origin_id, importance, storage_strength,
		                    resolved_entity_ids, unresolved_names, embedding_model, embedding, created_at)
		values (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		memory.MemoryID, memory.Content, memory.IsStatic, nullMilliseconds(memory.OccurredAt), nullMilliseconds(memory.ValidUntil),
		memory.OriginID, memory.Importance, memory.StorageStrength, string(resolvedJSON), string(unresolvedJSON),
		embeddingModel, encodeEmbedding(embedding), toMilliseconds(memory.CreatedAt))
	return errorValue
}

func (store *Store) nearestLive(ctx context.Context, embedding []float32, limit int) ([]candidate, error) {
	rows, errorValue := store.database.QueryContext(ctx, `select `+memoryColumns+`, embedding from memory
		where cold_since is null and embedding_model = ? and embedding is not null`, store.configuration.EmbeddingModel)
	if errorValue != nil {
		return nil, errorValue
	}
	defer rows.Close()
	type scoredCandidate struct {
		candidate
		similarity float64
	}
	scored := []scoredCandidate{}
	for rows.Next() {
		var encoded []byte
		memory, errorValue := scanMemory(rows, &encoded)
		if errorValue != nil {
			return nil, errorValue
		}
		held := candidate{memory: memory, embedding: decodeEmbedding(encoded)}
		scored = append(scored, scoredCandidate{candidate: held, similarity: cosineSimilarity(embedding, held.embedding)})
	}
	if errorValue := rows.Err(); errorValue != nil {
		return nil, errorValue
	}
	sort.SliceStable(scored, func(left int, right int) bool { return scored[left].similarity > scored[right].similarity })
	nearest := make([]candidate, 0, min(limit, len(scored)))
	for _, entry := range scored[:min(limit, len(scored))] {
		nearest = append(nearest, entry.candidate)
	}
	return nearest, nil
}

func (store *Store) embedDocument(ctx context.Context, text string) ([]float32, error) {
	embeddings, errorValue := store.configuration.Embedder.EmbedDocuments(ctx, []string{text})
	if errorValue != nil {
		return nil, fmt.Errorf("embedding failed: %w", errorValue)
	}
	if len(embeddings) != 1 {
		return nil, fmt.Errorf("embedder returned %d embeddings for one text", len(embeddings))
	}
	if errorValue := ValidateEmbedding(embeddings[0]); errorValue != nil {
		return nil, errorValue
	}
	return embeddings[0], nil
}

func (store *Store) resolveEntities(content string) ([]string, []string) {
	if store.configuration.People == nil {
		return []string{}, []string{}
	}
	resolved, unresolved := store.configuration.People.Resolve(content)
	return nonNil(resolved), nonNil(unresolved)
}

func candidateContents(candidates []candidate) []string {
	contents := make([]string, len(candidates))
	for index, held := range candidates {
		contents[index] = held.memory.Content
	}
	return contents
}

func nonNil(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}
