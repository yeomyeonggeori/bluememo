package bluememo

import (
	"context"
	"errors"
	"log/slog"
	"sort"
	"time"
)

const (
	JobKindExtract = "extract"
	JobKindProfile = "profile"
	JobKindReembed = "reembed"
	JobKindImport  = "import"
)

const (
	DefaultJobMaxAttempts      = 5
	DefaultJobLeaseDuration    = 5 * time.Minute
	DefaultJobWorkerInterval   = 2 * time.Second
	DefaultJobWorkerClaimLimit = 4
	jobRetryBaseDelay          = time.Minute
	jobRetryMaximumDelay       = 30 * time.Minute
)

type Job struct {
	JobID       string    `json:"jobID"`
	Kind        string    `json:"kind"`
	SubjectID   string    `json:"subjectID"`
	ClaimToken  string    `json:"claimToken,omitempty"`
	Generation  int64     `json:"generation"`
	Attempts    int       `json:"attempts"`
	RunAfter    time.Time `json:"runAfter"`
	LockedUntil time.Time `json:"lockedUntil,omitzero"`
	LastError   string    `json:"lastError,omitempty"`
	CreatedAt   time.Time `json:"createdAt"`
	FinishedAt  time.Time `json:"finishedAt,omitzero"`
}

type TerminalJobError struct {
	Cause error
}

func (errorValue TerminalJobError) Error() string {
	return errorValue.Cause.Error()
}

func (errorValue TerminalJobError) Unwrap() error {
	return errorValue.Cause
}

func JobRetryDelay(attempts int) time.Duration {
	if attempts <= 1 {
		return jobRetryBaseDelay
	}
	delay := jobRetryBaseDelay
	for attempt := 1; attempt < attempts; attempt++ {
		delay *= 2
		if delay >= jobRetryMaximumDelay {
			return jobRetryMaximumDelay
		}
	}
	return delay
}

type JobHandler func(context.Context, Job) error

type JobWorker struct {
	Jobs          JobRepository
	Handlers      map[string]JobHandler
	LeaseDuration time.Duration
	MaxAttempts   int
	ClaimLimit    int
	Logger        *slog.Logger
	Now           func() time.Time
}

func (worker JobWorker) Start(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = DefaultJobWorkerInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	worker.logger().Info("memory.job_worker.started", "intervalSecond", int(interval.Seconds()), "kinds", worker.handledKinds())
	for ctx.Err() == nil {
		if runCount, errorValue := worker.RunOnce(ctx); errorValue != nil {
			worker.logger().Error("memory.job_worker.failed", "error", errorValue.Error())
		} else if runCount > 0 {
			worker.logger().Info("memory.job_worker.completed", "runCount", runCount)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (worker JobWorker) RunOnce(ctx context.Context) (int, error) {
	if worker.Jobs == nil {
		return 0, errors.New("memory job repository is not configured")
	}
	kinds := worker.handledKinds()
	if len(kinds) == 0 {
		return 0, nil
	}
	jobs, errorValue := worker.Jobs.ClaimDueJobs(ctx, kinds, worker.now(), worker.leaseDuration(), worker.claimLimit())
	if errorValue != nil {
		return 0, errorValue
	}
	runCount := 0
	for _, job := range jobs {
		if ctx.Err() != nil {
			return runCount, ctx.Err()
		}
		worker.runJob(ctx, job)
		runCount++
	}
	return runCount, nil
}

func (worker JobWorker) runJob(ctx context.Context, job Job) {
	handlerError := worker.Handlers[job.Kind](ctx, job)
	if handlerError == nil {
		isSettled, errorValue := worker.Jobs.FinishJob(ctx, job, worker.now())
		worker.settle(job, isSettled, errorValue, "finished")
		return
	}
	var terminalError TerminalJobError
	if errors.As(handlerError, &terminalError) || job.Attempts >= worker.maxAttempts() {
		worker.logger().Error("memory.job.abandoned", "jobID", job.JobID, "kind", job.Kind, "subjectID", job.SubjectID, "attempts", job.Attempts, "error", handlerError.Error())
		isSettled, errorValue := worker.Jobs.AbandonJob(ctx, job, handlerError.Error(), worker.now())
		worker.settle(job, isSettled, errorValue, "abandoned")
		return
	}
	runAfter := worker.now().Add(JobRetryDelay(job.Attempts))
	worker.logger().Warn("memory.job.retry_scheduled", "jobID", job.JobID, "kind", job.Kind, "subjectID", job.SubjectID, "attempts", job.Attempts, "runAfter", runAfter, "error", handlerError.Error())
	isSettled, errorValue := worker.Jobs.RetryJob(ctx, job, handlerError.Error(), runAfter)
	worker.settle(job, isSettled, errorValue, "retry")
}

func (worker JobWorker) settle(job Job, isSettled bool, errorValue error, outcome string) {
	if !isSettled && errorValue == nil {
		return
	}
	if errorValue == nil {
		return
	}
	worker.logger().Error("memory.job.settle_failed", "jobID", job.JobID, "kind", job.Kind, "outcome", outcome, "error", errorValue.Error())
}

func (worker JobWorker) handledKinds() []string {
	kinds := make([]string, 0, len(worker.Handlers))
	for kind, handler := range worker.Handlers {
		if handler != nil {
			kinds = append(kinds, kind)
		}
	}
	sort.Strings(kinds)
	return kinds
}

func (worker JobWorker) leaseDuration() time.Duration {
	if worker.LeaseDuration > 0 {
		return worker.LeaseDuration
	}
	return DefaultJobLeaseDuration
}

func (worker JobWorker) maxAttempts() int {
	if worker.MaxAttempts > 0 {
		return worker.MaxAttempts
	}
	return DefaultJobMaxAttempts
}

func (worker JobWorker) claimLimit() int {
	if worker.ClaimLimit > 0 {
		return worker.ClaimLimit
	}
	return DefaultJobWorkerClaimLimit
}

func (worker JobWorker) now() time.Time {
	if worker.Now != nil {
		return worker.Now().UTC()
	}
	return time.Now().UTC()
}

func (worker JobWorker) logger() *slog.Logger {
	if worker.Logger != nil {
		return worker.Logger
	}
	return slog.Default()
}
