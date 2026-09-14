package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/yeomyeonggeori/bluememo"
)

func main() {
	if errorValue := run(); errorValue != nil {
		fmt.Fprintln(os.Stderr, errorValue)
		os.Exit(1)
	}
}

func run() error {
	contextValue := context.Background()
	referenceTime := time.Date(2026, 9, 2, 9, 0, 0, 0, time.UTC)
	repository := bluememo.NewInMemoryRepository()
	episode := bluememo.Episode{
		EpisodeID:         "example-episode",
		SourceKind:        bluememo.EpisodeSourceKindExplicit,
		SourceID:          "example-source",
		RequesterPersonID: "person-owner",
		Content:           "synthetic example facts",
		OccurredAt:        referenceTime,
	}
	facts := []bluememo.FactWrite{
		{Fact: privateFact("owner-fact", episode.EpisodeID, "person-owner", "이샘플 prefers concise project summaries", referenceTime)},
		{Fact: privateFact("other-fact", episode.EpisodeID, "person-other", "박예시 prefers detailed project summaries", referenceTime)},
	}
	if errorValue := repository.SaveEpisode(contextValue, bluememo.EpisodeWrite{Episode: episode, Facts: facts}); errorValue != nil {
		return errorValue
	}
	store := bluememo.Store{Facts: repository, Now: func() time.Time { return referenceTime }}
	reader := bluememo.NewReader("person-owner", nil, nil, 0, nil)
	recall, errorValue := store.Recall(contextValue, bluememo.RecallRequest{Reader: reader, Query: "project summaries"})
	if errorValue != nil {
		return errorValue
	}
	if len(recall.Facts) != 1 || recall.Facts[0].Fact.Content != "이샘플 prefers concise project summaries" {
		return fmt.Errorf("expected the reader to recall their own private fact only, got %+v", recall.Facts)
	}
	fmt.Printf("The reader can recall: %s\n", recall.Facts[0].Fact.Content)
	return nil
}

func privateFact(factID string, episodeID string, ownerPersonID string, content string, validFrom time.Time) bluememo.Fact {
	return bluememo.Fact{
		FactID:          factID,
		EpisodeID:       episodeID,
		OwnerPersonID:   ownerPersonID,
		SubjectPersonID: ownerPersonID,
		Kind:            bluememo.FactKindPreference,
		Content:         content,
		ValidFrom:       validFrom,
	}
}
