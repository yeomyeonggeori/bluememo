package bluememo

import (
	"context"
	"errors"
	"strconv"
	"strings"
)

const DefaultReembedBatchSize = 32

type ReembedJobHandler struct {
	Store     Store
	BatchSize int
}

func (handler ReembedJobHandler) Handle(ctx context.Context, job Job) error {
	embeddingModel := strings.TrimSpace(job.SubjectID)
	if embeddingModel == "" {
		return TerminalJobError{Cause: errors.New("reembed job names no embedding model")}
	}
	if handler.Store.Embedder == nil || handler.Store.Facts == nil {
		return TerminalJobError{Cause: errors.New("memory embedder or fact repository is not configured")}
	}
	for {
		moved, errorValue := handler.reembedBatch(ctx, embeddingModel)
		if errorValue != nil {
			return errorValue
		}
		if moved == 0 {
			return nil
		}
	}
}

func (handler ReembedJobHandler) reembedBatch(ctx context.Context, embeddingModel string) (int, error) {
	facts, errorValue := handler.Store.Facts.ListLiveFactsNotEmbeddedWith(ctx, embeddingModel, handler.batchSize(), handler.Store.now())
	if errorValue != nil || len(facts) == 0 {
		return 0, errorValue
	}
	contents := make([]string, 0, len(facts))
	for _, fact := range facts {
		contents = append(contents, fact.Content)
	}
	embeddings, errorValue := handler.Store.Embedder.EmbedDocuments(ctx, contents)
	if errorValue != nil {
		return 0, errorValue
	}
	if len(embeddings) != len(facts) {
		return 0, errors.New("embedder answered " + strconv.Itoa(len(embeddings)) + " embeddings for " + strconv.Itoa(len(facts)) + " facts")
	}
	for index, fact := range facts {
		if errorValue := ValidateEmbedding(embeddings[index]); errorValue != nil {
			return 0, errorValue
		}
		if errorValue := handler.Store.Facts.ReplaceFactEmbedding(ctx, fact.FactID, embeddingModel, embeddings[index]); errorValue != nil {
			return 0, errorValue
		}
	}
	return len(facts), nil
}

func (handler ReembedJobHandler) batchSize() int {
	if handler.BatchSize > 0 {
		return handler.BatchSize
	}
	return DefaultReembedBatchSize
}
