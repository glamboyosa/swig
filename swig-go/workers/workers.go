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

