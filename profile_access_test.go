package bluememo_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/yeomyeonggeori/bluememo"
	"github.com/yeomyeonggeori/bluememo/bluememotest"
)

func TestProfileUsesOnlyCurrentReaderFacts(t *testing.T) {
	ctx := context.Background()
	repository := bluememo.NewInMemoryRepository()
	model := bluememotest.NewScriptedModel()
	now := ingestNow
	store := bluememo.Store{Facts: repository, Profiles: repository, Jobs: repository, Now: func() time.Time { return now }}
	episode := bluememo.Episode{EpisodeID: "profile-source", SourceKind: bluememo.EpisodeSourceKindExplicit, SourceID: "profile-source", RequesterPersonID: "author", Content: "source facts", OccurredAt: now}
	private := bluememo.Fact{FactID: "private-about-reader", EpisodeID: episode.EpisodeID, OwnerPersonID: "author", SubjectPersonID: "reader", Kind: bluememo.FactKindIdentity, Content: "private source content", ValidFrom: now}
	shared := private
	shared.FactID = "shared-about-reader"
	shared.Kind = bluememo.FactKindTemporary
	shared.Content = "shared source content"
	shared.CircleIDs = []string{"team"}
	shared.SecurityLevelRank = 2
	shared.RequiredClasses = []string{"restricted"}
	shared.ValidUntil = now.Add(time.Hour)
	if errorValue := repository.SaveEpisode(ctx, bluememo.EpisodeWrite{Episode: episode, Facts: []bluememo.FactWrite{{Fact: private}, {Fact: shared}}}); errorValue != nil {
		t.Fatal(errorValue)
	}
	reader := bluememo.NewReader("reader", []string{"team"}, nil, 2, []string{"restricted"})
	model.Queue(bluememotest.ProfileResponse([]string{"shared summary"}, nil))
	profile, errorValue := (bluememo.ProfileBuilder{Store: store, Model: model, Now: store.Now}).Rebuild(ctx, reader)
	if errorValue != nil || profile.BuiltFromFactCount != 1 || strings.Contains(model.LastSubject(), private.Content) || !strings.Contains(model.LastSubject(), shared.Content) {
		t.Fatalf("profile must only receive readable sources: %+v, %v, %s", profile, errorValue, model.LastSubject())
	}
	assertProfileLineCount(t, store, reader, 1)
	for _, restricted := range []bluememo.Reader{
		bluememo.NewReader("reader", nil, nil, 2, []string{"restricted"}),
		bluememo.NewReader("reader", []string{"team"}, nil, 1, []string{"restricted"}),
		bluememo.NewReader("reader", []string{"team"}, nil, 2, nil),
		bluememo.NewReader("someone-else", []string{"team"}, nil, 2, []string{"restricted"}),
	} {
		assertProfileLineCount(t, store, restricted, 0)
	}
	now = now.Add(2 * time.Hour)
	assertProfileLineCount(t, store, reader, 0)
	now = ingestNow
	if _, errorValue := store.Forget(ctx, reader, []string{shared.FactID}, "requested"); errorValue != nil {
		t.Fatal(errorValue)
	}
	assertProfileLineCount(t, store, reader, 0)
}

func TestLegacyProfileIsPreservedButNotRecalled(t *testing.T) {
	repository := bluememo.NewInMemoryRepository()
	store := bluememo.Store{Facts: repository, Profiles: repository, Jobs: repository}
	if errorValue := repository.SaveProfile(context.Background(), bluememo.Profile{PersonID: "reader", IdentityLines: []string{"unverified legacy content"}}); errorValue != nil {
		t.Fatal(errorValue)
	}
	assertProfileLineCount(t, store, bluememo.NewReader("reader", nil, nil, 0, nil), 0)
	stored, isFound, errorValue := repository.FindProfile(context.Background(), "reader")
	if errorValue != nil || !isFound || len(stored.IdentityLines) != 1 {
		t.Fatalf("read repair must preserve stored content: %+v, %v", stored, errorValue)
	}
}

func assertProfileLineCount(t *testing.T, store bluememo.Store, reader bluememo.Reader, expectedCount int) {
	t.Helper()
	recall, errorValue := store.Recall(context.Background(), bluememo.RecallRequest{Reader: reader})
	if errorValue != nil || len(recall.ProfileLines()) != expectedCount {
		t.Fatalf("expected %d recall profile lines, got %+v (%v)", expectedCount, recall, errorValue)
	}
	profile, _, errorValue := store.ListReadable(context.Background(), reader, 100)
	if errorValue != nil || len(profile.IdentityLines)+len(profile.CurrentLines) != expectedCount {
		t.Fatalf("expected %d listed profile lines, got %+v (%v)", expectedCount, profile, errorValue)
	}
}
