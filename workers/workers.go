package workers

import (
	"context"
	"fmt"
	"reflect"
	"time"
)

type runtimeWorker interface {
	JobName() string
	Process(context.Context) error
}

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
	workers map[string]func() interface{}
}

type Worker[T any] interface {
	JobName() string
	Process(ctx context.Context, job Job[T]) error
}

func NewWorkerRegistry() *WorkerRegistry {
	return &WorkerRegistry{
		workers: make(map[string]func() interface{}),
	}
}

// RegisterWorker adds a worker implementation to the registry.
// It accepts any type that implements the Worker interface and performs
// runtime type checking to ensure the worker is properly implemented.
func (wr *WorkerRegistry) RegisterWorker(worker interface{}) error {
	w, ok := worker.(runtimeWorker)
	if !ok {
		if _, hasName := worker.(interface{ JobName() string }); !hasName {
			return fmt.Errorf("worker must implement JobName() string")
		}
		return fmt.Errorf("worker must implement Process(context.Context) error")
	}

	workerType := reflect.TypeOf(worker)
	if workerType == nil {
		return fmt.Errorf("worker must not be nil")
	}
	workerValue := reflect.ValueOf(worker)
	if workerType.Kind() == reflect.Ptr && workerValue.IsNil() {
		return fmt.Errorf("worker must not be nil")
	}

	jobName := w.JobName()

	if workerType.Kind() != reflect.Ptr {
		wr.workers[jobName] = func() interface{} {
			return worker
		}
		return nil
	}

	elemType := workerType.Elem()
	if elemType.Kind() != reflect.Struct {
		return fmt.Errorf("worker must be a pointer to a struct")
	}

	wr.workers[jobName] = func() interface{} {
		return reflect.New(elemType).Interface()
	}
	return nil
}

// GetWorker retrieves a worker implementation by its job name
func (wr *WorkerRegistry) GetWorker(jobName string) (interface{}, bool) {
	factory, exists := wr.workers[jobName]
	if !exists {
		return nil, false
	}
	return factory(), true
}
