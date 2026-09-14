package postgres_test

import (
	"context"
	"reflect"
	"sync"
	"testing"

	"github.com/yeomyeonggeori/bluememo"
)

func TestConcurrentEpisodeReplayReturnsCanonicalReceipt(t *testing.T) {
	fixture := openFixture(t)
	firstEpisode := fixture.episode("alice")
	firstEpisode.SourceID = "concurrent-replay-source"
	firstEpisode.Content = "the same source payload"
	secondEpisode := firstEpisode
	secondEpisode.EpisodeID = bluememo.NewIdentifier()
	firstFact := fixture.privateFact(firstEpisode.EpisodeID, "alice", "first attempted fact")
	secondFact := fixture.privateFact(secondEpisode.EpisodeID, "alice", "second attempted fact")
	writes := []bluememo.EpisodeWrite{
		{Episode: firstEpisode, Facts: []bluememo.FactWrite{{Fact: firstFact}}, CandidateCount: 3},
		{Episode: secondEpisode, Facts: []bluememo.FactWrite{{Fact: secondFact}}, CandidateCount: 7},
	}
	start := make(chan struct{})
	errorValues := make(chan error, len(writes))
	var waitGroup sync.WaitGroup
	for _, write := range writes {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			<-start
			errorValues <- fixture.facts.SaveEpisode(context.Background(), write)
		}()
	}
	close(start)
	waitGroup.Wait()
	close(errorValues)
	for errorValue := range errorValues {
		if errorValue != nil {
			t.Fatalf("expected concurrent source replay to succeed: %v", errorValue)
		}
	}
	firstReceipt, firstFound, errorValue := fixture.facts.FindEpisodeReceipt(context.Background(), firstEpisode)
	if errorValue != nil || !firstFound {
		t.Fatalf("expected the first request to find the canonical receipt, got found=%v (%v)", firstFound, errorValue)
	}
	secondReceipt, secondFound, errorValue := fixture.facts.FindEpisodeReceipt(context.Background(), secondEpisode)
	if errorValue != nil || !secondFound || !reflect.DeepEqual(firstReceipt, secondReceipt) {
		t.Fatalf("expected both requests to return the same canonical receipt, got first=%+v second=%+v found=%v (%v)", firstReceipt, secondReceipt, secondFound, errorValue)
	}
	if firstReceipt.EpisodeID != firstEpisode.EpisodeID && firstReceipt.EpisodeID != secondEpisode.EpisodeID {
		t.Fatalf("expected the receipt to name one attempted episode, got %+v", firstReceipt)
	}
	var episodeCount int
	if errorValue := fixture.database.QueryRow(`SELECT count(*) FROM memory_episode WHERE source_kind = $1 AND source_id = $2`, firstEpisode.SourceKind, firstEpisode.SourceID).Scan(&episodeCount); errorValue != nil || episodeCount != 1 {
		t.Fatalf("expected one stored episode for the source, got %d (%v)", episodeCount, errorValue)
	}
	var factCount int
	if errorValue := fixture.database.QueryRow(`SELECT count(*) FROM memory_fact WHERE episode_id = ANY($1::text[])`, []string{firstEpisode.EpisodeID, secondEpisode.EpisodeID}).Scan(&factCount); errorValue != nil || factCount != 1 {
		t.Fatalf("expected one attempted fact to be stored, got %d (%v)", factCount, errorValue)
	}
	if len(firstReceipt.FactIDs) != 1 || firstReceipt.FactIDs[0] != firstFact.FactID && firstReceipt.FactIDs[0] != secondFact.FactID {
		t.Fatalf("expected the canonical receipt to name the winning fact, got %+v", firstReceipt)
	}
	if firstReceipt.EpisodeID == firstEpisode.EpisodeID && firstReceipt.FactIDs[0] != firstFact.FactID || firstReceipt.EpisodeID == secondEpisode.EpisodeID && firstReceipt.FactIDs[0] != secondFact.FactID {
		t.Fatalf("expected the canonical receipt's fact to belong to its episode, got %+v", firstReceipt)
	}
}

func TestRepeatedReinforcementSourceIncrementsOnce(t *testing.T) {
	fixture := openFixture(t)
	ctx := context.Background()
	initialEpisode := fixture.episode("alice")
	initialFact := fixture.privateFact(initialEpisode.EpisodeID, "alice", "a durable preference")
	fixture.save(t, initialEpisode, bluememo.FactWrite{Fact: initialFact})
	reinforcementEpisode := fixture.episode("alice")
	reinforcementEpisode.SourceID = "reinforcement-replay-source"
	write := bluememo.EpisodeWrite{
		Episode: reinforcementEpisode,
		Facts:   []bluememo.FactWrite{{ReinforcesFactID: initialFact.FactID}},
	}
	for range 2 {
		if errorValue := fixture.facts.SaveEpisode(ctx, write); errorValue != nil {
			t.Fatalf("expected repeated reinforcement replay to succeed: %v", errorValue)
		}
	}
	var reinforcementCount int
	if errorValue := fixture.database.QueryRow(`SELECT reinforcement_count FROM memory_fact WHERE fact_id = $1`, initialFact.FactID).Scan(&reinforcementCount); errorValue != nil || reinforcementCount != 2 {
		t.Fatalf("expected one reinforcement effect, got count %d (%v)", reinforcementCount, errorValue)
	}
	receipt, isFound, errorValue := fixture.facts.FindEpisodeReceipt(ctx, reinforcementEpisode)
	if errorValue != nil || !isFound || !reflect.DeepEqual(receipt.ReinforcedFactIDs, []string{initialFact.FactID}) {
		t.Fatalf("expected the canonical receipt to record one reinforcement, got %+v found=%v (%v)", receipt, isFound, errorValue)
	}
}

func TestEpisodeAndForgetTransactionsRollBackWhenProfileQueueFails(t *testing.T) {
	fixture := openFixture(t)
	ctx := context.Background()
	seedEpisode := fixture.episode("alice")
	seedFact := fixture.privateFact(seedEpisode.EpisodeID, "alice", "a fact to forget")
	fixture.save(t, seedEpisode, bluememo.FactWrite{Fact: seedFact})
	functionName := "reject_memory_job_" + bluememo.NewIdentifier()
	triggerName := "reject_memory_job_insert_" + bluememo.NewIdentifier()
	if _, errorValue := fixture.database.Exec(`CREATE FUNCTION ` + functionName + `() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'memory job enqueue rejected'; END; $$`); errorValue != nil {
		t.Fatal(errorValue)
	}
	if _, errorValue := fixture.database.Exec(`CREATE TRIGGER ` + triggerName + ` BEFORE INSERT OR UPDATE ON memory_job FOR EACH ROW EXECUTE FUNCTION ` + functionName + `()`); errorValue != nil {
		_, _ = fixture.database.Exec(`DROP FUNCTION ` + functionName + `()`)
		t.Fatal(errorValue)
	}
	t.Cleanup(func() {
		if _, errorValue := fixture.database.Exec(`DROP TRIGGER IF EXISTS ` + triggerName + ` ON memory_job`); errorValue != nil {
			t.Errorf("expected the memory job trigger to drop: %v", errorValue)
		}
		if _, errorValue := fixture.database.Exec(`DROP FUNCTION IF EXISTS ` + functionName + `()`); errorValue != nil {
			t.Errorf("expected the memory job trigger function to drop: %v", errorValue)
		}
	})
	newEpisode := fixture.episode("bob")
	newEpisode.SourceID = "failed-profile-queue-source"
	newFact := fixture.privateFact(newEpisode.EpisodeID, "bob", "a fact whose profile enqueue fails")
	if errorValue := fixture.facts.SaveEpisode(ctx, bluememo.EpisodeWrite{Episode: newEpisode, Facts: []bluememo.FactWrite{{Fact: newFact}}}); errorValue == nil {
		t.Fatal("expected profile queue failure to fail the episode transaction")
	}
	var storedEpisodeCount int
	if errorValue := fixture.database.QueryRow(`SELECT count(*) FROM memory_episode WHERE episode_id = $1`, newEpisode.EpisodeID).Scan(&storedEpisodeCount); errorValue != nil || storedEpisodeCount != 0 {
		t.Fatalf("expected the episode insert to roll back, got %d (%v)", storedEpisodeCount, errorValue)
	}
	var storedFactCount int
	if errorValue := fixture.database.QueryRow(`SELECT count(*) FROM memory_fact WHERE fact_id = $1`, newFact.FactID).Scan(&storedFactCount); errorValue != nil || storedFactCount != 0 {
		t.Fatalf("expected the fact insert to roll back, got %d (%v)", storedFactCount, errorValue)
	}
	reader := bluememo.NewReader("alice", nil, nil, 1, nil)
	if _, errorValue := fixture.facts.ForgetFacts(ctx, reader, []string{seedFact.FactID}, "forget rollback", fixture.now); errorValue == nil {
		t.Fatal("expected profile queue failure to fail the forget transaction")
	}
	facts, errorValue := fixture.facts.ListFactsByID(ctx, reader, []string{seedFact.FactID}, fixture.now)
	if errorValue != nil || len(facts) != 1 || !facts[0].ForgottenAt.IsZero() {
		t.Fatalf("expected the fact to remain live after rollback, got %+v (%v)", facts, errorValue)
	}
}

func TestProfileFactsRespectPrivateOwnershipAndSharedAccess(t *testing.T) {
	fixture := openFixture(t)
	episode := fixture.episode("alice")
	privateOtherOwner := fixture.privateFact(episode.EpisodeID, "bob", "private information about target")
	privateOtherOwner.SubjectPersonID = "target"
	privateReaderOwner := fixture.privateFact(episode.EpisodeID, "alice", "reader-owned information about target")
	privateReaderOwner.SubjectPersonID = "target"
	sharedAllowed := fixture.circleFact(episode.EpisodeID, "permitted shared information about target", "team")
	sharedAllowed.SubjectPersonID = "target"
	sharedAllowed.SecurityLevelRank = 1
	sharedAllowed.RequiredClasses = []string{"legal"}
	sharedRankDenied := fixture.circleFact(episode.EpisodeID, "high-rank information about target", "team")
	sharedRankDenied.SubjectPersonID = "target"
	sharedRankDenied.SecurityLevelRank = 2
	sharedClassDenied := fixture.circleFact(episode.EpisodeID, "restricted-class information about target", "team")
	sharedClassDenied.SubjectPersonID = "target"
	sharedClassDenied.RequiredClasses = []string{"finance"}
	fixture.save(t, episode,
		bluememo.FactWrite{Fact: privateOtherOwner},
		bluememo.FactWrite{Fact: privateReaderOwner},
		bluememo.FactWrite{Fact: sharedAllowed},
		bluememo.FactWrite{Fact: sharedRankDenied},
		bluememo.FactWrite{Fact: sharedClassDenied},
	)
	reader := bluememo.NewReader("alice", []string{"team"}, nil, 1, []string{"legal"})
	facts, errorValue := fixture.facts.ListLiveFactsAboutPerson(context.Background(), reader, "target", fixture.now)
	if errorValue != nil {
		t.Fatalf("expected profile fact query to succeed: %v", errorValue)
	}
	visibleFactIDs := make(map[string]bool, len(facts))
	for _, fact := range facts {
		visibleFactIDs[fact.FactID] = true
	}
	for _, expected := range []bluememo.Fact{privateReaderOwner, sharedAllowed} {
		if !visibleFactIDs[expected.FactID] {
			t.Errorf("expected readable profile fact %q", expected.Content)
		}
	}
	for _, hidden := range []bluememo.Fact{privateOtherOwner, sharedRankDenied, sharedClassDenied} {
		if visibleFactIDs[hidden.FactID] {
			t.Errorf("expected unreadable profile fact %q to be excluded", hidden.Content)
		}
	}
}
