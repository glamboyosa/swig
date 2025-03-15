package swig

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
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

	leaderLockID  = 1234567 // Arbitrary number for advisory lock
	leaderKey     = "queue_leader"
	leaderTTL     = 30 * time.Second
	retryInterval = 5 * time.Second
)

// minimum number of workers to start
const minWorkers = 3

// Default timeout for graceful shutdown
const defaultShutdownTimeout = 30 * time.Second

type SwigQueueConfig struct {
	QueueType  QueueTypes
	MaxWorkers int
}
type Swig struct {
	swigQueueConfig []SwigQueueConfig
	driver          drivers.Driver
	Workers         workers.WorkerRegistry
	activeWorkers   sync.WaitGroup // Track active workers
	shutdown        chan struct{}  // Signal for graceful shutdown
	leaderID        string         // Current leader ID if we're the leader
	workerID        string         // Unique ID for this worker instance
}

// NewSwig creates a new job queue instance with the specified database driver,
// queue configurations, and worker registry. Each queue config defines a queue type (Default/Priority)
// and its worker pool size. The worker registry must contain all worker types that will be processed.
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
func NewSwig(driver drivers.Driver, swigQueueConfig []SwigQueueConfig, workers workers.WorkerRegistry) *Swig {
	return &Swig{
		driver:          driver,
		swigQueueConfig: swigQueueConfig,
		Workers:         workers,
		shutdown:        make(chan struct{}),
		workerID:        pkg.GenerateWorkerID(),
	}
}

// tryBecomeLeader attempts to acquire leadership using advisory locks
func (s *Swig) tryBecomeLeader(ctx context.Context) error {
	// Try to acquire advisory lock
	var acquired bool
	err := s.driver.QueryRow(ctx, `SELECT pg_try_advisory_lock($1)`, leaderLockID).Scan(&acquired)
	if err != nil || !acquired {
		return fmt.Errorf("failed to acquire leader lock: %w", err)
	}

	// If we got the lock, update the leader table
	s.leaderID = pkg.GenerateWorkerID()
	err = s.driver.Exec(ctx, `
		INSERT INTO swig_leader (id, leader_id, expires_at)
		VALUES ($1, $2, NOW() + $3::interval)
		ON CONFLICT (id) DO UPDATE
		SET leader_id = $2,
			expires_at = NOW() + $3::interval
	`, leaderKey, s.leaderID, leaderTTL.String())

	if err != nil {
		// Release the advisory lock if we couldn't update the table
		s.driver.Exec(ctx, `SELECT pg_advisory_unlock($1)`, leaderLockID)
		return fmt.Errorf("failed to update leader record: %w", err)
	}

	// Start leader duties in background
	go s.performLeaderDuties(ctx)

	return nil
}

// performLeaderDuties handles leader responsibilities like retrying failed jobs
func (s *Swig) performLeaderDuties(ctx context.Context) {
	ticker := time.NewTicker(retryInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-s.shutdown:
			return
		case <-ticker.C:
			if err := s.retryFailedJobs(ctx); err != nil {
				log.Printf("Error retrying failed jobs: %v", err)
			}
		}
	}
}

// retryFailedJobs finds failed jobs that can be retried and requeues them
func (s *Swig) retryFailedJobs(ctx context.Context) error {
	// Find failed jobs that haven't exceeded max attempts and apply backoff
	retrySQL := `
		UPDATE swig_jobs
		SET status = 'pending',
			instance_id = NULL,
			worker_id = NULL,
			locked_at = NULL,
			scheduled_for = CASE 
				-- Apply exponential backoff: 2^attempts seconds
				WHEN attempts > 0 THEN NOW() + (interval '1 second' * pow(2, attempts))
				ELSE NOW()
			END
		WHERE status = 'failed'
			AND attempts < max_attempts
			AND (
				instance_id IS NULL 
				OR locked_at < NOW() - interval '5 minutes'
			)
			-- Only retry jobs that have waited their backoff period
			AND (
				last_error IS NULL 
				OR last_error_at < NOW() - (interval '1 second' * pow(2, attempts))
			)
		RETURNING id, attempts`

	var jobIDs []string
	var totalAttempts int
	rows, err := s.driver.Query(ctx, retrySQL)
	if err != nil {
		// Don't report context cancellation as an error - this is normal during shutdown
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil
		}
		return fmt.Errorf("failed to query failed jobs: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var id string
		var attempts int
		if err := rows.Scan(&id, &attempts); err != nil {
			return fmt.Errorf("failed to scan job ID: %w", err)
		}
		jobIDs = append(jobIDs, id)
		totalAttempts += attempts
	}

	if len(jobIDs) > 0 {
		log.Printf("Requeued %d failed jobs for retry (avg attempts: %.1f)",
			len(jobIDs), float64(totalAttempts)/float64(len(jobIDs)))
	}

	return nil
}

// Start initializes the Swig queue and creates the necessary tables
func (s *Swig) Start(ctx context.Context) {
	createTableSQL := `
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

	CREATE TRIGGER swig_jobs_notify_trigger
		AFTER INSERT ON swig_jobs
		FOR EACH ROW
		EXECUTE FUNCTION notify_job_created();`

	createLeaderTableSQL := `
	CREATE TABLE IF NOT EXISTS swig_leader (
		id TEXT PRIMARY KEY,          -- Usually 'queue_leader'
		leader_id UUID NOT NULL,      -- Unique ID of current leader
		expires_at TIMESTAMPTZ NOT NULL,
		acquired_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		
		-- Ensure expires_at is always in the future
		CONSTRAINT leader_expires_future CHECK (expires_at > NOW())
	);
	
	-- Unlogged for better performance since this is temporary state
	ALTER TABLE swig_leader SET UNLOGGED;`

	s.driver.Exec(ctx, createTableSQL)
	s.driver.Exec(ctx, createLeaderTableSQL)

	// Try to become leader
	if err := s.tryBecomeLeader(ctx); err != nil {
		log.Printf("Failed to become leader: %v", err)
	}

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
				s.startWorker(ctx, qType)
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

	// Signal all workers to stop
	close(s.shutdown)

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

	// Release any leader locks we might be holding
	if s.leaderID != "" {
		unlockSQL := `
			DELETE FROM swig_leader
			WHERE leader_id = $1
		`
		if err := s.driver.Exec(ctx, unlockSQL, s.leaderID); err != nil {
			log.Printf("Failed to release leader lock: %v", err)
		}

		// Also release the advisory lock
		if err := s.driver.Exec(ctx, `SELECT pg_advisory_unlock($1)`, leaderLockID); err != nil {
			log.Printf("Failed to release advisory lock: %v", err)
		}
	}

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
	// Start listening for notifications
	if err := s.driver.Listen(ctx, "swig_jobs"); err != nil {
		log.Printf("Failed to start listening: %v", err)
		return
	}

	for {
		select {
		case <-ctx.Done():
			return
		default:
			// Try to acquire and process a job
			if err := s.processNextJob(ctx, queueType); err != nil {
				log.Printf("Error processing job: %v", err)
				// Small backoff on error
				time.Sleep(time.Second)
			}
		}
	}
}

// processNextJob attempts to acquire and process the next available job using SKIP LOCKED
func (s *Swig) processNextJob(ctx context.Context, queueType QueueTypes) error {
	// Generate unique worker ID for this job acquisition
	workerID := pkg.GenerateWorkerID()

	// Check for "no rows" errors from both database/sql and pgx
	acquireAndProcessJob := func(ctx context.Context, queueType QueueTypes, specificJobID string) error {
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
			return nil // No job available
		}
		if err != nil {
			return fmt.Errorf("failed to acquire job: %w", err)
		}

		// Find the worker implementation
		worker, ok := s.Workers.GetWorker(kind)
		if !ok {
			return fmt.Errorf("no worker registered for job type: %s", kind)
		}

		// Unmarshal the payload
		if err := json.Unmarshal(payload, worker); err != nil {
			return fmt.Errorf("failed to unmarshal job payload: %w", err)
		}

		// Process the job
		err = worker.(interface{ Process(context.Context) error }).Process(ctx)

		// Update job status based on processing result
		if err != nil {
			updateSQL := `
				UPDATE swig_jobs
				SET status = CASE 
						WHEN attempts >= max_attempts THEN 'failed'
						ELSE 'pending'
					END,
					last_error = $2,
					last_error_at = NOW(),
					instance_id = NULL,
					worker_id = NULL,
					locked_at = NULL
				WHERE id = $1`
			if err := s.driver.Exec(ctx, updateSQL, jobID, err.Error()); err != nil {
				return fmt.Errorf("failed to update failed job: %w", err)
			}
		} else {
			updateSQL := `
				UPDATE swig_jobs
				SET status = 'completed',
					instance_id = NULL,
					worker_id = NULL,
					locked_at = NULL
				WHERE id = $1`
			if err := s.driver.Exec(ctx, updateSQL, jobID); err != nil {
				return fmt.Errorf("failed to update completed job: %w", err)
			}
		}

		return nil
	}

	// First try to acquire and process any job
	err := acquireAndProcessJob(ctx, queueType, "")
	if err != nil {
		return err
	}

	// If no job was available, wait for notification
	notification, err := s.driver.WaitForNotification(ctx)
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
			return acquireAndProcessJob(ctx, queueType, notificationData.ID)
		}
	}

	return nil
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
