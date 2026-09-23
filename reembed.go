package bluememo

import (
	"context"
	"fmt"
)

type ReembedReport struct {
	Memories int `json:"memories"`
	Triggers int `json:"triggers"`
}

type staleText struct {
	identifier string
	text       string
}

func (store *Store) Reembed(ctx context.Context, batchSize int) (ReembedReport, error) {
	if store.configuration.Embedder == nil {
		return ReembedReport{}, ErrNoEmbedder
	}
	if batchSize <= 0 {
		batchSize = 64
	}
	memoryCount, errorValue := store.reembedTable(ctx, batchSize,
		`select memory_id, content from memory where embedding_model <> ? or embedding is null limit ?`,
		`update memory set embedding = ?, embedding_model = ? where memory_id = ?`)
	if errorValue != nil {
		return ReembedReport{}, errorValue
	}
	triggerCount, errorValue := store.reembedTable(ctx, batchSize,
		`select trigger_id, phrase from memory_trigger where embedding_model <> ? limit ?`,
		`update memory_trigger set embedding = ?, embedding_model = ? where trigger_id = ?`)
	return ReembedReport{Memories: memoryCount, Triggers: triggerCount}, errorValue
}

func (store *Store) reembedTable(ctx context.Context, batchSize int, selectStale string, updateEmbedding string) (int, error) {
	total := 0
	for {
		stale, errorValue := store.staleTexts(ctx, selectStale, batchSize)
		if errorValue != nil || len(stale) == 0 {
			return total, errorValue
		}
		if errorValue := store.replaceEmbeddings(ctx, stale, updateEmbedding); errorValue != nil {
			return total, errorValue
		}
		total += len(stale)
	}
}

func (store *Store) staleTexts(ctx context.Context, selectStale string, batchSize int) ([]staleText, error) {
	rows, errorValue := store.database.QueryContext(ctx, selectStale, store.configuration.EmbeddingModel, batchSize)
	if errorValue != nil {
		return nil, errorValue
	}
	defer rows.Close()
	stale := []staleText{}
	for rows.Next() {
		var entry staleText
		if errorValue := rows.Scan(&entry.identifier, &entry.text); errorValue != nil {
			return nil, errorValue
		}
		stale = append(stale, entry)
	}
	return stale, rows.Err()
}

func (store *Store) replaceEmbeddings(ctx context.Context, stale []staleText, updateEmbedding string) error {
	texts := make([]string, len(stale))
	for index, entry := range stale {
		texts[index] = entry.text
	}
	embeddings, errorValue := store.configuration.Embedder.EmbedDocuments(ctx, texts)
	if errorValue != nil {
		return fmt.Errorf("reembedding failed: %w", errorValue)
	}
	if len(embeddings) != len(stale) {
		return fmt.Errorf("embedder returned %d embeddings for %d texts", len(embeddings), len(stale))
	}
	transaction, errorValue := store.database.BeginTx(ctx, nil)
	if errorValue != nil {
		return errorValue
	}
	defer transaction.Rollback()
	for index, entry := range stale {
		if errorValue := ValidateEmbedding(embeddings[index]); errorValue != nil {
			return fmt.Errorf("reembedding %s: %w", entry.identifier, errorValue)
		}
		if _, errorValue := transaction.ExecContext(ctx, updateEmbedding,
			encodeEmbedding(embeddings[index]), store.configuration.EmbeddingModel, entry.identifier); errorValue != nil {
			return errorValue
		}
	}
	return transaction.Commit()
}
