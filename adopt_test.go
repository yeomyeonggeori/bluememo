package bluememo_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/yeomyeonggeori/bluememo"
	"github.com/yeomyeonggeori/bluememo/bluememotest"
)

func adoptedFact(memoryID string, content string) bluememo.AdoptedMemory {
	return bluememo.AdoptedMemory{
		Memory: bluememo.Memory{
			MemoryID:   memoryID,
			Content:    content,
			OccurredAt: time.Date(2026, 3, 5, 0, 0, 0, 0, time.UTC),
		},
	}
}

func TestAdoptKeepsTheIdentifierAndTheWordingWithoutAModel(t *testing.T) {
	testFixture := newFixture(t)
	adopted := adoptedFact("carried-1", "이샘플 leads the spring audit.")

	report, errorValue := testFixture.store.Adopt(context.Background(), []bluememo.AdoptedMemory{adopted})
	if errorValue != nil {
		t.Fatalf("adopt: %v", errorValue)
	}
	if report.Adopted != 1 || report.AlreadyHeld != 0 {
		t.Fatalf("report = %+v, want one adopted", report)
	}

	memories := testFixture.memories(t)
	if len(memories) != 1 {
		t.Fatalf("holds %d memories, want 1", len(memories))
	}
	if memories[0].MemoryID != "carried-1" {
		t.Errorf("identifier = %q, want the one it arrived with", memories[0].MemoryID)
	}
	if memories[0].Content != adopted.Memory.Content {
		t.Errorf("content = %q, want the wording it arrived with", memories[0].Content)
	}
	if memories[0].Importance < 1 || memories[0].StorageStrength <= 0 {
		t.Errorf("importance %d and strength %v, want a settled memory's defaults",
			memories[0].Importance, memories[0].StorageStrength)
	}
}

func TestAdoptRunTwiceHoldsOneCopy(t *testing.T) {
	testFixture := newFixture(t)
	adopted := []bluememo.AdoptedMemory{adoptedFact("carried-1", "이샘플 leads the spring audit.")}
	ctx := context.Background()

	if _, errorValue := testFixture.store.Adopt(ctx, adopted); errorValue != nil {
		t.Fatalf("first adopt: %v", errorValue)
	}
	report, errorValue := testFixture.store.Adopt(ctx, adopted)
	if errorValue != nil {
		t.Fatalf("second adopt: %v", errorValue)
	}
	if report.Adopted != 0 || report.AlreadyHeld != 1 {
		t.Fatalf("report = %+v, want nothing adopted and one already held", report)
	}
	if memories := testFixture.memories(t); len(memories) != 1 {
		t.Fatalf("holds %d memories, want 1", len(memories))
	}
}

func TestAdoptCarriesAnEmbeddingTheStoreCanRecallBy(t *testing.T) {
	testFixture := newFixture(t)
	ctx := context.Background()
	content := "박예시 keeps the quarterly ledger."
	vectors, errorValue := bluememotest.HashEmbedder{}.EmbedDocuments(ctx, []string{content})
	if errorValue != nil {
		t.Fatalf("embed: %v", errorValue)
	}

	adopted := adoptedFact("carried-1", content)
	adopted.Embedding = vectors[0]
	adopted.EmbeddingModel = "hash"
	if _, errorValue := testFixture.store.Adopt(ctx, []bluememo.AdoptedMemory{adopted}); errorValue != nil {
		t.Fatalf("adopt: %v", errorValue)
	}

	result, errorValue := testFixture.store.Recall(ctx, "who keeps the quarterly ledger", 5)
	if errorValue != nil {
		t.Fatalf("recall: %v", errorValue)
	}
	if len(result.Memories) != 1 || result.Memories[0].Memory.MemoryID != "carried-1" {
		t.Fatalf("recall returned %+v, want the adopted memory", result.Memories)
	}
}

func TestAdoptKeepsAnEmbeddingFromAnotherModelUntilReembed(t *testing.T) {
	testFixture := newFixture(t)
	ctx := context.Background()

	adopted := adoptedFact("carried-1", "최견본 chairs the safety review.")
	adopted.Embedding = []float32{0.1, 0.2, 0.3}
	adopted.EmbeddingModel = "some-other-model"
	if _, errorValue := testFixture.store.Adopt(ctx, []bluememo.AdoptedMemory{adopted}); errorValue != nil {
		t.Fatalf("adopt: %v", errorValue)
	}

	state, errorValue := testFixture.store.IndexState(ctx)
	if errorValue != nil {
		t.Fatalf("index state: %v", errorValue)
	}
	if state.Stale != 1 {
		t.Fatalf("stale = %d, want the adopted memory reported as stale", state.Stale)
	}

	if _, errorValue := testFixture.store.Reembed(ctx, 10); errorValue != nil {
		t.Fatalf("reembed: %v", errorValue)
	}
	state, errorValue = testFixture.store.IndexState(ctx)
	if errorValue != nil {
		t.Fatalf("index state after reembed: %v", errorValue)
	}
	if state.Stale != 0 {
		t.Errorf("stale = %d after reembed, want 0", state.Stale)
	}
}

func TestAdoptRefusesAMemoryItCannotPlace(t *testing.T) {
	cases := map[string]bluememo.AdoptedMemory{
		"no identifier": {Memory: bluememo.Memory{Content: "이샘플 leads the audit."}},
		"no content":    {Memory: bluememo.Memory{MemoryID: "carried-1"}},
		"an embedding without its model": {
			Memory:    bluememo.Memory{MemoryID: "carried-1", Content: "이샘플 leads the audit."},
			Embedding: []float32{0.1, 0.2},
		},
	}
	for name, candidate := range cases {
		t.Run(name, func(t *testing.T) {
			testFixture := newFixture(t)
			_, errorValue := testFixture.store.Adopt(context.Background(), []bluememo.AdoptedMemory{candidate})
			if !errors.Is(errorValue, bluememo.ErrAdoptedMemoryIncomplete) {
				t.Fatalf("error = %v, want ErrAdoptedMemoryIncomplete", errorValue)
			}
			if memories := testFixture.memories(t); len(memories) != 0 {
				t.Fatalf("holds %d memories, want none written", len(memories))
			}
		})
	}
}
