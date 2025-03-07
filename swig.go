package swig

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/glamboyosa/swig/drivers"
	"github.com/glamboyosa/swig/workers"
)

type QueueTypes string

const (
	Default  QueueTypes = "default"
	Priority QueueTypes = "priority"
)

// minimum number of workers to start
const minWorkers = 3

type SwigQueueConfig struct {
	QueueType  QueueTypes
	MaxWorkers int
}
type Swig struct {
	swigQueueConfig []SwigQueueConfig
	driver          drivers.Driver
	Workers         workers.WorkerRegistry
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
	}
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
		locked_by UUID,
		locked_at TIMESTAMPTZ,
		last_error TEXT,
		
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

	// Start worker pools for each queue
	for _, config := range s.swigQueueConfig {
		workers := config.MaxWorkers
		if workers < minWorkers {
			workers = minWorkers
		}

		// Start worker pool for this queue
		for i := 0; i < workers; i++ {
			go s.startWorker(ctx, config.QueueType)
		}
	}
}

// Wait for active workers to finish and release any leader locks we might be holding
func (s *Swig) Stop(ctx context.Context) error {
	// Release any leader locks we might be holding
	// unlockSQL := `
	//     DELETE FROM swig_leader
	//     WHERE leader_id = $1
	// `
	// // Assuming we store our leader_id somewhere in Swig struct
	// if err := s.driver.Exec(ctx, unlockSQL, s.leaderID); err != nil {
	// 	return err
	// }

	// // Close database connections cleanly
	// if err := s.driver.Close(); err != nil {
	// 	return err
	// }

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
	// Acquire a job with FOR UPDATE SKIP LOCKED
	// Always check priority queue first, then fall back to assigned queue
	acquireSQL := `
		UPDATE swig_jobs
		SET status = 'processing',
			locked_by = gen_random_uuid(),
			locked_at = NOW(),
			attempts = attempts + 1
		WHERE id = (
			SELECT id
			FROM swig_jobs
			WHERE status = 'pending'
				AND scheduled_for <= NOW()
				AND (
					-- First try to get priority jobs (if any exist)
					(queue = 'priority' AND EXISTS (
						SELECT 1 FROM swig_jobs 
						WHERE queue = 'priority' 
						AND status = 'pending'
						AND scheduled_for <= NOW()
					))
					-- If no priority jobs, use the worker's assigned queue
					OR (queue = $1 AND NOT EXISTS (
						SELECT 1 FROM swig_jobs 
						WHERE queue = 'priority' 
						AND status = 'pending'
						AND scheduled_for <= NOW()
					))
				)
			ORDER BY 
				queue = 'priority' DESC, -- Priority queue jobs first
				priority DESC,           -- Then by job priority
				created_at              -- Finally by age
			FOR UPDATE SKIP LOCKED
			LIMIT 1
		)
		RETURNING id, kind, payload;`

	var jobID string
	var kind string
	var payload []byte

	err := s.driver.QueryRow(ctx, acquireSQL, string(queueType)).Scan(&jobID, &kind, &payload)
	if err == sql.ErrNoRows {
		// No jobs available, wait for notification
		notification, err := s.driver.WaitForNotification(ctx)
		if err != nil {
			return fmt.Errorf("notification error: %w", err)
		}
		// Process notification if needed
		_ = notification
		return nil
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
	var updateSQL string
	if err != nil {
		updateSQL = `
			UPDATE swig_jobs
			SET status = CASE 
					WHEN attempts >= max_attempts THEN 'failed'
					ELSE 'pending'
				END,
				last_error = $2,
				locked_by = NULL,
				locked_at = NULL
			WHERE id = $1`
		if err := s.driver.Exec(ctx, updateSQL, jobID, err.Error()); err != nil {
			return fmt.Errorf("failed to update failed job: %w", err)
		}
	} else {
		updateSQL = `
			UPDATE swig_jobs
			SET status = 'completed',
				locked_by = NULL,
				locked_at = NULL
			WHERE id = $1`
		if err := s.driver.Exec(ctx, updateSQL, jobID); err != nil {
			return fmt.Errorf("failed to update completed job: %w", err)
		}
	}

	return nil
}
