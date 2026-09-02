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
  job_id, kind, subject_id, attempts, run_after, locked_until, COALESCE(last_error, ''), created_at, finished_at`

func (repository JobRepository) EnqueueJob(ctx context.Context, kind string, subjectID string, runAfter time.Time) (bluememo.Job, bool, error) {
	row := repository.database.QueryRowContext(ctx, `
INSERT INTO memory_job (job_id, kind, subject_id, run_after)
VALUES ($1, $2, $3, $4)
ON CONFLICT (kind, subject_id) WHERE finished_at IS NULL DO NOTHING
RETURNING`+jobColumns, bluememo.NewIdentifier(), kind, subjectID, runAfter.UTC())
	job, errorValue := scanJob(row)
	if errorValue == nil {
		return job, true, nil
	}
	if !errors.Is(errorValue, sql.ErrNoRows) {
		return bluememo.Job{}, false, errorValue
	}
	pendingJob, errorValue := scanJob(repository.database.QueryRowContext(ctx, `
SELECT`+jobColumns+`
FROM memory_job WHERE kind = $1 AND subject_id = $2 AND finished_at IS NULL`, kind, subjectID))
	return pendingJob, false, errorValue
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
UPDATE memory_job job SET locked_until = $3, attempts = job.attempts + 1
FROM due WHERE job.job_id = due.job_id
RETURNING job.job_id, job.kind, job.subject_id, job.attempts, job.run_after, job.locked_until, COALESCE(job.last_error, ''), job.created_at, job.finished_at`,
		nonNilStrings(kinds), referenceTime.UTC(), referenceTime.Add(leaseDuration).UTC(), limit)
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

func (repository JobRepository) FinishJob(ctx context.Context, jobID string, finishedAt time.Time) error {
	_, errorValue := repository.database.ExecContext(ctx, `
UPDATE memory_job SET finished_at = $2, locked_until = NULL, last_error = NULL WHERE job_id = $1`, jobID, finishedAt.UTC())
	return errorValue
}

func (repository JobRepository) RetryJob(ctx context.Context, jobID string, lastError string, runAfter time.Time) error {
	_, errorValue := repository.database.ExecContext(ctx, `
UPDATE memory_job SET run_after = $3, locked_until = NULL, last_error = $2 WHERE job_id = $1`, jobID, lastError, runAfter.UTC())
	return errorValue
}

func (repository JobRepository) AbandonJob(ctx context.Context, jobID string, lastError string, finishedAt time.Time) error {
	_, errorValue := repository.database.ExecContext(ctx, `
UPDATE memory_job SET finished_at = $3, locked_until = NULL, last_error = $2 WHERE job_id = $1`, jobID, lastError, finishedAt.UTC())
	return errorValue
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
	errorValue := row.Scan(&job.JobID, &job.Kind, &job.SubjectID, &job.Attempts, &job.RunAfter, &lockedUntil, &job.LastError, &job.CreatedAt, &finishedAt)
	if errorValue != nil {
		return bluememo.Job{}, errorValue
	}
	job.RunAfter = job.RunAfter.UTC()
	job.CreatedAt = job.CreatedAt.UTC()
	job.LockedUntil = timeFromNullable(lockedUntil)
	job.FinishedAt = timeFromNullable(finishedAt)
	return job, nil
}
