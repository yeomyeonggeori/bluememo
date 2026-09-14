package bluememo_test

import (
	"context"
	"testing"

	"github.com/yeomyeonggeori/bluememo"
	"github.com/yeomyeonggeori/bluememo/bluememotest"
)

type candidateQueryCapture struct {
	*bluememo.InMemoryRepository
	query bluememo.FactSearchQuery
}

func (repository *candidateQueryCapture) SearchFacts(ctx context.Context, query bluememo.FactSearchQuery) ([]bluememo.RankedFact, error) {
	repository.query = query
	return repository.InMemoryRepository.SearchFacts(ctx, query)
}

func TestIngestCandidatesUseConfiguredEmbeddingModel(t *testing.T) {
	for _, testCase := range []struct {
		name           string
		embeddingModel string
	}{
		{name: "custom model", embeddingModel: "test-embed"},
		{name: "empty model", embeddingModel: ""},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			assertIngestCandidateSearchUsesEmbeddingModel(t, testCase.embeddingModel)
		})
	}
}

func assertIngestCandidateSearchUsesEmbeddingModel(t *testing.T, embeddingModel string) {
	t.Helper()
	fixture := newIngestFixture()
	fixture.ingester.Store.EmbeddingModel = embeddingModel
	queryCapture := &candidateQueryCapture{InMemoryRepository: fixture.repository}
	fixture.ingester.Store.Facts = queryCapture
	fixture.model.Queue(bluememotest.IngestResponse())

	if _, errorValue := fixture.ingester.Ingest(context.Background(), fixture.request("a new episode")); errorValue != nil {
		t.Fatalf("expected ingest to succeed: %v", errorValue)
	}
	if queryCapture.query.EmbeddingModel != embeddingModel {
		t.Fatalf("expected candidate search to use configured embedding model %q, got %q", embeddingModel, queryCapture.query.EmbeddingModel)
	}
}
