package bluememo_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/yeomyeonggeori/bluememo"
	"github.com/yeomyeonggeori/bluememo/bluememotest"
)

type namedEmbedder struct {
	bluememotest.HashEmbedder
}

func (namedEmbedder) EmbeddingModelName() string { return "embeddinggemma" }

func TestAnEmbedderThatNamesItselfSavesTheHostFromNamingItTwice(t *testing.T) {
	ctx := context.Background()
	store, errorValue := bluememo.Open(ctx, filepath.Join(t.TempDir(), "named.db"), bluememo.Configuration{
		Embedder: namedEmbedder{},
	})
	if errorValue != nil {
		t.Fatalf("open: %v", errorValue)
	}
	defer store.Close()

	state, errorValue := store.IndexState(ctx)
	if errorValue != nil {
		t.Fatalf("index state: %v", errorValue)
	}
	if state.EmbeddingModel != "embeddinggemma" {
		t.Fatalf("the store labels its vectors %q, so a name the embedder already knows had to be written twice", state.EmbeddingModel)
	}
}

func TestAChooserAloneIsEnoughToJudge(t *testing.T) {
	ctx := context.Background()
	store, errorValue := bluememo.Open(ctx, filepath.Join(t.TempDir(), "chooser.db"), bluememo.Configuration{
		Embedder: namedEmbedder{},
		Model:    settlingModel{},
		Chooser:  answeringChooser{},
	})
	if errorValue != nil {
		t.Fatalf("open: %v", errorValue)
	}
	defer store.Close()

	if errorValue := store.Memorize(ctx, bluememo.Note{SpeakerName: "Alex", Body: "I lead the payments team"}); errorValue != nil {
		t.Fatalf("memorize: %v", errorValue)
	}
	report, errorValue := store.Settle(ctx)
	if errorValue != nil {
		t.Fatalf("a store given a chooser and no judge could not settle: %v", errorValue)
	}
	if report.Inserted == 0 {
		t.Fatal("a store given a chooser and no judge settled nothing")
	}
}

// answeringChooser answers each of the judge's questions the one way that
// keeps a statement: unrelated to anything held, and worth more than noise.
type answeringChooser struct{}

func (answeringChooser) Choose(_ context.Context, request bluememo.ChoiceRequest) (map[string]float64, error) {
	answer := request.Answers[0]
	switch request.Instruction {
	case bluememo.RelationInstruction:
		answer = "4"
	case bluememo.ImportanceInstruction:
		answer = "3"
	}
	distribution := map[string]float64{}
	for _, candidate := range request.Answers {
		distribution[candidate] = 0.01
	}
	distribution[answer] = 0.9
	return distribution, nil
}

type settlingModel struct{}

func (settlingModel) GenerateStructured(_ context.Context, request bluememo.StructuredRequest) (string, error) {
	return `{"propositions":[{"content":"Alex leads the payments team"}]}`, nil
}
