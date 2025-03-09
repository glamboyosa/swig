package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/glamboyosa/swig"
	"github.com/glamboyosa/swig/drivers"
	"github.com/glamboyosa/swig/workers"
	_ "github.com/lib/pq"
)

// EmailWorker demonstrates a basic worker implementation
type EmailWorker struct {
	To      string `json:"to"`
	Subject string `json:"subject"`
	Body    string `json:"body"`
}

func (w *EmailWorker) JobName() string {
	return "send_email"
}

func (w *EmailWorker) Process(ctx context.Context) error {
	fmt.Printf("Sending email to: %s with subject: %s\n", w.To, w.Subject)
	return nil
}

func main() {
	ctx := context.Background()

	// Create a context that will be cancelled on SIGINT or SIGTERM
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Setup signal handling
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig := <-sigChan
		log.Printf("Received signal %v, initiating shutdown...", sig)
		cancel()
	}()

	connectionString := "postgres://postgres:postgres@localhost:5432/swig_example?sslmode=disable"
	// Connect to PostgreSQL using database/sql
	db, err := sql.Open("postgres", connectionString)
	if err != nil {
		log.Fatalf("Unable to connect to database: %v", err)
	}
	defer db.Close()

	// Create SQL driver
	driver, err := drivers.NewSQLDriver(db, connectionString)
	if err != nil {
		log.Fatalf("Unable to create driver: %v", err)
	}

	// Create and register workers
	workers := workers.NewWorkerRegistry()
	workers.RegisterWorker(&EmailWorker{})

	// Configure queues
	configs := []swig.SwigQueueConfig{
		{QueueType: swig.Default, MaxWorkers: 5},
		{QueueType: swig.Priority, MaxWorkers: 2},
	}

	// Create and start Swig
	swigClient := swig.NewSwig(driver, configs, *workers)
	swigClient.Start(ctx)
	defer func() {
		// Create a new context for shutdown since the main one might be cancelled
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer shutdownCancel()

		if err := swigClient.Stop(shutdownCtx); err != nil {
			log.Printf("Error during shutdown: %v", err)
		}
	}()

	// Add some example jobs
	err = swigClient.AddJob(ctx, &EmailWorker{
		To:      "user@example.com",
		Subject: "Welcome to Swig!",
		Body:    "This is a regular priority job",
	})
	if err != nil {
		log.Printf("Failed to add regular job: %v", err)
	}

	// Add a high priority job
	err = swigClient.AddJob(ctx, &EmailWorker{
		To:      "urgent@example.com",
		Subject: "Urgent Notice!",
		Body:    "This is a high priority job",
	}, swig.JobOptions{
		Queue:    swig.Priority,
		Priority: 5,
	})
	if err != nil {
		log.Printf("Failed to add priority job: %v", err)
	}

	// Add a scheduled job
	err = swigClient.AddJob(ctx, &EmailWorker{
		To:      "scheduled@example.com",
		Subject: "Scheduled Email",
		Body:    "This job will run in 1 minute",
	}, swig.JobOptions{
		RunAt: time.Now().Add(time.Minute),
	})
	if err != nil {
		log.Printf("Failed to add scheduled job: %v", err)
	}

	// Wait for shutdown signal
	<-ctx.Done()
	log.Println("Shutting down gracefully...")
}
