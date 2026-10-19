package swig

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/glamboyosa/swig/drivers"
	"github.com/glamboyosa/swig/pkg"
	"github.com/glamboyosa/swig/workers"
)

type QueueTypes string

const (
	Default  QueueTypes = "default"
	Priority QueueTypes = "priority"

	leaderLockID int64 = 1234567 // Arbitrary number for advisory lock
	leaderKey          = "queue_leader"
	leaderTTL          = 30 * time.Second
)

// Defaults for SwigOptions fields left at their zero value.
const (
	defaultJobLease       = time.Minute
	defaultPollInterval   = 5 * time.Second
	defaultLeaderInterval = 5 * time.Second
	defaultRetryBackoff   = time.Second
)

// minimum number of workers to start
const minWorkers = 3

// Default timeout for graceful shutdown
const defaultShutdownTimeout = 30 * time.Second

type SwigQueueConfig struct {
	QueueType  QueueTypes
	MaxWorkers int
}

// SwigOptions tunes Swig's timing. Zero values use the defaults.
type SwigOptions struct {
	// JobLease is how long a claimed job may go without a heartbeat before the
	// leader assumes its worker died and requeues it. Running jobs renew their
	// lease every JobLease/3. Default: 1 minute.
	JobLease time.Duration
	// PollInterval bounds how long an idle worker waits for a notification
	// before checking for due jobs again. This picks up scheduled and retried
	// jobs, which never send a notification. Default: 5 seconds.
	PollInterval time.Duration
	// LeaderInterval is how often an instance tries to become leader and how
	// often the leader requeues jobs with expired leases. Default: 5 seconds.
	LeaderInterval time.Duration
	// RetryBackoff is the base retry delay. After failed attempt n, the job
	// becomes due again after RetryBackoff * 2^n. Default: 1 second.
	RetryBackoff time.Duration
}

// withDefaults returns a copy of o with zero values replaced by defaults.
func (o SwigOptions) withDefaults() SwigOptions {
	if o.JobLease <= 0 {
		o.JobLease = defaultJobLease
	}
	if o.PollInterval <= 0 {
		o.PollInterval = defaultPollInterval
	}
	if o.LeaderInterval <= 0 {
		o.LeaderInterval = defaultLeaderInterval
	}
	if o.RetryBackoff <= 0 {
		o.RetryBackoff = defaultRetryBackoff
	}
	return o
}

type Swig struct {
	swigQueueConfig []SwigQueueConfig
	driver          drivers.Driver
	Workers         workers.WorkerRegistry
	activeWorkers   sync.WaitGroup // Track active workers
	leaderMu        sync.Mutex
	stopOnce        sync.Once     // Make shutdown safe to call once or many times
	shutdown        chan struct{} // Signal for graceful shutdown
	workerCancel    context.CancelFunc
	leaderLock      drivers.AdvisoryLock
	leaderID        string // Current leader ID if we're the leader
	workerID        string // Unique ID for this worker instance
	options         SwigOptions
}

// NewSwig creates a new job queue instance with the specified database driver,
// queue configurations, and worker registry. Each queue config defines a queue type (Default/Priority)
// and its worker pool size. The worker registry must contain all worker types that will be processed.
// An optional SwigOptions value tunes job leases, polling, and retry backoff.
//
// Example:
//
//	driver := postgres.NewDriver(...)
//
//	// Register your workers
//	workers := NewWorkerRegistry()
//	workers.Register(&EmailWorker{})
//
//	// Configure queues
//	configs := []SwigQueueConfig{
//	    {QueueType: Default, MaxWorkers: 5},
//	}
//
//	swig := NewSwig(driver, configs, workers)
func NewSwig(driver drivers.Driver, swigQueueConfig []SwigQueueConfig, workers workers.WorkerRegistry, opts ...SwigOptions) *Swig {
	options := SwigOptions{}
	if len(opts) > 0 {
		options = opts[0]
	}
	return &Swig{
		options:         options.withDefaults(),
		driver:          driver,
		swigQueueConfig: swigQueueConfig,
		Workers:         workers,
		shutdown:        make(chan struct{}),
		workerID:        pkg.GenerateWorkerID(),
	}
}

// tryBecomeLeader attempts to acquire leadership using advisory locks
func (s *Swig) tryBecomeLeader(ctx context.Context) (drivers.AdvisoryLock, bool, error) {
	lock, acquired, err := s.driver.TryAdvisoryLock(ctx, leaderLockID)
	if err != nil {
		return nil, false, err
	}
	if !acquired {
		return nil, false, nil
	}

	// If we got the lock, update the leader table
	leaderID := pkg.GenerateWorkerID()
	err = lock.Exec(ctx, `
		INSERT INTO swig_leader (id, leader_id, expires_at)
		VALUES ($1, $2, NOW() + $3::interval)
		ON CONFLICT (id) DO UPDATE
		SET leader_id = $2,
			expires_at = NOW() + $3::interval
	`, leaderKey, leaderID, leaderTTL.String())

	if err != nil {
		if closeErr := lock.Close(ctx); closeErr != nil {
			log.Printf("Failed to release leader lock after setup error: %v", closeErr)
		}
		return nil, false, fmt.Errorf("failed to update leader record: %w", err)
	}

	s.leaderMu.Lock()
	s.leaderID = leaderID
	s.leaderLock = lock
	s.leaderMu.Unlock()

	return lock, true, nil
}

func (s *Swig) startLeaderElection(ctx context.Context) {
	ticker := time.NewTicker(s.options.LeaderInterval)
	defer ticker.Stop()

	for {
		lock, acquired, err := s.tryBecomeLeader(ctx)
		if err != nil {
			log.Printf("Failed to become leader: %v", err)
		}
		if acquired {
			log.Printf("Acquired Swig leadership")
			if err := s.performLeaderDuties(ctx, lock); err != nil {
				log.Printf("Leader duties stopped: %v", err)
			}
		}

		select {
		case <-ctx.Done():
			return
		case <-s.shutdown:
			return
		case <-ticker.C:
		}
	}
}

func (s *Swig) releaseLeader(ctx context.Context, lock drivers.AdvisoryLock) {
	s.leaderMu.Lock()
	if lock == nil {
		lock = s.leaderLock
	}
	if lock == nil || lock != s.leaderLock {
		s.leaderMu.Unlock()
		return
	}

	leaderID := s.leaderID
	s.leaderID = ""
	s.leaderLock = nil
	s.leaderMu.Unlock()

	if leaderID != "" {
		unlockSQL := `
			DELETE FROM swig_leader
			WHERE leader_id = $1
		`
		if err := s.driver.Exec(ctx, unlockSQL, leaderID); err != nil {
			log.Printf("Failed to delete leader record: %v", err)
		}
	}

	if err := lock.Close(ctx); err != nil {
		log.Printf("Failed to release advisory lock: %v", err)
	}
}

// performLeaderDuties handles leader responsibilities like requeueing jobs
// whose workers died.
func (s *Swig) performLeaderDuties(ctx context.Context, lock drivers.AdvisoryLock) error {
	ticker := time.NewTicker(s.options.LeaderInterval)
	defer ticker.Stop()
	defer func() {
		releaseCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		s.releaseLeader(releaseCtx, lock)
	}()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-s.shutdown:
			return nil
		case <-ticker.C:
			if err := s.requeueExpiredJobs(ctx, lock); err != nil {
				return err
			}
		}
	}
}

// requeueExpiredJobs recovers jobs stuck in 'processing' because their worker
// stopped renewing the lease, for example after a crash or SIGKILL. The claim
// already counted the attempt, so jobs with attempts left become due again
// immediately and exhausted jobs are marked failed.
func (s *Swig) requeueExpiredJobs(ctx context.Context, tx drivers.Transaction) error {
	requeueSQL := `
		UPDATE swig_jobs
		SET status = CASE
				WHEN attempts >= max_attempts THEN 'failed'
				ELSE 'pending'
			END,
			scheduled_for = NOW(),
			last_error = 'job lease expired',
			last_error_at = NOW(),
			instance_id = NULL,
			worker_id = NULL,
			locked_at = NULL
		WHERE status = 'processing'
			AND locked_at < NOW() - ($1::float8 * interval '1 millisecond')
		RETURNING id`

	rows, err := tx.Query(ctx, requeueSQL, s.options.JobLease.Milliseconds())
	if err != nil {
		// Don't report context cancellation as an error - this is normal during shutdown
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil
		}
		return fmt.Errorf("failed to requeue expired jobs: %w", err)
	}
	defer rows.Close()

	requeued := 0
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return fmt.Errorf("failed to scan job ID: %w", err)
		}
		requeued++
	}

	if requeued > 0 {
		log.Printf("Recovered %d jobs with expired leases", requeued)
	}

	return nil
}

// Start initializes the Swig queue and creates the necessary tables
func (s *Swig) Start(ctx context.Context) {
	createTableSQL := `
	-- This string runs as one implicit transaction, so the transaction-scoped
	-- advisory lock serialises concurrent Starts across all of the DDL below.
	SELECT pg_advisory_xact_lock(hashtext('swig_schema'));

	CREATE TABLE IF NOT EXISTS swig_jobs (
		id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
		kind VARCHAR NOT NULL,
		queue VARCHAR NOT NULL,
		payload JSONB NOT NULL,
		status VARCHAR NOT NULL DEFAULT 'pending',
		priority INTEGER NOT NULL DEFAULT 0,
		attempts INTEGER NOT NULL DEFAULT 0,
		max_attempts INTEGER NOT NULL DEFAULT 3,
		created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		scheduled_for TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		instance_id UUID,           -- ID of the Swig instance
		worker_id UUID,             -- ID of the specific worker
		locked_at TIMESTAMPTZ,
		last_error TEXT,
		last_error_at TIMESTAMPTZ,  -- When the last error occurred
		
		CONSTRAINT valid_status CHECK (status IN (
			'pending', 'processing', 'completed', 'failed', 'scheduled'
		))
	);

	-- Create notification trigger for real-time job processing
	CREATE OR REPLACE FUNCTION notify_job_created()
		RETURNS trigger AS $$
	BEGIN
		PERFORM pg_notify(
			'swig_jobs',
			json_build_object(
				'id', NEW.id,
				'queue', NEW.queue,
				'kind', NEW.kind
			)::text
		);
		RETURN NEW;
	END;
	$$ LANGUAGE plpgsql;

	-- Create indexes and the trigger only when missing, so a restart against an
	-- existing schema takes no table locks.
	DO $$
	BEGIN
		-- Supports the worker claim query
		IF to_regclass('swig_jobs_pending_idx') IS NULL THEN
			CREATE INDEX swig_jobs_pending_idx
				ON swig_jobs (queue, scheduled_for)
				WHERE status = 'pending';
		END IF;
		-- Supports the leader's expired lease scan
		IF to_regclass('swig_jobs_processing_idx') IS NULL THEN
			CREATE INDEX swig_jobs_processing_idx
				ON swig_jobs (locked_at)
				WHERE status = 'processing';
		END IF;
		IF NOT EXISTS (
			SELECT 1 FROM pg_trigger
			WHERE tgname = 'swig_jobs_notify_trigger'
				AND tgrelid = 'swig_jobs'::regclass
		) THEN
			CREATE TRIGGER swig_jobs_notify_trigger
				AFTER INSERT ON swig_jobs
				FOR EACH ROW
				EXECUTE FUNCTION notify_job_created();
		END IF;
	END;
	$$;`

	createLeaderTableSQL := `
	SELECT pg_advisory_xact_lock(hashtext('swig_schema'));

	-- Unlogged for better performance since this is temporary state
	CREATE UNLOGGED TABLE IF NOT EXISTS swig_leader (
		id TEXT PRIMARY KEY,          -- Usually 'queue_leader'
		leader_id UUID NOT NULL,      -- Unique ID of current leader
		expires_at TIMESTAMPTZ NOT NULL,
		acquired_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		
		-- Ensure expires_at is always in the future
		CONSTRAINT leader_expires_future CHECK (expires_at > NOW())
	);`

	if err := s.driver.Exec(ctx, createTableSQL); err != nil {
		log.Printf("Failed to create swig_jobs table: %v", err)
		return
	}
	if err := s.driver.Exec(ctx, createLeaderTableSQL); err != nil {
		log.Printf("Failed to create swig_leader table: %v", err)
		return
	}

	workerCtx, cancel := context.WithCancel(ctx)
	s.workerCancel = cancel

	go s.startLeaderElection(workerCtx)

	// Start worker pools for each queue
	for _, config := range s.swigQueueConfig {
		workers := config.MaxWorkers
		if workers < minWorkers {
			workers = minWorkers
		}

		// Start worker pool for this queue
		for i := 0; i < workers; i++ {
			s.activeWorkers.Add(1)
			go func(qType QueueTypes) {
				defer s.activeWorkers.Done()
				s.startWorker(workerCtx, qType)
			}(config.QueueType)
		}
	}
}

// Wait for active workers to finish and release any leader locks we might be holding
func (s *Swig) Stop(ctx context.Context) error {
	if _, ok := ctx.Deadline(); !ok {
		// No timeout set, use default
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, defaultShutdownTimeout)
		defer cancel()
	}

	// Signal all workers to stop. sync.Once makes Stop safe to call repeatedly.
	s.stopOnce.Do(func() {
		close(s.shutdown)
		if s.workerCancel != nil {
			s.workerCancel()
		}
	})

	// Wait for all workers to finish their current jobs
	done := make(chan struct{})
	go func() {
		s.activeWorkers.Wait()
		close(done)
	}()

	// Wait for workers to finish or timeout
	select {
	case <-done:
		log.Printf("All workers gracefully shutdown")
	case <-ctx.Done():
		log.Printf("Shutdown timed out after %v, some workers may still be running", defaultShutdownTimeout)
		// Even if we timeout, try to cleanup any jobs this instance was processing
		if err := s.cleanupInstanceJobs(ctx); err != nil {
			log.Printf("Failed to cleanup instance jobs: %v", err)
		}
		return fmt.Errorf("shutdown timed out: %w", ctx.Err())
	}

	// Cleanup any jobs this instance was processing
	if err := s.cleanupInstanceJobs(ctx); err != nil {
		log.Printf("Failed to cleanup instance jobs: %v", err)
	}

	s.releaseLeader(ctx, nil)

	// Close database connections cleanly
	if closer, ok := s.driver.(interface{ Close() error }); ok {
		if err := closer.Close(); err != nil {
			log.Printf("Failed to close database connection: %v", err)
		}
	}

	return nil
}

// cleanupInstanceJobs resets any jobs that were being processed by this instance
func (s *Swig) cleanupInstanceJobs(ctx context.Context) error {
	cleanupSQL := `
		UPDATE swig_jobs
		SET status = CASE 
				WHEN attempts >= max_attempts THEN 'failed'
				ELSE 'pending'
			END,
			instance_id = NULL,
			worker_id = NULL,
			locked_at = NULL,
			last_error = CASE 
				WHEN attempts >= max_attempts THEN 'Job failed due to instance shutdown'
				ELSE last_error
			END
		WHERE instance_id = $1
		RETURNING id`

	var jobIDs []string
	rows, err := s.driver.Query(ctx, cleanupSQL, s.workerID)
	if err != nil {
		return fmt.Errorf("failed to cleanup instance jobs: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return fmt.Errorf("failed to scan job ID: %w", err)
		}
		jobIDs = append(jobIDs, id)
	}

	if len(jobIDs) > 0 {
		log.Printf("Cleaned up %d jobs during shutdown", len(jobIDs))
	}

	return nil
}

// JobOptions allows configuring job-specific settings
type JobOptions struct {
	Queue    QueueTypes
	Priority int
	RunAt    time.Time
}

// DefaultJobOptions provides default settings
func DefaultJobOptions() JobOptions {
	return JobOptions{
		Queue:    Default,
		Priority: 1,
		RunAt:    time.Now(),
	}
}

// AddJob enqueues a new job for processing. The workerWithArgs must be a struct that:
//  1. Implements JobName() string to identify the worker type
//  2. Implements Process(context.Context) error for job execution
//  3. Contains JSON-serializable fields that will be passed to Process
//
// Job options can be provided to configure queue, priority, and scheduling.
// If no options are provided, the job will be added to the default queue with normal priority
// and immediate execution.
//
// Example:
//
//	err := swig.AddJob(ctx, &EmailWorker{
//	    To: "user@example.com",
//	    Subject: "Welcome!",
//	}, swig.JobOptions{
//	    Queue: swig.Priority,
//	    RunAt: time.Now().Add(time.Hour),
//	})
func (s *Swig) AddJob(ctx context.Context, workerWithArgs interface{}, opts ...JobOptions) error {
	// Type assert to check if it implements Worker interface
	if _, ok := workerWithArgs.(interface{ JobName() string }); !ok {
		return fmt.Errorf("workerWithArgs must implement JobName() string")
	}
	// Use default options if none provided
	jobOpts := DefaultJobOptions()
	if len(opts) > 0 {
		jobOpts = opts[0]
	}

	// Serialize the worker (which contains the args)
	argsJSON, err := json.Marshal(workerWithArgs)
	if err != nil {
		return fmt.Errorf("failed to serialize job args: %w", err)
	}

	insertSQL := `
		INSERT INTO swig_jobs (
			kind,
			queue,
			payload,
			priority,
			scheduled_for,
			status
		) VALUES ($1, $2, $3, $4, $5, 'pending')
	`

	return s.driver.Exec(
		ctx,
		insertSQL,
		workerWithArgs.(interface{ JobName() string }).JobName(),
		string(jobOpts.Queue),
		argsJSON,
		jobOpts.Priority,
		jobOpts.RunAt,
	)
}

// AddJobWithTx enqueues a new job as part of an existing transaction. The transaction must be
// compatible with the driver being used (pgx.Tx for PgxDriver or *sql.Tx for SQLDriver).
// The caller is responsible for committing or rolling back the transaction.
//
// Example with pgx:
//
//	tx, _ := pool.Begin(ctx)
//	defer tx.Rollback(ctx)
//
//	err := swig.AddJobWithTx(ctx, tx, &EmailWorker{
//	    To: "user@example.com",
//	    Subject: "Welcome!",
//	})
//	if err != nil {
//	    return err
//	}
//	return tx.Commit(ctx)
//
// Example with database/sql:
//
//	tx, _ := db.BeginTx(ctx, nil)
//	defer tx.Rollback()
//
//	err := swig.AddJobWithTx(ctx, tx, &EmailWorker{
//	    To: "user@example.com",
//	    Subject: "Welcome!",
//	})
//	if err != nil {
//	    return err
//	}
//	return tx.Commit()
func (s *Swig) AddJobWithTx(ctx context.Context, tx interface{}, workerWithArgs interface{}, opts ...JobOptions) error {
	// Type assert to check if it implements Worker interface
	if _, ok := workerWithArgs.(interface{ JobName() string }); !ok {
		return fmt.Errorf("workerWithArgs must implement JobName() string")
	}

	// Get transaction adapter from driver
	txAdapter, err := s.driver.AddJobWithTx(ctx, tx)
	if err != nil {
		return fmt.Errorf("invalid transaction for driver: %w", err)
	}

	// Use default options if none provided
	jobOpts := DefaultJobOptions()
	if len(opts) > 0 {
		jobOpts = opts[0]
	}

	// Serialize the worker (which contains the args)
	argsJSON, err := json.Marshal(workerWithArgs)
	if err != nil {
		return fmt.Errorf("failed to serialize job args: %w", err)
	}

	insertSQL := `
		INSERT INTO swig_jobs (
			kind,
			queue,
			payload,
			priority,
			scheduled_for,
			status
		) VALUES ($1, $2, $3, $4, $5, 'pending')
	`

	return txAdapter.Exec(
		ctx,
		insertSQL,
		workerWithArgs.(interface{ JobName() string }).JobName(),
		string(jobOpts.Queue),
		argsJSON,
		jobOpts.Priority,
		jobOpts.RunAt,
	)
}

// startWorker runs a worker goroutine that:
// 1. Listens for notifications about new jobs
// 2. Attempts to acquire and process jobs using SELECT FOR UPDATE SKIP LOCKED
// 3. Handles job completion and failure
func (s *Swig) startWorker(ctx context.Context, queueType QueueTypes) {
	listener, err := s.driver.NewListener(ctx, "swig_jobs")
	if err != nil {
		log.Printf("Failed to start listening: %v", err)
		return
	}
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := listener.Close(closeCtx); err != nil {
			log.Printf("Failed to close listener: %v", err)
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return
		case <-s.shutdown:
			return
		default:
			// Try to acquire and process a job
			if err := s.processNextJob(ctx, queueType, listener); err != nil {
				log.Printf("Error processing job: %v", err)
				// Small backoff on error
				time.Sleep(time.Second)
			}
		}
	}
}

// processNextJob attempts to acquire and process the next available job using SKIP LOCKED
func (s *Swig) processNextJob(ctx context.Context, queueType QueueTypes, listener drivers.Listener) error {
	// Generate unique worker ID for this job acquisition
	workerID := pkg.GenerateWorkerID()

	// Check for "no rows" errors from both database/sql and pgx
	// acquireAndProcessJob reports whether it claimed a job.
	acquireAndProcessJob := func(ctx context.Context, queueType QueueTypes, specificJobID string) (bool, error) {
		var acquireSQL string
		var args []interface{}

		if specificJobID != "" {
			// Try to acquire a specific job first (from notification)
			acquireSQL = `
				UPDATE swig_jobs
				SET status = 'processing',
					instance_id = $1,
					worker_id = $2,
					locked_at = NOW(),
					attempts = attempts + 1
				WHERE id = $3
					AND status = 'pending'
					AND scheduled_for <= NOW()
				RETURNING id, kind, payload;`
			args = []interface{}{s.workerID, workerID, specificJobID}
		} else {
			// Otherwise try to acquire any job with priority handling
			acquireSQL = `
				UPDATE swig_jobs
				SET status = 'processing',
					instance_id = $1,
					worker_id = $2,
					locked_at = NOW(),
					attempts = attempts + 1
				WHERE id = (
					SELECT id
					FROM swig_jobs
					WHERE status = 'pending'
						AND scheduled_for <= NOW()
						AND (
							(queue = 'priority' AND EXISTS (
								SELECT 1 FROM swig_jobs 
								WHERE queue = 'priority' 
								AND status = 'pending'
								AND scheduled_for <= NOW()
							))
							OR (queue = $3 AND NOT EXISTS (
								SELECT 1 FROM swig_jobs 
								WHERE queue = 'priority' 
								AND status = 'pending'
								AND scheduled_for <= NOW()
							))
						)
					ORDER BY 
						queue = 'priority' DESC,
						priority DESC,
						created_at
					FOR UPDATE SKIP LOCKED
					LIMIT 1
				)
				RETURNING id, kind, payload;`
			args = []interface{}{s.workerID, workerID, string(queueType)}
		}

		var jobID string
		var kind string
		var payload []byte

		err := s.driver.QueryRow(ctx, acquireSQL, args...).Scan(&jobID, &kind, &payload)
		if err == sql.ErrNoRows || err != nil && (err.Error() == "no rows in result set" || err.Error() == "no rows in result") {
			return false, nil // No job available
		}
		if err != nil {
			return false, fmt.Errorf("failed to acquire job: %w", err)
		}

		// Find the worker implementation
		worker, ok := s.Workers.GetWorker(kind)
		if !ok {
			return true, fmt.Errorf("no worker registered for job type: %s", kind)
		}

		// Unmarshal the payload
		if err := json.Unmarshal(payload, worker); err != nil {
			return true, fmt.Errorf("failed to unmarshal job payload: %w", err)
		}

		stopHeartbeat := s.startHeartbeat(ctx, jobID, workerID)
		err = worker.(interface{ Process(context.Context) error }).Process(ctx)
		stopHeartbeat()

		// Update job status based on processing result. The worker_id check
		// skips the update if the lease expired and the job was requeued.
		if err != nil {
			updateSQL := `
				UPDATE swig_jobs
				SET status = CASE 
						WHEN attempts >= max_attempts THEN 'failed'
						ELSE 'pending'
					END,
					scheduled_for = NOW() + ($4::float8 * pow(2, attempts) * interval '1 millisecond'),
					last_error = $3,
					last_error_at = NOW(),
					instance_id = NULL,
					worker_id = NULL,
					locked_at = NULL
				WHERE id = $1 AND worker_id = $2`
			if err := s.driver.Exec(ctx, updateSQL, jobID, workerID, err.Error(), s.options.RetryBackoff.Milliseconds()); err != nil {
				return true, fmt.Errorf("failed to update failed job: %w", err)
			}
		} else {
			updateSQL := `
				UPDATE swig_jobs
				SET status = 'completed',
					instance_id = NULL,
					worker_id = NULL,
					locked_at = NULL
				WHERE id = $1 AND worker_id = $2`
			if err := s.driver.Exec(ctx, updateSQL, jobID, workerID); err != nil {
				return true, fmt.Errorf("failed to update completed job: %w", err)
			}
		}

		return true, nil
	}

	// First try to acquire and process any job. After a job, look for the next
	// one right away: backlogged and retried jobs send no notification.
	claimed, err := acquireAndProcessJob(ctx, queueType, "")
	if err != nil || claimed {
		return err
	}

	// If no job was available, wait for a notification. The wait is bounded
	// because scheduled and retried jobs become due without a notification.
	waitCtx, cancel := context.WithTimeout(ctx, s.options.PollInterval)
	defer cancel()
	notification, err := listener.WaitForNotification(waitCtx)
	if err != nil {
		// Don't report context cancellation as an error - this is normal during shutdown
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil
		}
		return fmt.Errorf("notification error: %w", err)
	}

	// Process the notification if we received one
	if notification != nil && notification.Payload != "" {
		// Try to parse the notification payload (should be JSON with job ID)
		var notificationData struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal([]byte(notification.Payload), &notificationData); err == nil && notificationData.ID != "" {
			// Try to acquire and process the specific job from the notification
			_, err := acquireAndProcessJob(ctx, queueType, notificationData.ID)
			return err
		}
	}

	return nil
}

// startHeartbeat renews the job's lease while it runs so the leader does not
// requeue long jobs. The returned function stops the heartbeat and waits for it.
func (s *Swig) startHeartbeat(ctx context.Context, jobID, workerID string) func() {
	heartbeatCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})

	go func() {
		defer close(done)
		ticker := time.NewTicker(s.options.JobLease / 3)
		defer ticker.Stop()

		for {
			select {
			case <-heartbeatCtx.Done():
				return
			case <-ticker.C:
				heartbeatSQL := `
					UPDATE swig_jobs
					SET locked_at = NOW()
					WHERE id = $1 AND worker_id = $2 AND status = 'processing'`
				if err := s.driver.Exec(heartbeatCtx, heartbeatSQL, jobID, workerID); err != nil && heartbeatCtx.Err() == nil {
					log.Printf("Failed to renew lease for job %s: %v", jobID, err)
				}
			}
		}
	}()

	return func() {
		cancel()
		<-done
	}
}

// Close drops all Swig-related tables from the database. This is a destructive operation
// that will permanently delete all jobs and leader election data. It's particularly useful
// in testing environments or when completely removing Swig from your database.
//
// This method will:
// 1. Drop the swig_jobs table (including all jobs, history, and triggers)
// 2. Drop the swig_leader table (removing leader election state)
//
// Note: This is different from Stop() which gracefully shuts down workers.
// Close() is for complete cleanup of database objects.
//
// Example:
//
//	// In a test environment
//	swig := NewSwig(driver, configs, workers)
//	defer swig.Close(ctx) // Clean up after tests
//
// Returns an error if the tables cannot be dropped or if the context is cancelled.
func (s *Swig) Close(ctx context.Context) error {
	// Drop the notify trigger first to avoid dependency issues
	dropTriggerSQL := `
		DROP TRIGGER IF EXISTS swig_jobs_notify_trigger ON swig_jobs;
		DROP FUNCTION IF EXISTS notify_job_created();
	`
	if err := s.driver.Exec(ctx, dropTriggerSQL); err != nil {
		return fmt.Errorf("failed to drop trigger and function: %w", err)
	}

	// Drop the tables
	dropTablesSQL := `
		DROP TABLE IF EXISTS swig_jobs;
		DROP TABLE IF EXISTS swig_leader;
	`
	if err := s.driver.Exec(ctx, dropTablesSQL); err != nil {
		return fmt.Errorf("failed to drop tables: %w", err)
	}

	log.Printf("Successfully dropped all Swig tables and triggers")
	return nil
}

// AddJobs adds multiple jobs in a single database operation
func (s *Swig) AddJobs(ctx context.Context, jobs []drivers.BatchJob) error {
	if len(jobs) == 0 {
		return nil
	}

	// Start a transaction for atomic batch insertion
	return s.driver.WithTx(ctx, func(tx drivers.Transaction) error {
		// Build the values clause and args
		var values []string
		var args []interface{}
		argCount := 1

		for _, job := range jobs {
			// Type assert to check if it implements Worker interface
			if _, ok := job.Worker.(interface{ JobName() string }); !ok {
				return fmt.Errorf("worker must implement JobName() string")
			}

			// Serialize the worker
			argsJSON, err := json.Marshal(job.Worker)
			if err != nil {
				return fmt.Errorf("failed to serialize job args: %w", err)
			}

			// Add values for this job
			values = append(values, fmt.Sprintf("($%d, $%d, $%d, $%d, $%d, 'pending')",
				argCount, argCount+1, argCount+2, argCount+3, argCount+4))

			args = append(args,
				job.Worker.(interface{ JobName() string }).JobName(),
				string(job.Opts.Queue),
				argsJSON,
				job.Opts.Priority,
				job.Opts.RunAt,
			)
			argCount += 5
		}

		// Build and execute the insert query
		insertSQL := fmt.Sprintf(`
			INSERT INTO swig_jobs (
				kind,
				queue,
				payload,
				priority,
				scheduled_for,
				status
			) VALUES %s
		`, strings.Join(values, ","))

		return tx.Exec(ctx, insertSQL, args...)
	})
}

// AddJobsWithTx adds multiple jobs as part of an existing transaction
func (s *Swig) AddJobsWithTx(ctx context.Context, tx interface{}, jobs []drivers.BatchJob) error {
	return s.driver.AddJobsWithTx(ctx, tx, jobs)
}
