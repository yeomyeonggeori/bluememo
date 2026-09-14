package postgres

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/yeomyeonggeori/bluememo"
)

type JobRepository struct {
	database *sql.DB
}

func NewJobRepository(database *sql.DB) JobRepository {
	return JobRepository{database: database}
}

const jobColumns = `
  job_id, kind, subject_id, claim_token, generation, attempts, run_after, locked_until, COALESCE(last_error, ''), created_at, finished_at`

func (repository JobRepository) EnqueueJob(ctx context.Context, kind string, subjectID string, runAfter time.Time) (bluememo.Job, bool, error) {
	return enqueueJob(ctx, repository.database, kind, subjectID, runAfter)
}

type jobQueryer interface {
	QueryRowContext(ctx context.Context, query string, arguments ...any) *sql.Row
}

func enqueueJob(ctx context.Context, queryer jobQueryer, kind string, subjectID string, runAfter time.Time) (bluememo.Job, bool, error) {
	row := queryer.QueryRowContext(ctx, `
INSERT INTO memory_job (job_id, kind, subject_id, run_after, generation)
VALUES ($1, $2, $3, $4, 1)
ON CONFLICT (kind, subject_id) WHERE finished_at IS NULL DO UPDATE
SET run_after = LEAST(memory_job.run_after, EXCLUDED.run_after),
    generation = memory_job.generation + 1
RETURNING`+jobColumns, bluememo.NewIdentifier(), kind, subjectID, runAfter.UTC())
	job, errorValue := scanJob(row)
	if errorValue != nil {
		return bluememo.Job{}, false, errorValue
	}
	return job, job.Generation == 1, nil
}

func (repository JobRepository) ClaimDueJobs(ctx context.Context, kinds []string, referenceTime time.Time, leaseDuration time.Duration, limit int) ([]bluememo.Job, error) {
	if len(kinds) == 0 || limit <= 0 {
		return []bluememo.Job{}, nil
	}
	rows, errorValue := repository.database.QueryContext(ctx, `
WITH due AS (
  SELECT job_id FROM memory_job
  WHERE finished_at IS NULL
    AND kind = ANY($1::text[])
    AND run_after <= $2
    AND (locked_until IS NULL OR locked_until <= $2)
  ORDER BY run_after ASC
  LIMIT $4
  FOR UPDATE SKIP LOCKED
)
UPDATE memory_job job SET locked_until = $3, attempts = job.attempts + 1,
    claim_token = $5 || ':' || job.job_id
FROM due WHERE job.job_id = due.job_id
RETURNING job.job_id, job.kind, job.subject_id, job.claim_token, job.generation, job.attempts, job.run_after, job.locked_until, COALESCE(job.last_error, ''), job.created_at, job.finished_at`,
		nonNilStrings(kinds), referenceTime.UTC(), referenceTime.Add(leaseDuration).UTC(), limit, bluememo.NewIdentifier())
	if errorValue != nil {
		return nil, errorValue
	}
	defer rows.Close()
	jobs := []bluememo.Job{}
	for rows.Next() {
		job, errorValue := scanJob(rows)
		if errorValue != nil {
			return nil, errorValue
		}
		jobs = append(jobs, job)
	}
	return jobs, rows.Err()
}

func (repository JobRepository) FinishJob(ctx context.Context, job bluememo.Job, finishedAt time.Time) (bool, error) {
	return settleJob(ctx, repository.database, `
UPDATE memory_job
SET finished_at = CASE WHEN generation = $3 THEN $2::timestamptz ELSE NULL END,
    locked_until = NULL,
    claim_token = '',
    attempts = CASE WHEN generation = $3 THEN attempts ELSE 0 END,
    last_error = NULL
WHERE job_id = $1 AND claim_token = $4 AND claim_token <> '' AND finished_at IS NULL
RETURNING generation = $3`, job.JobID, finishedAt.UTC(), job.Generation, job.ClaimToken)
}

func (repository JobRepository) RetryJob(ctx context.Context, job bluememo.Job, lastError string, runAfter time.Time) (bool, error) {
	return settleJob(ctx, repository.database, `
UPDATE memory_job
SET run_after = CASE WHEN generation = $4 THEN $3::timestamptz ELSE run_after END,
    locked_until = NULL,
    claim_token = '',
    attempts = CASE WHEN generation = $4 THEN attempts ELSE 0 END,
    last_error = CASE WHEN generation = $4 THEN $2 ELSE NULL END
WHERE job_id = $1 AND claim_token = $5 AND claim_token <> '' AND finished_at IS NULL
RETURNING generation = $4`, job.JobID, lastError, runAfter.UTC(), job.Generation, job.ClaimToken)
}

func (repository JobRepository) AbandonJob(ctx context.Context, job bluememo.Job, lastError string, finishedAt time.Time) (bool, error) {
	return settleJob(ctx, repository.database, `
UPDATE memory_job
SET finished_at = CASE WHEN generation = $4 THEN $3::timestamptz ELSE NULL END,
    locked_until = NULL,
    claim_token = '',
    attempts = CASE WHEN generation = $4 THEN attempts ELSE 0 END,
    last_error = CASE WHEN generation = $4 THEN $2 ELSE NULL END
WHERE job_id = $1 AND claim_token = $5 AND claim_token <> '' AND finished_at IS NULL
RETURNING generation = $4`, job.JobID, lastError, finishedAt.UTC(), job.Generation, job.ClaimToken)
}

func settleJob(ctx context.Context, queryer jobQueryer, query string, arguments ...any) (bool, error) {
	var isGenerationCurrent bool
	errorValue := queryer.QueryRowContext(ctx, query, arguments...).Scan(&isGenerationCurrent)
	if errors.Is(errorValue, sql.ErrNoRows) {
		return false, nil
	}
	return isGenerationCurrent, errorValue
}

func (repository JobRepository) FindJob(ctx context.Context, jobID string) (bluememo.Job, bool, error) {
	job, errorValue := scanJob(repository.database.QueryRowContext(ctx, `
SELECT`+jobColumns+`
FROM memory_job WHERE job_id = $1`, jobID))
	if errors.Is(errorValue, sql.ErrNoRows) {
		return bluememo.Job{}, false, nil
	}
	return job, errorValue == nil, errorValue
}

type rowScanner interface {
	Scan(targets ...any) error
}

func scanJob(row rowScanner) (bluememo.Job, error) {
	var job bluememo.Job
	var lockedUntil, finishedAt sql.NullTime
	errorValue := row.Scan(&job.JobID, &job.Kind, &job.SubjectID, &job.ClaimToken, &job.Generation, &job.Attempts, &job.RunAfter, &lockedUntil, &job.LastError, &job.CreatedAt, &finishedAt)
	if errorValue != nil {
		return bluememo.Job{}, errorValue
	}
	setJobTimes(&job, lockedUntil, finishedAt)
	return job, nil
}

func setJobTimes(job *bluememo.Job, lockedUntil sql.NullTime, finishedAt sql.NullTime) {
	job.RunAfter = job.RunAfter.UTC()
	job.CreatedAt = job.CreatedAt.UTC()
	job.LockedUntil = timeFromNullable(lockedUntil)
	job.FinishedAt = timeFromNullable(finishedAt)
}
