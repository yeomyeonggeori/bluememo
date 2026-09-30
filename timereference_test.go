package bluememo_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/yeomyeonggeori/bluememo"
	"github.com/yeomyeonggeori/bluememo/bluememotest"
)

type recordingEmbedder struct {
	bluememotest.HashEmbedder
	mutex     sync.Mutex
	documents []string
}

func (embedder *recordingEmbedder) EmbedDocuments(ctx context.Context, texts []string) ([][]float32, error) {
	embedder.mutex.Lock()
	embedder.documents = append(embedder.documents, texts...)
	embedder.mutex.Unlock()
	return embedder.HashEmbedder.EmbedDocuments(ctx, texts)
}

func datedStatement(content string, occurredOn string) bluememo.Proposition {
	return bluememo.Proposition{Content: content, Expiry: bluememo.ExpiryNone, OccurredOn: occurredOn}
}

func newTimeReferenceFixture(t *testing.T, embedder bluememo.Embedder, enabled bool) *fixture {
	return newFixture(t, func(configuration *bluememo.Configuration) {
		configuration.Embedder = embedder
		configuration.EmbedTimeReference = enabled
	})
}

func TestTimeReferencePrefixesOnlyTheEmbeddedTextOfADatedMemory(t *testing.T) {
	embedder := &recordingEmbedder{}
	testFixture := newTimeReferenceFixture(t, embedder, true)
	testFixture.judge.Queue(bluememo.Judgement{Relation: bluememo.RelationUnrelated, TargetIndex: -1, Importance: 2})
	testFixture.judge.Queue(bluememo.Judgement{Relation: bluememo.RelationUnrelated, TargetIndex: -1, Importance: 2})
	testFixture.settle(t, "출장", datedStatement("이샘플은 부산 출장을 다녀왔다.", "2026-08-10"))
	testFixture.settle(t, "커피", statement("박예시는 아침에만 커피를 마신다."))

	want := []string{"2026년 8월 10일, 지난달: 이샘플은 부산 출장을 다녀왔다.", "박예시는 아침에만 커피를 마신다."}
	if len(embedder.documents) != 2 || embedder.documents[0] != want[0] || embedder.documents[1] != want[1] {
		t.Fatalf("embedded %q, want %q", embedder.documents, want)
	}
	testFixture.memoryWithContent(t, "이샘플은 부산 출장을 다녀왔다.")
	testFixture.memoryWithContent(t, want[1])
	if len(testFixture.memories(t)) != 2 {
		t.Fatalf("stored content must stay bare, got %q", contents(testFixture.memories(t)))
	}
}

func TestTimeReferenceIsOffByDefault(t *testing.T) {
	embedder := &recordingEmbedder{}
	testFixture := newTimeReferenceFixture(t, embedder, false)
	testFixture.judge.Queue(bluememo.Judgement{Relation: bluememo.RelationUnrelated, TargetIndex: -1, Importance: 2})
	testFixture.settle(t, "출장", datedStatement("이샘플은 부산 출장을 다녀왔다.", "2026-08-10"))
	if len(embedder.documents) != 1 || embedder.documents[0] != "이샘플은 부산 출장을 다녀왔다." {
		t.Fatalf("embedded %q", embedder.documents)
	}
}

func TestTimeReferenceReadsTheStoreLocationAndClock(t *testing.T) {
	seoul, errorValue := time.LoadLocation("Asia/Seoul")
	if errorValue != nil {
		t.Skip("no timezone database")
	}
	embedder := &recordingEmbedder{}
	testFixture := newFixture(t, func(configuration *bluememo.Configuration) {
		configuration.Embedder = embedder
		configuration.EmbedTimeReference = true
		configuration.Location = seoul
	})
	testFixture.judge.Queue(bluememo.Judgement{Relation: bluememo.RelationUnrelated, TargetIndex: -1, Importance: 2})
	testFixture.settle(t, "출장", datedStatement("이샘플은 부산 출장을 다녀왔다.", "2025-12-31"))
	if want := "2025년 12월 31일, 9개월 전: 이샘플은 부산 출장을 다녀왔다."; embedder.documents[0] != want {
		t.Fatalf("embedded %q, want %q", embedder.documents[0], want)
	}
}

func TestReembedAppliesTheTimeReference(t *testing.T) {
	testFixture := newFixture(t)
	testFixture.judge.Queue(bluememo.Judgement{Relation: bluememo.RelationUnrelated, TargetIndex: -1, Importance: 2})
	testFixture.settle(t, "출장", datedStatement("이샘플은 부산 출장을 다녀왔다.", "2026-08-10"))
	testFixture.store.Close()

	embedder := &recordingEmbedder{}
	moved, errorValue := bluememo.Open(context.Background(), testFixture.path, bluememo.Configuration{
		Embedder: embedder, EmbeddingModel: "hash-v2", EmbedTimeReference: true, Now: testFixture.clock.now,
	})
	if errorValue != nil {
		t.Fatal(errorValue)
	}
	defer moved.Close()
	if _, errorValue := moved.Reembed(context.Background(), 0); errorValue != nil {
		t.Fatal(errorValue)
	}
	if want := "2026년 8월 10일, 지난달: 이샘플은 부산 출장을 다녀왔다."; len(embedder.documents) != 1 || embedder.documents[0] != want {
		t.Fatalf("reembedded %q, want %q", embedder.documents, want)
	}
	memories, errorValue := moved.Memories(context.Background())
	if errorValue != nil || len(memories) != 1 || memories[0].Content != "이샘플은 부산 출장을 다녀왔다." {
		t.Fatalf("reembedding must not touch content, got %+v (%v)", memories, errorValue)
	}
}
