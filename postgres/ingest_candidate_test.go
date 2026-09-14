package postgres_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/yeomyeonggeori/bluememo"
	"github.com/yeomyeonggeori/bluememo/bluememotest"
)

func TestPostgresIngestCandidatesUseConfiguredEmbeddingModel(t *testing.T) {
	fixture := openFixture(t)
	if !fixture.hasVector {
		t.Skip("the database has no vector extension")
	}

	ctx := context.Background()
	embeddingModel := "ingest-candidate-test-model"
	seedEpisode := fixture.episode("alice")
	seededFact := fixture.privateFact(seedEpisode.EpisodeID, "alice", "quartz ledger cobalt index")
	seededFact.EmbeddingModel = embeddingModel
	fixture.save(t, seedEpisode, bluememo.FactWrite{Fact: seededFact, Embedding: unitEmbedding(0)})

	scripted := bluememotest.NewScriptedModel()
	scripted.Queue(bluememotest.IngestResponse(bluememotest.IngestFact{
		Content:       seededFact.Content,
		Kind:          seededFact.Kind,
		Relation:      bluememo.FactRelationReinforces,
		RelatedFactID: seededFact.FactID,
	}))
	store := bluememo.Store{
		Facts:          fixture.facts,
		Jobs:           fixture.jobs,
		Embedder:       candidateTestEmbedder{embedding: unitEmbedding(0)},
		EmbeddingModel: embeddingModel,
		Now:            func() time.Time { return fixture.now },
	}
	ingester := bluememo.Ingester{Store: store, Model: scripted, Now: func() time.Time { return fixture.now }}
	request := bluememo.IngestRequest{
		Episode:       fixture.episode("alice"),
		Reader:        bluememo.NewReader("alice", nil, nil, 0, nil),
		RequesterName: "이샘플",
		Label:         bluememo.SecurityLabel{RequiredClasses: []string{}},
	}
	request.Episode.Content = "juniper gearbox inspected"
	lexicalHits, errorValue := fixture.facts.SearchFacts(ctx, bluememo.FactSearchQuery{
		Reader:         request.Reader,
		Text:           request.Episode.Content,
		EmbeddingModel: embeddingModel,
		CandidateLimit: bluememo.DefaultSearchCandidateLimit,
		ReferenceTime:  fixture.now,
	})
	if errorValue != nil || len(lexicalHits) != 0 {
		t.Fatalf("expected no lexical candidates for disjoint words, got %+v (%v)", lexicalHits, errorValue)
	}

	result, errorValue := ingester.Ingest(ctx, request)
	if errorValue != nil {
		t.Fatalf("expected ingest to reinforce the vector candidate: %v", errorValue)
	}
	if result.CandidateCount != 1 || len(result.ReinforcedFactIDs) != 1 || result.ReinforcedFactIDs[0] != seededFact.FactID {
		t.Fatalf("expected one candidate and the seeded fact reinforced, got %+v", result)
	}
	if !strings.Contains(scripted.LastSubject(), "id="+seededFact.FactID) {
		t.Fatalf("expected the vector candidate offered to the model, got %s", scripted.LastSubject())
	}
	storedFacts, errorValue := fixture.facts.ListFactsByID(ctx, request.Reader, []string{seededFact.FactID}, fixture.now)
	if errorValue != nil || len(storedFacts) != 1 || storedFacts[0].ReinforcementCount != 2 {
		t.Fatalf("expected PostgreSQL to record one reinforcement, got %+v (%v)", storedFacts, errorValue)
	}
}

type candidateTestEmbedder struct {
	embedding []float32
}

func (embedder candidateTestEmbedder) EmbedQuery(context.Context, string) ([]float32, error) {
	return embedder.embedding, nil
}

func (embedder candidateTestEmbedder) EmbedDocuments(_ context.Context, texts []string) ([][]float32, error) {
	embeddings := make([][]float32, len(texts))
	for index := range texts {
		embeddings[index] = embedder.embedding
	}
	return embeddings, nil
}
