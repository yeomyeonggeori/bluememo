package bluememo_test

import (
	"context"
	"testing"

	"github.com/yeomyeonggeori/bluememo"
	"github.com/yeomyeonggeori/bluememo/bluememotest"
)

func TestReembedMovesEveryVectorOntoTheCurrentModel(t *testing.T) {
	testFixture := newFixture(t)
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
