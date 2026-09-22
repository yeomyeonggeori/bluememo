package bluememo_test

import (
	"context"
	"testing"
	"time"

	"github.com/yeomyeonggeori/bluememo"
	"github.com/yeomyeonggeori/bluememo/bluememotest"
)

func TestSiblingExpansionStopsAtTheReadersClearance(t *testing.T) {
	ctx := context.Background()
	now := ingestNow
	repository := bluememo.NewInMemoryRepository()
	store := bluememo.Store{Facts: repository, Profiles: repository, Jobs: repository, Embedder: &bluememotest.HashEmbedder{}, Now: func() time.Time { return now }}
	episode := bluememo.Episode{EpisodeID: "shift-handover", SourceKind: bluememo.EpisodeSourceKindExplicit, SourceID: "shift-handover", RequesterPersonID: "author", Content: "handover", OccurredAt: now}
	found := bluememo.Fact{FactID: "found", EpisodeID: episode.EpisodeID, OwnerPersonID: "author", CircleIDs: []string{"team"}, Kind: bluememo.FactKindFact, Content: "the night shift handover happens at seven", ValidFrom: now}
	classified := bluememo.Fact{FactID: "classified", EpisodeID: episode.EpisodeID, OwnerPersonID: "author", CircleIDs: []string{"team"}, Kind: bluememo.FactKindFact, Content: "the night shift headcount plan is frozen", SecurityLevelRank: 5, ValidFrom: now}
	write := bluememo.EpisodeWrite{Episode: episode, Facts: []bluememo.FactWrite{{Fact: found, Embedding: bluememotest.Embed(found.Content)}, {Fact: classified}}}
	if errorValue := repository.SaveEpisode(ctx, write); errorValue != nil {
		t.Fatal(errorValue)
	}

	uncleared := bluememo.NewReader("reader", []string{"team"}, nil, 1, nil)
	recall, errorValue := store.Recall(ctx, bluememo.RecallRequest{Reader: uncleared, Query: "when is the night shift handover"})
	if errorValue != nil {
		t.Fatal(errorValue)
	}
	if recalledContent(recall) != "the night shift handover happens at seven" {
		t.Fatalf("a sibling above the reader's clearance must not ride along with its episode, got %+v", recall.Facts)
	}

	cleared := bluememo.NewReader("reader", []string{"team"}, nil, 5, nil)
	clearedRecall, errorValue := store.Recall(ctx, bluememo.RecallRequest{Reader: cleared, Query: "when is the night shift handover"})
	if errorValue != nil {
		t.Fatal(errorValue)
	}
	if len(clearedRecall.Facts) != 2 {
		t.Fatalf("a reader who clears the sibling receives it, got %+v", clearedRecall.Facts)
	}
}

func recalledContent(recall bluememo.Recall) string {
	contents := ""
	for _, scored := range recall.Facts {
		contents += scored.Fact.Content
	}
	return contents
}
