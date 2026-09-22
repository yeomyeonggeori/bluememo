package postgres

import (
	"context"
	"database/sql"

	"github.com/yeomyeonggeori/bluememo"
)

type TriggerRepository struct {
	database *sql.DB
}

func NewTriggerRepository(database *sql.DB) TriggerRepository {
	return TriggerRepository{database: database}
}

func (repository TriggerRepository) SaveFactTriggers(ctx context.Context, triggers []bluememo.FactTrigger, embeddings [][]float32) error {
	if len(triggers) == 0 {
		return nil
	}
	transaction, errorValue := repository.database.BeginTx(ctx, nil)
	if errorValue != nil {
		return errorValue
	}
	defer transaction.Rollback()
	for index, trigger := range triggers {
		if _, errorValue := transaction.ExecContext(ctx, `
INSERT INTO memory_fact_trigger (trigger_id, fact_id, phrase, embedding_model)
VALUES ($1, $2, $3, $4)
ON CONFLICT (fact_id, phrase) DO NOTHING`, trigger.TriggerID, trigger.FactID, trigger.Phrase, trigger.EmbeddingModel); errorValue != nil {
			return errorValue
		}
		if index >= len(embeddings) || len(embeddings[index]) == 0 {
			continue
		}
		if _, errorValue := transaction.ExecContext(ctx, `
INSERT INTO memory_fact_trigger_embedding (trigger_id, embedding) VALUES ($1, $2::vector)
ON CONFLICT (trigger_id) DO UPDATE SET embedding = EXCLUDED.embedding`, trigger.TriggerID, vectorLiteral(embeddings[index])); errorValue != nil {
			return errorValue
		}
	}
	return transaction.Commit()
}

func (repository TriggerRepository) DeleteFactTriggers(ctx context.Context, factIDs []string) error {
	if len(factIDs) == 0 {
		return nil
	}
	_, errorValue := repository.database.ExecContext(ctx, `
DELETE FROM memory_fact_trigger WHERE fact_id = ANY($1::text[])`, nonNilStrings(factIDs))
	return errorValue
}

func (repository TriggerRepository) SearchTriggers(ctx context.Context, query bluememo.FactSearchQuery) ([]bluememo.RankedFact, error) {
	if len(query.Embedding) == 0 {
		return nil, nil
	}
	arguments := append(readerArguments(query.Reader, query.ReferenceTime), query.CandidateLimit, vectorLiteral(query.Embedding), query.EmbeddingModel)
	rows, errorValue := repository.database.QueryContext(ctx, triggerSearchSQL, arguments...)
	if errorValue != nil {
		return nil, errorValue
	}
	defer rows.Close()
	return scanRankedFacts(rows)
}

const triggerSearchSQL = `
WITH readable AS (
  SELECT f.fact_id FROM memory_fact f WHERE` + readableFactFilter + `
), trigger_hits AS (
  SELECT t.fact_id, row_number() OVER (ORDER BY MIN(e.embedding <=> $7::vector)) AS vector_rank
  FROM memory_fact_trigger t
  JOIN readable r ON r.fact_id = t.fact_id
  JOIN memory_fact_trigger_embedding e ON e.trigger_id = t.trigger_id
  WHERE t.embedding_model = $8
  GROUP BY t.fact_id
  ORDER BY MIN(e.embedding <=> $7::vector)
  LIMIT $6
)
SELECT` + factColumns + `, trigger_hits.vector_rank, 0
FROM memory_fact f
JOIN trigger_hits ON trigger_hits.fact_id = f.fact_id
ORDER BY trigger_hits.vector_rank`

func (repository TriggerRepository) ListTriggerPhrases(ctx context.Context, factIDs []string) (map[string][]string, error) {
	phrases := map[string][]string{}
	if len(factIDs) == 0 {
		return phrases, nil
	}
	rows, errorValue := repository.database.QueryContext(ctx, `
SELECT fact_id, phrase
FROM memory_fact_trigger
WHERE fact_id = ANY($1::text[])
ORDER BY fact_id, phrase`, nonNilStrings(factIDs))
	if errorValue != nil {
		return nil, errorValue
	}
	defer rows.Close()
	for rows.Next() {
		var factID, phrase string
		if errorValue := rows.Scan(&factID, &phrase); errorValue != nil {
			return nil, errorValue
		}
		phrases[factID] = append(phrases[factID], phrase)
	}
	return phrases, rows.Err()
}
