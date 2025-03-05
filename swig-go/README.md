# Swig

> Job queues as intuitive as taking a swig 🍺

Swig is a robust, PostgreSQL-backed job queue system for Go applications, designed for developers who need reliable background job processing with minimal setup.

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

// 1. Define your worker
type EmailWorker struct {
    To      string `json:"to"`
    Subject string `json:"subject"`
    Body    string `json:"body"`
}

func (w *EmailWorker) JobName() string {
    return "send_email"
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
    
    // Configure queues (default setup)
    configs := []swig.SwigQueueConfig{
        {QueueType: swig.Default, MaxWorkers: 5},
    }
    
    // Create and start Swig
    swigClient := swig.NewSwig(driver, configs)
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

## Understanding Workers

Workers in Swig are structs that:
1. Implement the `JobName() string` method
2. Have JSON-serializable fields for job arguments

Example worker:
```go
type ImageResizeWorker struct {
    ImageURL string `json:"image_url"`
    Width    int    `json:"width"`
    Height   int    `json:"height"`
}

func (w *ImageResizeWorker) JobName() string {
    return "resize_image"
}
```

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