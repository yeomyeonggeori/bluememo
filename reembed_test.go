package bluememo_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/yeomyeonggeori/bluememo"
	"github.com/yeomyeonggeori/bluememo/bluememotest"
)

func TestReembedRejectsAnObsoleteModelBeforeChangingFacts(t *testing.T) {
	repository := bluememo.NewInMemoryRepository()
	now := time.Now().UTC()
	episode := bluememo.Episode{EpisodeID: "original-episode", SourceKind: bluememo.EpisodeSourceKindExplicit, SourceID: "original-source", RequesterPersonID: "reader", Content: "source", OccurredAt: now}
	fact := bluememo.Fact{FactID: "original-fact", EpisodeID: episode.EpisodeID, OwnerPersonID: "reader", Kind: bluememo.FactKindFact, Content: "a source fact", EmbeddingModel: "original-model", ValidFrom: now}
	if errorValue := repository.SaveEpisode(context.Background(), bluememo.EpisodeWrite{Episode: episode, Facts: []bluememo.FactWrite{{Fact: fact}}}); errorValue != nil {
		t.Fatal(errorValue)
	}
	store := bluememo.Store{Facts: repository, Embedder: &bluememotest.HashEmbedder{}, EmbeddingModel: "current-model"}
	errorValue := (bluememo.ReembedJobHandler{Store: store}).Handle(context.Background(), bluememo.Job{Kind: bluememo.JobKindReembed, SubjectID: "old-model"})
	var terminal bluememo.TerminalJobError
	if !errors.As(errorValue, &terminal) {
		t.Fatalf("an obsolete model must fail before embedding, got %v", errorValue)
	}
	stored, _ := repository.FindFact(fact.FactID)
	if stored.EmbeddingModel != "original-model" {
		t.Fatalf("obsolete job changed the fact's embedding model: %+v", stored)
	}
}

func TestSearchOnlyRanksVectorsFromTheStoreModelAndReembedMovesTheRest(t *testing.T) {
	now := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	repository := bluememo.NewInMemoryRepository()
	embedder := &bluememotest.HashEmbedder{}
	store := bluememo.Store{Facts: repository, Jobs: repository, Embedder: embedder, EmbeddingModel: "new-model", Now: func() time.Time { return now }}
	episode := bluememo.Episode{EpisodeID: "episode-1", SourceKind: bluememo.EpisodeSourceKindExplicit, SourceID: "source-1", RequesterPersonID: "alice", Content: "transcript", OccurredAt: now}
	content := "the standup moved to 10am"
	oldEmbedding, _ := embedder.EmbedDocuments(context.Background(), []string{content})
	fact := bluememo.Fact{FactID: "fact-1", EpisodeID: episode.EpisodeID, OwnerPersonID: "alice", SubjectPersonID: "alice", Kind: bluememo.FactKindFact, Content: content, EmbeddingModel: "old-model", ValidFrom: now.Add(-time.Hour)}
	if errorValue := repository.SaveEpisode(context.Background(), bluememo.EpisodeWrite{Episode: episode, Facts: []bluememo.FactWrite{{Fact: fact, Embedding: oldEmbedding[0]}}}); errorValue != nil {
		t.Fatal(errorValue)
	}
	reader := bluememo.NewReader("alice", nil, nil, 0, nil)

	before := searchRanks(t, store, reader, content)
	if before.VectorRank != 0 || before.LexicalRank == 0 {
		t.Fatalf("expected a fact embedded by another model to rank lexically only, got %+v", before)
	}

	job, isNew, errorValue := store.EnqueueReembed(context.Background())
	if errorValue != nil || !isNew {
		t.Fatalf("expected a reembed job, got new=%v error=%v", isNew, errorValue)
	}
	if errorValue := (bluememo.ReembedJobHandler{Store: store}).Handle(context.Background(), job); errorValue != nil {
		t.Fatalf("expected the reembed to run: %v", errorValue)
	}

	after := searchRanks(t, store, reader, content)
	if after.VectorRank != 1 || after.Fact.EmbeddingModel != "new-model" {
		t.Fatalf("expected the fact to rank by vector under the store model after reembedding, got %+v", after)
	}
}

func searchRanks(t *testing.T, store bluememo.Store, reader bluememo.Reader, text string) bluememo.RankedFact {
	t.Helper()
	embedding, _ := store.Embedder.EmbedQuery(context.Background(), text)
	hits, errorValue := store.Facts.SearchFacts(context.Background(), bluememo.FactSearchQuery{Reader: reader, Text: text, Embedding: embedding, EmbeddingModel: store.EmbeddingModel, ReferenceTime: time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)})
	if errorValue != nil || len(hits) != 1 {
		t.Fatalf("expected one hit, got %d (%v)", len(hits), errorValue)
	}
	return hits[0]
}
