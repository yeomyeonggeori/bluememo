package bluememo

import (
	"context"
	"strings"
	"testing"
	"time"
)

type phraseModel struct{ phrasesByFact map[string]string }

func (model phraseModel) GenerateStructured(_ context.Context, request StructuredRequest) (string, error) {
	for content, response := range model.phrasesByFact {
		if strings.Contains(request.Subject, content) {
			return response, nil
		}
	}
	return `{"phrases": []}`, nil
}

type keywordEmbedder struct{ axes []string }

func (embedder keywordEmbedder) EmbedQuery(_ context.Context, text string) ([]float32, error) {
	return embedder.vector(text), nil
}

func (embedder keywordEmbedder) EmbedDocuments(_ context.Context, texts []string) ([][]float32, error) {
	embeddings := make([][]float32, 0, len(texts))
	for _, text := range texts {
		embeddings = append(embeddings, embedder.vector(text))
	}
	return embeddings, nil
}

func (embedder keywordEmbedder) vector(text string) []float32 {
	vector := make([]float32, len(embedder.axes))
	for index, axis := range embedder.axes {
		if strings.Contains(text, axis) {
			vector[index] = 1
		}
	}
	vector[len(vector)-1] = 0.1
	return vector
}

func rehearsalFixture(t *testing.T) (Rehearser, *InMemoryRepository) {
	t.Helper()
	repository := NewInMemoryRepository()
	now := time.Date(2026, 9, 22, 0, 0, 0, 0, time.UTC)
	facts := []Fact{
		{FactID: "f-coffee", EpisodeID: "e-1", OwnerPersonID: "p-1", Kind: FactKindFact, Content: "박예시는 아침에만 커피를 마신다.", ValidFrom: now, CreatedAt: now, EmbeddingModel: "test"},
		{FactID: "f-release", EpisodeID: "e-1", OwnerPersonID: "p-1", Kind: FactKindFact, Content: "릴리스는 admind와 capabilityd를 함께 올린다.", ValidFrom: now, CreatedAt: now, EmbeddingModel: "test"},
	}
	if errorValue := repository.SaveEpisode(context.Background(), EpisodeWrite{
		Episode: Episode{EpisodeID: "e-1", RequesterPersonID: "p-1", SourceKind: EpisodeSourceKindExplicit, SourceID: "seed-1", Content: "두 가지 사실", OccurredAt: now},
		Facts:   []FactWrite{{Fact: facts[0]}, {Fact: facts[1]}},
	}); errorValue != nil {
		t.Fatalf("seed: %v", errorValue)
	}
	embedder := keywordEmbedder{axes: []string{"커피", "아침", "음료", "릴리스", "배포", "여기저기", "bias"}}
	store := Store{
		Facts:          repository,
		Triggers:       NewInMemoryTriggerRepository(repository),
		Jobs:           repository,
		Embedder:       embedder,
		EmbeddingModel: "test",
		Now:            func() time.Time { return now },
	}
	model := phraseModel{phrasesByFact: map[string]string{
		"커피":  `{"phrases": ["아침 음료 준비", "여기저기 쓰이는 말"]}`,
		"릴리스": `{"phrases": ["릴리스 순서", "여기저기 쓰이는 말"]}`,
	}}
	return Rehearser{Store: store, Model: model}, repository
}

func TestRehearseKeepsSpecificPhrasesAndDropsGeneralOnes(t *testing.T) {
	rehearser, _ := rehearsalFixture(t)
	result, errorValue := rehearser.Rehearse(context.Background(), "e-1")
	if errorValue != nil {
		t.Fatalf("rehearse: %v", errorValue)
	}
	if result.FactCount != 2 {
		t.Fatalf("expected both facts rehearsed, got %d", result.FactCount)
	}
	kept := map[string][]string{}
	for _, factID := range []string{"f-coffee", "f-release"} {
		for _, trigger := range rehearser.Store.Triggers.(*InMemoryTriggerRepository).TriggersForFact(factID) {
			kept[factID] = append(kept[factID], trigger.Phrase)
		}
	}
	t.Logf("kept %v, dropped %d of %d", kept, result.DroppedCount, result.PhraseCount+result.DroppedCount)
	if len(kept["f-coffee"]) != 1 || kept["f-coffee"][0] != "아침 음료 준비" {
		t.Errorf("the coffee fact should keep only its specific phrase, kept %v", kept["f-coffee"])
	}
	if len(kept["f-release"]) != 1 || kept["f-release"][0] != "릴리스 순서" {
		t.Errorf("the release fact should keep only its specific phrase, kept %v", kept["f-release"])
	}
	if result.DroppedCount != 2 {
		t.Errorf("both copies of the phrase that fits anything should be dropped, dropped %d", result.DroppedCount)
	}
}

func TestRehearseWithoutTriggerRepositoryFailsTerminally(t *testing.T) {
	rehearser, _ := rehearsalFixture(t)
	rehearser.Store.Triggers = nil
	_, errorValue := rehearser.Rehearse(context.Background(), "e-1")
	var terminal TerminalJobError
	if !asTerminal(errorValue, &terminal) {
		t.Fatalf("expected a terminal error when triggers are unconfigured, got %v", errorValue)
	}
}

func TestIngestEnqueuesRehearsalOnlyWhenTriggersExist(t *testing.T) {
	rehearser, repository := rehearsalFixture(t)
	if errorValue := rehearser.Store.EnqueueRehearsal(context.Background(), "e-1"); errorValue != nil {
		t.Fatalf("enqueue: %v", errorValue)
	}
	jobs, errorValue := repository.ClaimDueJobs(context.Background(), []string{JobKindRehearse}, rehearser.Store.now(), time.Minute, 4)
	if errorValue != nil || len(jobs) != 1 {
		t.Fatalf("expected one rehearse job, got %d (%v)", len(jobs), errorValue)
	}
	if jobs[0].SubjectID != "e-1" {
		t.Fatalf("rehearse job should name the episode, got %q", jobs[0].SubjectID)
	}
	storeWithoutTriggers := rehearser.Store
	storeWithoutTriggers.Triggers = nil
	if errorValue := storeWithoutTriggers.EnqueueRehearsal(context.Background(), "e-2"); errorValue != nil {
		t.Fatalf("enqueue without triggers should be a quiet no-op, got %v", errorValue)
	}
	jobs, _ = repository.ClaimDueJobs(context.Background(), []string{JobKindRehearse}, rehearser.Store.now(), time.Minute, 4)
	if len(jobs) != 0 {
		t.Fatalf("no rehearse job should exist when the store has no trigger repository, got %d", len(jobs))
	}
}

func asTerminal(errorValue error, target *TerminalJobError) bool {
	terminal, isTerminal := errorValue.(TerminalJobError)
	if isTerminal {
		*target = terminal
	}
	return isTerminal
}
