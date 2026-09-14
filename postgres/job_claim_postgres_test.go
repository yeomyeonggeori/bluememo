package postgres_test

import (
	"context"
	"testing"
	"time"

	"github.com/yeomyeonggeori/bluememo"
	"github.com/yeomyeonggeori/bluememo/postgres"
)

func TestPostgresJobClaimsFenceRequeuedAndExpiredWorkers(t *testing.T) {
	database, _ := openMigrationDatabase(t)
	ctx := context.Background()
	if errorValue := postgres.ApplyMigrations(ctx, database); errorValue != nil {
		t.Fatalf("expected migrations to apply: %v", errorValue)
	}
	repository := postgres.NewJobRepository(database)
	now := time.Date(2026, 9, 2, 9, 0, 0, 0, time.UTC)
	firstRunAfter := now.Add(time.Hour)
	firstJob, isCreated, errorValue := repository.EnqueueJob(ctx, bluememo.JobKindExtract, "requeued-run", firstRunAfter)
	if errorValue != nil || !isCreated || firstJob.Generation != 1 {
		t.Fatalf("expected generation one to be enqueued, got %+v created=%v (%v)", firstJob, isCreated, errorValue)
	}
	if isSettled, errorValue := repository.FinishJob(ctx, firstJob, now); errorValue != nil || isSettled {
		t.Fatalf("expected an unclaimed job not to finish, got settled=%v (%v)", isSettled, errorValue)
	}
	if isSettled, errorValue := repository.RetryJob(ctx, firstJob, "unclaimed retry", now); errorValue != nil || isSettled {
		t.Fatalf("expected an unclaimed job not to retry, got settled=%v (%v)", isSettled, errorValue)
	}
	if isSettled, errorValue := repository.AbandonJob(ctx, firstJob, "unclaimed abandon", now); errorValue != nil || isSettled {
		t.Fatalf("expected an unclaimed job not to abandon, got settled=%v (%v)", isSettled, errorValue)
	}
	firstClaim, errorValue := repository.ClaimDueJobs(ctx, []string{bluememo.JobKindExtract}, now.Add(2*time.Hour), time.Hour, 1)
	if errorValue != nil || len(firstClaim) != 1 || firstClaim[0].ClaimToken == "" {
		t.Fatalf("expected the first claim to have a token, got %+v (%v)", firstClaim, errorValue)
	}
	secondRunAfter := now.Add(30 * time.Minute)
	queuedJob, isCreated, errorValue := repository.EnqueueJob(ctx, bluememo.JobKindExtract, "requeued-run", secondRunAfter)
	if errorValue != nil || isCreated || queuedJob.Generation != 2 || !queuedJob.RunAfter.Equal(secondRunAfter) {
		t.Fatalf("expected re-enqueue to advance generation and keep the earlier run time, got %+v created=%v (%v)", queuedJob, isCreated, errorValue)
	}
	isSettled, errorValue := repository.FinishJob(ctx, firstClaim[0], now.Add(2*time.Hour))
	if errorValue != nil || isSettled {
		t.Fatalf("expected the first generation to be fenced, got settled=%v (%v)", isSettled, errorValue)
	}
	resetJob, isFound, errorValue := repository.FindJob(ctx, firstJob.JobID)
	if errorValue != nil || !isFound || !resetJob.FinishedAt.IsZero() || !resetJob.LockedUntil.IsZero() || resetJob.Attempts != 0 || resetJob.Generation != 2 {
		t.Fatalf("expected the newer generation to remain pending and unlocked, got %+v found=%v (%v)", resetJob, isFound, errorValue)
	}
	secondClaim, errorValue := repository.ClaimDueJobs(ctx, []string{bluememo.JobKindExtract}, now.Add(2*time.Hour), time.Hour, 1)
	if errorValue != nil || len(secondClaim) != 1 || secondClaim[0].Generation != 2 || secondClaim[0].ClaimToken == firstClaim[0].ClaimToken {
		t.Fatalf("expected the newer generation to receive a fresh claim, got %+v (%v)", secondClaim, errorValue)
	}
	if isSettled, errorValue := repository.RetryJob(ctx, firstClaim[0], "stale retry", now.Add(3*time.Hour)); errorValue != nil || isSettled {
		t.Fatalf("expected an old retry to lose its claim, got settled=%v (%v)", isSettled, errorValue)
	}
	if isSettled, errorValue := repository.AbandonJob(ctx, firstClaim[0], "stale abandon", now.Add(3*time.Hour)); errorValue != nil || isSettled {
		t.Fatalf("expected an old abandonment to lose its claim, got settled=%v (%v)", isSettled, errorValue)
	}
	currentJob, isFound, errorValue := repository.FindJob(ctx, firstJob.JobID)
	if errorValue != nil || !isFound || currentJob.ClaimToken != secondClaim[0].ClaimToken || currentJob.Attempts != 1 || !currentJob.FinishedAt.IsZero() {
		t.Fatalf("expected stale settlements to preserve the newer claim, got %+v found=%v (%v)", currentJob, isFound, errorValue)
	}
	if isSettled, errorValue := repository.FinishJob(ctx, secondClaim[0], now.Add(2*time.Hour)); errorValue != nil || !isSettled {
		t.Fatalf("expected the current claim to finish, got settled=%v (%v)", isSettled, errorValue)
	}
	assertExpiredJobClaimIsFenced(t, ctx, repository, now)
}

func assertExpiredJobClaimIsFenced(t *testing.T, ctx context.Context, repository postgres.JobRepository, now time.Time) {
	t.Helper()
	job, _, errorValue := repository.EnqueueJob(ctx, bluememo.JobKindExtract, "expired-run", now)
	if errorValue != nil {
		t.Fatal(errorValue)
	}
	firstClaim, errorValue := repository.ClaimDueJobs(ctx, []string{bluememo.JobKindExtract}, now, time.Minute, 1)
	if errorValue != nil || len(firstClaim) != 1 || firstClaim[0].JobID != job.JobID {
		t.Fatalf("expected the first expired-run claim, got %+v (%v)", firstClaim, errorValue)
	}
	secondClaim, errorValue := repository.ClaimDueJobs(ctx, []string{bluememo.JobKindExtract}, now.Add(2*time.Minute), time.Minute, 1)
	if errorValue != nil || len(secondClaim) != 1 || secondClaim[0].JobID != job.JobID || secondClaim[0].ClaimToken == firstClaim[0].ClaimToken {
		t.Fatalf("expected a fresh claim after lease expiry, got %+v (%v)", secondClaim, errorValue)
	}
	if isSettled, errorValue := repository.FinishJob(ctx, firstClaim[0], now.Add(2*time.Minute)); errorValue != nil || isSettled {
		t.Fatalf("expected the expired worker to lose its claim, got settled=%v (%v)", isSettled, errorValue)
	}
	currentJob, isFound, errorValue := repository.FindJob(ctx, job.JobID)
	if errorValue != nil || !isFound || currentJob.ClaimToken != secondClaim[0].ClaimToken || currentJob.Attempts != 2 || !currentJob.LockedUntil.Equal(secondClaim[0].LockedUntil) {
		t.Fatalf("expected the reclaimed lease to remain intact, got %+v found=%v (%v)", currentJob, isFound, errorValue)
	}
}
