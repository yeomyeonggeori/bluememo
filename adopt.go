package bluememo

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// AdoptedMemory is a memory another store settled, carried with the embedding
// it was settled with.
type AdoptedMemory struct {
	Memory         Memory    `json:"memory"`
	Embedding      []float32 `json:"embedding,omitempty"`
	EmbeddingModel string    `json:"embeddingModel,omitempty"`
}

// AdoptReport counts what one adoption did.
type AdoptReport struct {
	Adopted     int `json:"adopted"`
	AlreadyHeld int `json:"alreadyHeld"`
}

// ErrAdoptedMemoryIncomplete reports an adopted memory the store cannot place:
// one without an identifier or content, or an embedding that does not say
// which model produced it.
var ErrAdoptedMemoryIncomplete = errors.New("adopted memory is incomplete")

// Adopt writes memories another store settled, exactly as they are. No model
// runs, nothing is judged, and no sentence is rewritten, so a memory keeps the
// identifier and the wording it already had. It is how one person's memory
// moves between stores.
//
// Adoption is idempotent on the identifier, so a move interrupted halfway
// finishes by running again.
//
// An embedding is kept with the model that produced it. Recall reads only the
// vectors of the model this store is configured with, so a memory adopted from
// another model answers on its wording until Reembed puts the current model's
// vector in its place. A memory adopted without an embedding names no model at
// all, which is the same stale state by a shorter road.
func (store *Store) Adopt(ctx context.Context, adopted []AdoptedMemory) (AdoptReport, error) {
	report := AdoptReport{}
	for _, candidate := range adopted {
		if err := validateAdoptedMemory(candidate); err != nil {
			return report, err
		}
	}
	for _, candidate := range adopted {
		held, err := store.alreadyHolds(ctx, candidate.Memory.MemoryID)
		if err != nil {
			return report, err
		}
		if held {
			report.AlreadyHeld++
			continue
		}
		if err := store.adoptOne(ctx, candidate); err != nil {
			return report, err
		}
		report.Adopted++
	}
	return report, nil
}

func validateAdoptedMemory(candidate AdoptedMemory) error {
	if candidate.Memory.MemoryID == "" {
		return fmt.Errorf("%w: no identifier", ErrAdoptedMemoryIncomplete)
	}
	if candidate.Memory.Content == "" {
		return fmt.Errorf("%w: %s has no content", ErrAdoptedMemoryIncomplete, candidate.Memory.MemoryID)
	}
	if len(candidate.Embedding) > 0 && candidate.EmbeddingModel == "" {
		return fmt.Errorf("%w: %s carries an embedding without its model", ErrAdoptedMemoryIncomplete, candidate.Memory.MemoryID)
	}
	return nil
}

func (store *Store) alreadyHolds(ctx context.Context, memoryID string) (bool, error) {
	var count int
	errorValue := store.database.QueryRowContext(ctx,
		`select count(*) from memory where memory_id = ?`, memoryID).Scan(&count)
	if errorValue != nil {
		return false, errorValue
	}
	return count > 0, nil
}

func (store *Store) adoptOne(ctx context.Context, candidate AdoptedMemory) error {
	memory := settledMemory(candidate.Memory, store.now())
	_, errorValue := store.database.ExecContext(ctx, `
		insert into memory (memory_id, content, is_static, occurred_at, occurred_until, valid_until, origin_id,
		                    importance, storage_strength, embedding_model, embedding, created_at)
		values (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		memory.MemoryID, memory.Content, memory.IsStatic,
		nullMilliseconds(memory.OccurredAt), nullMilliseconds(memory.OccurredUntil), nullMilliseconds(memory.ValidUntil),
		memory.OriginID, memory.Importance, memory.StorageStrength,
		candidate.EmbeddingModel, encodeEmbedding(candidate.Embedding), toMilliseconds(memory.CreatedAt))
	return errorValue
}

// settledMemory fills what a store settles for itself, so a caller carrying a
// memory from an older shape need only supply its wording and its dates.
func settledMemory(memory Memory, now time.Time) Memory {
	if memory.Importance < 1 || memory.Importance > 5 {
		memory.Importance = DefaultImportance
	}
	if memory.StorageStrength <= 0 {
		memory.StorageStrength = InitialStorageStrength
	}
	if memory.CreatedAt.IsZero() {
		memory.CreatedAt = now
	}
	return memory
}
