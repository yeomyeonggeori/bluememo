package bluememo

import (
	"context"
	"database/sql"
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

// ErrEmbeddingWidthMismatch reports an adopted embedding whose width differs
// from another vector this store holds, or is adopting, from the same model.
var ErrEmbeddingWidthMismatch = errors.New("an embedding is not as wide as the others from its model")

// Adopt writes memories another store settled, exactly as they are. No model
// runs, nothing is judged, and no sentence is rewritten, so a memory keeps the
// identifier and the wording it already had. It is how one person's memory
// moves between stores.
//
// Adoption is idempotent on the identifier, so a move interrupted halfway
// finishes by running again. An identifier the store has buried counts as
// already held, so running a move again never brings back what was forgotten.
//
// An embedding is kept with the model that produced it. Recall reads only the
// vectors of the model this store is configured with, so a memory adopted from
// another model answers on its wording until Reembed puts the current model's
// vector in its place. A memory adopted without an embedding names no model at
// all, which is the same stale state by a shorter road. One model has one
// width in a store: an embedding is refused when a vector the store holds, or
// another in the same adoption, came from its model at a different width.
func (store *Store) Adopt(ctx context.Context, adopted []AdoptedMemory) (AdoptReport, error) {
	report := AdoptReport{}
	for _, candidate := range adopted {
		if err := validateAdoptedMemory(candidate); err != nil {
			return report, err
		}
	}
	if err := store.validateEmbeddingWidths(ctx, adopted); err != nil {
		return report, err
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
	if len(candidate.Embedding) == 0 {
		return nil
	}
	if candidate.EmbeddingModel == "" {
		return fmt.Errorf("%w: %s carries an embedding without its model", ErrAdoptedMemoryIncomplete, candidate.Memory.MemoryID)
	}
	if err := ValidateEmbedding(candidate.Embedding); err != nil {
		return fmt.Errorf("%w: %s", err, candidate.Memory.MemoryID)
	}
	return nil
}

func (store *Store) validateEmbeddingWidths(ctx context.Context, adopted []AdoptedMemory) error {
	widthByModel := map[string]int{}
	for _, candidate := range adopted {
		if len(candidate.Embedding) == 0 {
			continue
		}
		width, known := widthByModel[candidate.EmbeddingModel]
		if !known {
			heldWidth, err := store.heldEmbeddingWidth(ctx, candidate.EmbeddingModel)
			if err != nil {
				return err
			}
			width = heldWidth
		}
		if width == 0 {
			width = len(candidate.Embedding)
		}
		if len(candidate.Embedding) != width {
			return fmt.Errorf("%w: %s is %d wide and %s vectors are %d wide",
				ErrEmbeddingWidthMismatch, candidate.Memory.MemoryID, len(candidate.Embedding), candidate.EmbeddingModel, width)
		}
		widthByModel[candidate.EmbeddingModel] = width
	}
	return nil
}

func (store *Store) heldEmbeddingWidth(ctx context.Context, embeddingModel string) (int, error) {
	var byteCount sql.NullInt64
	errorValue := store.database.QueryRowContext(ctx, `
		select length(embedding) from (
			select embedding from memory where embedding_model = ? and embedding is not null
			union all select embedding from memory_trigger where embedding_model = ?
			union all select embedding from file where embedding_model = ? and embedding is not null
		) limit 1`, embeddingModel, embeddingModel, embeddingModel).Scan(&byteCount)
	if errors.Is(errorValue, sql.ErrNoRows) {
		return 0, nil
	}
	if errorValue != nil {
		return 0, errorValue
	}
	return int(byteCount.Int64) / 4, nil
}

func (store *Store) alreadyHolds(ctx context.Context, memoryID string) (bool, error) {
	var isHeld bool
	errorValue := store.database.QueryRowContext(ctx, `
		select exists (select 1 from memory where memory_id = ?)
		    or exists (select 1 from tombstone where memory_id = ?)`, memoryID, memoryID).Scan(&isHeld)
	return isHeld, errorValue
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
