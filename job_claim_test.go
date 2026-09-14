package bluememo

import (
	"context"
	"testing"
	"time"
)

func TestJobGenerationFencesOldClaims(t *testing.T) {
	repository := NewInMemoryRepository()
	contextValue := context.Background()
	now := time.Date(2026, 9, 2, 9, 0, 0, 0, time.UTC)
	firstRunAfter := now.Add(time.Hour)
	firstJob, isCreated, errorValue := repository.EnqueueJob(contextValue, JobKindExtract, "run-1", firstRunAfter)
	if errorValue != nil || !isCreated || firstJob.Generation != 1 {
		t.Fatalf("expected generation one to be enqueued, got %+v created=%v (%v)", firstJob, isCreated, errorValue)
	}
	if isSettled, errorValue := repository.FinishJob(contextValue, firstJob, now); errorValue != nil || isSettled {
		t.Fatalf("expected an unclaimed job not to finish, got settled=%v (%v)", isSettled, errorValue)
	}
	if isSettled, errorValue := repository.RetryJob(contextValue, firstJob, "unclaimed retry", now); errorValue != nil || isSettled {
		t.Fatalf("expected an unclaimed job not to retry, got settled=%v (%v)", isSettled, errorValue)
	}
	if isSettled, errorValue := repository.AbandonJob(contextValue, firstJob, "unclaimed abandon", now); errorValue != nil || isSettled {
		t.Fatalf("expected an unclaimed job not to abandon, got settled=%v (%v)", isSettled, errorValue)
	}
	firstClaim, errorValue := repository.ClaimDueJobs(contextValue, []string{JobKindExtract}, now.Add(2*time.Hour), time.Hour, 1)
	if errorValue != nil || len(firstClaim) != 1 || firstClaim[0].ClaimToken == "" {
		t.Fatalf("expected the first claim to have a token, got %+v (%v)", firstClaim, errorValue)
	}
	secondRunAfter := now.Add(30 * time.Minute)
	queuedJob, isCreated, errorValue := repository.EnqueueJob(contextValue, JobKindExtract, "run-1", secondRunAfter)
	if errorValue != nil || isCreated || queuedJob.Generation != 2 || !queuedJob.RunAfter.Equal(secondRunAfter) {
		t.Fatalf("expected re-enqueue to advance generation and keep the earlier run time, got %+v created=%v (%v)", queuedJob, isCreated, errorValue)
	}
	isSettled, errorValue := repository.FinishJob(contextValue, firstClaim[0], now.Add(2*time.Hour))
	if errorValue != nil || isSettled {
		t.Fatalf("expected the first generation to be fenced, got settled=%v (%v)", isSettled, errorValue)
	}
	resetJob, isFound := repository.FindJob(firstJob.JobID)
	if !isFound || !resetJob.FinishedAt.IsZero() || !resetJob.LockedUntil.IsZero() || resetJob.Attempts != 0 || resetJob.Generation != 2 {
		t.Fatalf("expected the newer generation to remain pending and unlocked, got %+v", resetJob)
	}
	secondClaim, errorValue := repository.ClaimDueJobs(contextValue, []string{JobKindExtract}, now.Add(2*time.Hour), time.Hour, 1)
	if errorValue != nil || len(secondClaim) != 1 || secondClaim[0].Generation != 2 || secondClaim[0].ClaimToken == firstClaim[0].ClaimToken {
		t.Fatalf("expected the newer generation to receive a fresh claim, got %+v (%v)", secondClaim, errorValue)
	}
	if isSettled, errorValue := repository.RetryJob(contextValue, firstClaim[0], "stale retry", now.Add(3*time.Hour)); errorValue != nil || isSettled {
		t.Fatalf("expected an old retry to lose its claim, got settled=%v (%v)", isSettled, errorValue)
	}
	if isSettled, errorValue := repository.AbandonJob(contextValue, firstClaim[0], "stale abandon", now.Add(3*time.Hour)); errorValue != nil || isSettled {
		t.Fatalf("expected an old abandonment to lose its claim, got settled=%v (%v)", isSettled, errorValue)
	}
	currentJob, isFound := repository.FindJob(firstJob.JobID)
	if !isFound || currentJob.ClaimToken != secondClaim[0].ClaimToken || currentJob.Attempts != 1 || !currentJob.FinishedAt.IsZero() {
		t.Fatalf("expected stale settlements to preserve the newer claim, got %+v", currentJob)
	}
	if isSettled, errorValue := repository.FinishJob(contextValue, secondClaim[0], now.Add(2*time.Hour)); errorValue != nil || !isSettled {
		t.Fatalf("expected the current claim to finish, got settled=%v (%v)", isSettled, errorValue)
	}
}
