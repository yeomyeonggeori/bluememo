package bluememo

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestRankFactsFusesBothRankLists(t *testing.T) {
	referenceTime := time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC)
	both := RankedFact{Fact: Fact{FactID: "both", Kind: FactKindFact, ValidFrom: referenceTime}, VectorRank: 2, LexicalRank: 2}
	vectorOnly := RankedFact{Fact: Fact{FactID: "vector", Kind: FactKindFact, ValidFrom: referenceTime}, VectorRank: 1}
	if ranked := RankFacts([]RankedFact{vectorOnly, both}, referenceTime); ranked[0].Fact.FactID != "both" {
		t.Fatalf("expected the fact present in both lists to rank first, got %s", ranked[0].Fact.FactID)
	}
}

func TestRankFactsDecaysOldEpisodesAndKeepsBetterMatchesAheadOfReinforcedOnes(t *testing.T) {
	referenceTime := time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC)
	betterMatch := RankedFact{Fact: Fact{FactID: "fact", Kind: FactKindFact, ValidFrom: referenceTime}, VectorRank: 1, LexicalRank: 1}
	reinforced := RankedFact{Fact: Fact{FactID: "preference", Kind: FactKindPreference, ReinforcementCount: 9, ValidFrom: referenceTime}, VectorRank: 2, LexicalRank: 2}
	oldEpisode := RankedFact{Fact: Fact{FactID: "episode", Kind: FactKindEpisode, ValidFrom: referenceTime.Add(-180 * 24 * time.Hour)}, VectorRank: 1, LexicalRank: 1}
	ranked := RankFacts([]RankedFact{reinforced, oldEpisode, betterMatch}, referenceTime)
	if ranked[0].Fact.FactID != "fact" || ranked[1].Fact.FactID != "preference" || ranked[2].Fact.FactID != "episode" {
		t.Fatalf("expected fact, preference, episode, got %s, %s, %s", ranked[0].Fact.FactID, ranked[1].Fact.FactID, ranked[2].Fact.FactID)
	}
}

func TestRankFactsBreaksTiesByReinforcementThenRecency(t *testing.T) {
	referenceTime := time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC)
	once := RankedFact{Fact: Fact{FactID: "once", Kind: FactKindPreference, ReinforcementCount: 1, ValidFrom: referenceTime}, LexicalRank: 2}
	often := RankedFact{Fact: Fact{FactID: "often", Kind: FactKindPreference, ReinforcementCount: 4, ValidFrom: referenceTime.Add(-time.Hour)}, LexicalRank: 2}
	older := RankedFact{Fact: Fact{FactID: "older", Kind: FactKindFact, ValidFrom: referenceTime.Add(-time.Hour)}, LexicalRank: 3}
	newer := RankedFact{Fact: Fact{FactID: "newer", Kind: FactKindFact, ValidFrom: referenceTime}, LexicalRank: 3}
	ranked := RankFacts([]RankedFact{once, older, often, newer}, referenceTime)
	if ranked[0].Fact.FactID != "often" || ranked[2].Fact.FactID != "newer" {
		t.Fatalf("expected reinforcement then recency to break ties, got %s, %s, %s, %s", ranked[0].Fact.FactID, ranked[1].Fact.FactID, ranked[2].Fact.FactID, ranked[3].Fact.FactID)
	}
}

func TestJobRetryDelayDoublesAndCaps(t *testing.T) {
	for attempts, expectedDelay := range map[int]time.Duration{1: time.Minute, 2: 2 * time.Minute, 3: 4 * time.Minute, 5: 16 * time.Minute, 12: 30 * time.Minute} {
		if delay := JobRetryDelay(attempts); delay != expectedDelay {
			t.Fatalf("expected attempt %d to wait %s, got %s", attempts, expectedDelay, delay)
		}
	}
}

func TestJobWorkerClaimsOnlyHandledKindsRetriesWithBackoffAndAbandons(t *testing.T) {
	now := time.Date(2026, 9, 2, 9, 0, 0, 0, time.UTC)
	repository := NewInMemoryRepository()
	extractJob, _, _ := repository.EnqueueJob(context.Background(), JobKindExtract, "run-1", now)
	repository.EnqueueJob(context.Background(), JobKindProfile, "person-1", now)
	attempts := 0
	worker := JobWorker{Jobs: repository, MaxAttempts: 2, Now: func() time.Time { return now }, Handlers: map[string]JobHandler{
		JobKindExtract: func(context.Context, Job) error { attempts++; return errors.New("model unavailable") },
	}}
	if runCount, errorValue := worker.RunOnce(context.Background()); errorValue != nil || runCount != 1 {
		t.Fatalf("expected one extract job to run, got %d (%v)", runCount, errorValue)
	}
	retried, _ := repository.FindJob(extractJob.JobID)
	if retried.RunAfter != now.Add(JobRetryDelay(1)) || !retried.FinishedAt.IsZero() {
		t.Fatalf("expected a retry, got %+v", retried)
	}
	now = retried.RunAfter
	if _, errorValue := worker.RunOnce(context.Background()); errorValue != nil {
		t.Fatal(errorValue)
	}
	abandoned, _ := repository.FindJob(extractJob.JobID)
	if abandoned.FinishedAt.IsZero() || abandoned.LastError != "model unavailable" || attempts != 2 {
		t.Fatalf("expected the second attempt to abandon the job, got %+v after %d attempts", abandoned, attempts)
	}
	terminal := JobWorker{Jobs: repository, Handlers: map[string]JobHandler{
		JobKindProfile: func(context.Context, Job) error { return TerminalJobError{Cause: errors.New("unknown person")} },
	}}
	if _, errorValue := terminal.RunOnce(context.Background()); errorValue != nil {
		t.Fatal(errorValue)
	}
	if pending, _ := repository.ClaimDueJobs(context.Background(), []string{JobKindProfile}, now.Add(time.Hour), time.Minute, 10); len(pending) != 0 {
		t.Fatalf("expected a terminal error to abandon at once, got %+v", pending)
	}
}
