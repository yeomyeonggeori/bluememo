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

	want := []string{"2026-08-10: 이샘플은 부산 출장을 다녀왔다.", "박예시는 아침에만 커피를 마신다."}
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

func TestTimeReferenceReadsTheStoreLocation(t *testing.T) {
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
	testFixture.judge.Queue(bluememo.Judgement{Relation: bluememo.RelationUnrelated, TargetIndex: -1, Importance: 2})
	testFixture.settle(t, "출장", datedStatement("이샘플은 부산 출장을 다녀왔다.", "2025-12-31"))
	testFixture.settle(t, "통화", datedStatement("이샘플이 거래처와 통화했다.", "2026-03-05T06:30Z"))
	wanted := []string{"2025-12-31: 이샘플은 부산 출장을 다녀왔다.", "2026-03-05T15:30: 이샘플이 거래처와 통화했다."}
	for index, want := range wanted {
		if embedder.documents[index] != want {
			t.Fatalf("embedded %q, want %q", embedder.documents[index], want)
		}
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
	if want := "2026-08-10: 이샘플은 부산 출장을 다녀왔다."; len(embedder.documents) != 1 || embedder.documents[0] != want {
		t.Fatalf("reembedded %q, want %q", embedder.documents, want)
	}
	memories, errorValue := moved.Memories(context.Background())
	if errorValue != nil || len(memories) != 1 || memories[0].Content != "이샘플은 부산 출장을 다녀왔다." {
		t.Fatalf("reembedding must not touch content, got %+v (%v)", memories, errorValue)
	}
}

func spannedStatement(content string, occurredOn string, occurredUntil string) bluememo.Proposition {
	occurrence := occurredOn
	if occurredUntil != "" {
		occurrence = occurredOn + "/" + occurredUntil
	}
	return bluememo.Proposition{Content: content, Expiry: bluememo.ExpiryNone, OccurredOn: occurrence}
}

func TestTimeReferenceRendersTheStoredRangeExactly(t *testing.T) {
	cases := []struct {
		name          string
		occurredOn    string
		occurredUntil string
		want          string
	}{
		{"one day", "2026-03-05", "", "2026-03-05: 이샘플은 부산에 갔다."},
		{"a span ending where it starts", "2026-03-05", "2026-03-05", "2026-03-05: 이샘플은 부산에 갔다."},
		{"a whole month stays a range", "2026-06-01", "2026-06-30", "2026-06-01/2026-06-30: 이샘플은 부산에 갔다."},
		{"a whole year stays a range", "2022-01-01", "2022-12-31", "2022-01-01/2022-12-31: 이샘플은 부산에 갔다."},
		{"a time of day", "2026-03-05T15:00", "", "2026-03-05T15:00: 이샘플은 부산에 갔다."},
		{"an offset is accepted", "2026-03-05T06:30Z", "", "2026-03-05T06:30: 이샘플은 부산에 갔다."},
		{"a span of two instants", "2026-03-05T09:00", "2026-03-05T11:30", "2026-03-05T09:00/2026-03-05T11:30: 이샘플은 부산에 갔다."},
		{"a span ending before it starts", "2026-03-05", "2026-03-01", "2026-03-05: 이샘플은 부산에 갔다."},
		{"an end without a start", "", "2026-03-09", "이샘플은 부산에 갔다."},
		{"no time at all", "", "", "이샘플은 부산에 갔다."},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			embedder := &recordingEmbedder{}
			testFixture := newTimeReferenceFixture(t, embedder, true)
			testFixture.judge.Queue(bluememo.Judgement{Relation: bluememo.RelationUnrelated, TargetIndex: -1, Importance: 2})
			testFixture.settle(t, "출장", spannedStatement("이샘플은 부산에 갔다.", testCase.occurredOn, testCase.occurredUntil))
			if len(embedder.documents) != 1 || embedder.documents[0] != testCase.want {
				t.Fatalf("embedded %q, want %q", embedder.documents, testCase.want)
			}
		})
	}
}

func TestOccurredUntilSurvivesAWriteAndARead(t *testing.T) {
	testFixture := newFixture(t)
	testFixture.judge.Queue(bluememo.Judgement{Relation: bluememo.RelationUnrelated, TargetIndex: -1, Importance: 2})
	testFixture.judge.Queue(bluememo.Judgement{Relation: bluememo.RelationUnrelated, TargetIndex: -1, Importance: 2})
	testFixture.settle(t, "그림", spannedStatement("이샘플은 호수 일출을 그렸다.", "2022-01-01", "2022-12-31"))
	testFixture.settle(t, "출장", datedStatement("박예시는 부산 출장을 다녀왔다.", "2026-08-10"))

	spanned := testFixture.memoryWithContent(t, "이샘플은 호수 일출을 그렸다.")
	if want := time.Date(2022, 1, 1, 0, 0, 0, 0, time.UTC); !spanned.OccurredAt.Equal(want) {
		t.Fatalf("occurredAt %v, want %v", spanned.OccurredAt, want)
	}
	if want := time.Date(2022, 12, 31, 0, 0, 0, 0, time.UTC); !spanned.OccurredUntil.Equal(want) {
		t.Fatalf("occurredUntil %v, want %v", spanned.OccurredUntil, want)
	}
	if day := testFixture.memoryWithContent(t, "박예시는 부산 출장을 다녀왔다."); !day.OccurredUntil.IsZero() {
		t.Fatalf("a single day must have no span, got %v", day.OccurredUntil)
	}
}

func TestReembedRendersTheStoredSpan(t *testing.T) {
	testFixture := newFixture(t)
	testFixture.judge.Queue(bluememo.Judgement{Relation: bluememo.RelationUnrelated, TargetIndex: -1, Importance: 2})
	testFixture.settle(t, "그림", spannedStatement("이샘플은 호수 일출을 그렸다.", "2022-01-01", "2022-12-31"))
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
	if want := "2022-01-01/2022-12-31: 이샘플은 호수 일출을 그렸다."; len(embedder.documents) != 1 || embedder.documents[0] != want {
		t.Fatalf("reembedded %q, want %q", embedder.documents, want)
	}
}
