package bluememo_test

import (
	"context"
	"testing"

	"github.com/yeomyeonggeori/bluememo"
	"github.com/yeomyeonggeori/bluememo/bluememotest"
)

func TestReembedMovesEveryVectorOntoTheCurrentModel(t *testing.T) {
	testFixture := newFixture(t, func(configuration *bluememo.Configuration) { configuration.RehearseTriggers = true })
	testFixture.judge.Queue(bluememo.Judgement{Relation: bluememo.RelationUnrelated, TargetIndex: -1, Importance: 4})
	testFixture.model.QueueTriggers("아침에만 커피를")
	testFixture.settle(t, "커피", statement("박예시는 아침에만 커피를 마신다."))
	testFixture.store.Close()

	moved, errorValue := bluememo.Open(context.Background(), testFixture.path, bluememo.Configuration{Embedder: bluememotest.HashEmbedder{}, EmbeddingModel: "hash-v2"})
	if errorValue != nil {
		t.Fatal(errorValue)
	}
	defer moved.Close()
	if result, _ := moved.Recall(context.Background(), "박예시는 아침에만 커피를 마신다", 1); result.Memories[0].VectorRank != 0 {
		t.Fatalf("a vector from another model must not rank, got %+v", result.Memories[0])
	}
	report, errorValue := moved.Reembed(context.Background(), 0)
	if errorValue != nil || report.Memories != 1 || report.Triggers != 1 {
		t.Fatalf("expected one memory and one trigger reembedded, got %+v (%v)", report, errorValue)
	}
	if result, _ := moved.Recall(context.Background(), "박예시는 아침에만 커피를 마신다", 1); result.Memories[0].VectorRank != 1 {
		t.Fatalf("after reembedding the vector should rank again, got %+v", result.Memories[0])
	}
}

func TestAMovedStoreSaysItsIndexIsStaleBeforeRecallGoesQuiet(t *testing.T) {
	testFixture := newFixture(t)
	testFixture.judge.Queue(bluememo.Judgement{Relation: bluememo.RelationUnrelated, TargetIndex: -1, Importance: 4})
	testFixture.settle(t, "부산", statement("이샘플은 부산에 산다."))
	if errorValue := testFixture.store.StoreFile(context.Background(), bluememo.File{
		FileID: "k7m2qx9fjh4t8", Name: "lease", Extension: "pdf",
		Medium: bluememo.MediumDocument, Summary: "The lease for the Busan office.",
	}); errorValue != nil {
		t.Fatalf("store file: %v", errorValue)
	}
	before, errorValue := testFixture.store.IndexState(context.Background())
	if errorValue != nil {
		t.Fatal(errorValue)
	}
	if before.Stale != 0 || before.Current == 0 {
		t.Fatalf("a store built by its own embedder is already stale: %+v", before)
	}
	testFixture.store.Close()

	moved, errorValue := bluememo.Open(context.Background(), testFixture.path, bluememo.Configuration{
		Embedder: bluememotest.HashEmbedder{}, EmbeddingModel: "somewhere-else"})
	if errorValue != nil {
		t.Fatal(errorValue)
	}
	defer moved.Close()

	arrived, errorValue := moved.IndexState(context.Background())
	if errorValue != nil {
		t.Fatal(errorValue)
	}
	if arrived.Stale != before.Current || arrived.Current != 0 {
		t.Fatalf("a moved store does not report what a recall cannot see: %+v", arrived)
	}
	if _, errorValue := moved.Reembed(context.Background(), 8); errorValue != nil {
		t.Fatalf("reembed: %v", errorValue)
	}
	settled, errorValue := moved.IndexState(context.Background())
	if errorValue != nil {
		t.Fatal(errorValue)
	}
	if settled.Stale != 0 || settled.Current != before.Current {
		t.Fatalf("reembedding did not make the store current: %+v", settled)
	}
}
