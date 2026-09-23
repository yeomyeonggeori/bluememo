package bluememo_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/yeomyeonggeori/bluememo"
	"github.com/yeomyeonggeori/bluememo/bluememotest"
)

type fixture struct {
	store *bluememo.Store
	model *bluememotest.ScriptedModel
	judge *bluememotest.ScriptedJudge
	clock *clock
	path  string
}

type clock struct {
	current time.Time
}

func (clock *clock) now() time.Time {
	return clock.current
}

func (clock *clock) advance(duration time.Duration) {
	clock.current = clock.current.Add(duration)
}

func newFixture(t *testing.T, adjust ...func(*bluememo.Configuration)) *fixture {
	t.Helper()
	testFixture := &fixture{
		model: bluememotest.NewScriptedModel(),
		judge: &bluememotest.ScriptedJudge{},
		clock: &clock{current: time.Date(2026, 9, 21, 9, 0, 0, 0, time.UTC)},
		path:  filepath.Join(t.TempDir(), "memory.db"),
	}
	configuration := bluememo.Configuration{
		Embedder:       bluememotest.HashEmbedder{},
		EmbeddingModel: "hash",
		Model:          testFixture.model,
		Judge:          testFixture.judge,
		Now:            testFixture.clock.now,
	}
	for _, change := range adjust {
		change(&configuration)
	}
	store, errorValue := bluememo.Open(context.Background(), testFixture.path, configuration)
	if errorValue != nil {
		t.Fatalf("open store: %v", errorValue)
	}
	t.Cleanup(func() { store.Close() })
	testFixture.store = store
	return testFixture
}

func (testFixture *fixture) settle(t *testing.T, body string, propositions ...bluememo.Proposition) bluememo.SettleReport {
	t.Helper()
	testFixture.model.QueueDecomposition(propositions...)
	if errorValue := testFixture.store.Memorize(context.Background(), bluememo.Note{Body: body, SpeakerName: "이샘플"}); errorValue != nil {
		t.Fatalf("memorize: %v", errorValue)
	}
	report, errorValue := testFixture.store.Settle(context.Background())
	if errorValue != nil {
		t.Fatalf("settle: %v", errorValue)
	}
	return report
}

func (testFixture *fixture) memories(t *testing.T) []bluememo.Memory {
	t.Helper()
	memories, errorValue := testFixture.store.Memories(context.Background())
	if errorValue != nil {
		t.Fatalf("list memories: %v", errorValue)
	}
	return memories
}

func (testFixture *fixture) recall(t *testing.T, query string, limit int) bluememo.RecallResult {
	t.Helper()
	result, errorValue := testFixture.store.Recall(context.Background(), query, limit)
	if errorValue != nil {
		t.Fatalf("recall %q: %v", query, errorValue)
	}
	return result
}

func (testFixture *fixture) memoryWithContent(t *testing.T, content string) bluememo.Memory {
	t.Helper()
	for _, memory := range testFixture.memories(t) {
		if memory.Content == content {
			return memory
		}
	}
	t.Fatalf("no live memory says %q", content)
	return bluememo.Memory{}
}

func statement(content string) bluememo.Proposition {
	return bluememo.Proposition{Content: content, Expiry: bluememo.ExpiryNone}
}

func trait(content string) bluememo.Proposition {
	return bluememo.Proposition{Content: content, IsStatic: true, Expiry: bluememo.ExpiryNone}
}

func contents(memories []bluememo.Memory) []string {
	values := make([]string, len(memories))
	for index, memory := range memories {
		values[index] = memory.Content
	}
	return values
}

func recalledContents(result bluememo.RecallResult) []string {
	values := make([]string, len(result.Memories))
	for index, entry := range result.Memories {
		values[index] = entry.Memory.Content
	}
	return values
}
