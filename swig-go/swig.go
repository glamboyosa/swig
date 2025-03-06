package main

import (
	"context"
	"encoding/json"
	"fmt"
	"swig/drivers"
	"swig/workers"
	"time"
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
	)`

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
	//workers := make([]*Worker, len(s.swigQueueConfig))
	// for _, config := range s.swigQueueConfig {
	// 	for i := 0; i < config.MaxWorkers; i++ {
	// 		go s.startWorker(config.QueueType)
	// 	}
	// }
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

// AddJob is now a regular method that takes an interface{}
func (s *Swig) AddJob(ctx context.Context, worker interface{}, opts ...JobOptions) error {
	// Type assert to check if it implements Worker interface
	if _, ok := worker.(interface{ JobName() string }); !ok {
		return fmt.Errorf("worker must implement JobName() string")
	}
	// Use default options if none provided
	jobOpts := DefaultJobOptions()
	if len(opts) > 0 {
		jobOpts = opts[0]
	}

	// Serialize the worker (which contains the args)
	argsJSON, err := json.Marshal(worker)
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
		worker.(interface{ JobName() string }).JobName(),
		string(jobOpts.Queue),
		argsJSON,
		jobOpts.Priority,
		jobOpts.RunAt,
	)
}
