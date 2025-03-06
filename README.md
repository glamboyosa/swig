# Swig

> Job queues as refreshing as taking a swig 🍺

Swig is a robust, PostgreSQL-backed job queue system for Go applications, designed for developers who need reliable background job processing with minimal setup.

⚠️ **Alpha Status**: Swig is currently in alpha and actively being developed towards v1.0.0. The API may undergo changes during this phase. For stability in production environments, we strongly recommend pinning to a specific version:

```bash
go get github.com/swig/swig-go@v0.0.1-alpha
```

## Why Transactional Integrity Matters

In distributed systems, especially job queues, transactional integrity is crucial. Here's why:

- **No Lost Jobs**: Jobs are either fully committed or not at all. If your application crashes while enqueueing a job, you won't have partially created jobs.
- **Atomic Processing**: Jobs are processed exactly once. Using PostgreSQL's SKIP LOCK ensures no two workers can process the same job.
- **Data Consistency**: Jobs can be part of your application's transactions. For example, when creating a user:
  ```go
  tx, _ := db.BeginTx(ctx)
  // Create user
  userID := createUser(tx)
  // Enqueue welcome email in the same transaction
  swigClient.AddJob(ctx, &EmailWorker{
      To: email,
      Subject: "Welcome!",
  })
  tx.Commit()
  ```
  If either the user creation or job enqueueing fails, everything is rolled back. No welcome emails for non-existent users!

## Features

- **PostgreSQL-Powered**: Leverages PostgreSQL's SKIP LOCK for efficient job distribution
- **Transactional Integrity**: Jobs are processed exactly once with transactional guarantees
- **Leader Election**: Built-in leader election using advisory locks
- **Multiple Queue Support**: Priority and default queues out of the box
- **Type-Safe Job Arguments**: Strongly typed job arguments with Go generics
- **Simple API**: Intuitive API for enqueueing and processing jobs

## Installation

```bash
go get github.com/swig/swig-go
```

## Supported Drivers

Swig supports two PostgreSQL driver implementations:
- `pgx` - Using the high-performance [pgx](https://github.com/jackc/pgx) driver
- `sql` - Using Go's standard `database/sql` interface

## Understanding Workers

Workers in Swig are structs that implement two key requirements:
1. The `JobName() string` method to identify the worker type
2. The `Process(context.Context) error` method to execute the job
3. Have JSON-serializable fields for job arguments

Example worker:
```go
type EmailWorker struct {
    To      string `json:"to"`
    Subject string `json:"subject"`
    Body    string `json:"body"`
}

func (w *EmailWorker) JobName() string {
    return "send_email"
}

func (w *EmailWorker) Process(ctx context.Context) error {
    return sendEmail(w.To, w.Subject, w.Body)
}
```

## Quick Start

```go
package main

import (
    "context"
    "github.com/jackc/pgx/v5/pgxpool"
    "database/sql"
    _ "github.com/lib/pq"
    "github.com/swig/swig-go"
    "github.com/swig/swig-go/drivers"
)

// 1. Define your worker (as shown above in Understanding Workers)
type EmailWorker struct {
    To      string `json:"to"`
    Subject string `json:"subject"`
    Body    string `json:"body"`
}

func (w *EmailWorker) JobName() string {
    return "send_email"
}

func (w *EmailWorker) Process(ctx context.Context) error {
    return sendEmail(w.To, w.Subject, w.Body)
}

func main() {
    ctx := context.Background()

    // Setup database connection (choose one)
    
    // Option A: Using pgx
    pgxConfig, _ := pgxpool.ParseConfig("postgres://localhost:5432/myapp")
    pgxPool, _ := pgxpool.NewWithConfig(ctx, pgxConfig)
    driver, _ := drivers.NewPgxDriver(pgxPool)
    
    // Option B: Using database/sql
    // db, _ := sql.Open("postgres", "postgres://localhost:5432/myapp")
    // driver, _ := drivers.NewSQLDriver(db)
    
    // Create a worker registry and register your workers
    registry := swig.NewWorkerRegistry()
    registry.Register(&EmailWorker{})
    
    // Configure queues (default setup)
    configs := []swig.SwigQueueConfig{
        {QueueType: swig.Default, MaxWorkers: 5},
    }
    
    // Create and start Swig with the worker registry
    swigClient := swig.NewSwig(driver, configs, registry)
    swigClient.Start(ctx)
    
    // Add a job (uses default queue)
    err := swigClient.AddJob(ctx, &EmailWorker{
        To:      "user@example.com",
        Subject: "Welcome!",
        Body:    "Hello from Swig",
    })
}
```

## Queue Configuration

Swig supports multiple queues with different worker pools. While the default queue is sufficient for many applications, you can configure multiple queues for more complex scenarios:

```go
// Configure multiple queues
configs := []swig.SwigQueueConfig{
    {QueueType: swig.Default, MaxWorkers: 5},   // Default queue
    {QueueType: swig.Priority, MaxWorkers: 3},  // Priority queue
}

swigClient := swig.NewSwig(driver, configs)
```

Once you have multiple queues, you can specify which queue to use with JobOptions:

```go
// High priority email
err := swigClient.AddJob(ctx, &EmailWorker{
    To:      "urgent@example.com",
    Subject: "Urgent Notice!",
    Body:    "Priority message",
}, swig.JobOptions{
    Queue: swig.Priority,
    Priority: 5,
})

// Scheduled email in default queue
err = swigClient.AddJob(ctx, &EmailWorker{
    To:      "reminder@example.com",
    Subject: "Reminder",
    Body:    "Don't forget!",
}, swig.JobOptions{
    Queue: swig.Default,
    RunAt: time.Now().Add(24 * time.Hour),
})
```

Each queue operates independently with its own worker pool, allowing you to:
- Process priority jobs faster with dedicated workers
- Prevent low-priority jobs from blocking important tasks
- Scale worker pools based on queue requirements

## Job Processing

Swig handles job processing with:
- Automatic retries
- Error tracking
- Job status management
- Scheduled jobs
- Priority queues

## Architecture

Swig uses PostgreSQL's SKIP LOCK for efficient job distribution across multiple processes. This, combined with advisory locks for leader election, ensures:

- No duplicate job processing
- Fair job distribution
- High availability
- Transactional integrity

## Contributing

We welcome contributions! Please see our [Contributing Guide](CONTRIBUTING.md) for details.

## License

MIT License - see [LICENSE](LICENSE) for details.

## Worker Registration

Workers must be registered with Swig before they can process jobs. This is done through the worker registry:

```go
// Create a registry
registry := swig.NewWorkerRegistry()

// Register workers
registry.Register(&EmailWorker{})
registry.Register(&ImageResizeWorker{})

// Pass registry to Swig
swigClient := swig.NewSwig(driver, configs, registry)
```

The registry ensures that:
- Only registered worker types can be processed
- Worker implementations are validated at startup
- Job payloads can be properly deserialized 