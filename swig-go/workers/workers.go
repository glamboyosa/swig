package workers

import (
	"context"
	"fmt"
	"time"
)

type Job[T any] struct {
	ID        string
	Kind      string
	Queue     string
	Args      T
	Attempts  int
	CreatedAt time.Time
	// Add any other metadata fields you want to expose to workers
}

// WorkerRegistry manages the registered job workers and their processing.
// For job notifications, it uses PostgreSQL LISTEN/NOTIFY when available:
// - With pgx driver: Native LISTEN/NOTIFY support
// - With database/sql + lib/pq: LISTEN/NOTIFY if properly configured
// - Fallback: Polling for new jobs if LISTEN/NOTIFY is unavailable
//
// TODO: Implement polling fallback for environments where LISTEN/NOTIFY
// is not available or configured.
type WorkerRegistry struct {
	workers map[string]interface{} // stores Worker[T] instances
}

type Worker[T any] interface {
	JobName() string
	Process(ctx context.Context, job Job[T]) error
}

func NewWorkerRegistry() *WorkerRegistry {
	return &WorkerRegistry{
		workers: make(map[string]interface{}),
	}
}

// RegisterWorker now uses interface{} with runtime type checking
func (wr *WorkerRegistry) RegisterWorker(worker interface{}) error {
	// Type assert to check if it implements required methods
	if w, ok := worker.(interface{ JobName() string }); !ok {
		return fmt.Errorf("worker must implement JobName() string")
	} else {
		wr.workers[w.JobName()] = worker
		return nil
	}
}

// REMEMEBER TO TELL USERS TO IMPLEMENT A JOB WORKER I.E. SORTWORKER
