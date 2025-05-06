package main

import (
	"context"
	"database/sql"
	"encoding/json"
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

	// Create users table for transaction examples
	createTableSQL := `
	CREATE TABLE IF NOT EXISTS users (
		id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
		email TEXT NOT NULL UNIQUE,
		created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
	);`

	if _, err := db.ExecContext(ctx, createTableSQL); err != nil {
		log.Fatalf("Failed to create users table: %v", err)
	}

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
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer shutdownCancel()

		if err := swigClient.Stop(shutdownCtx); err != nil {
			log.Printf("Error during shutdown: %v", err)
		}
		if err := swigClient.Close(shutdownCtx); err != nil {
			log.Printf("Error closing swig client: %v", err)
		}
	}()

	// Example 1: Using AddJobWithTx - Create user and send welcome email in same transaction
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		log.Printf("Failed to begin transaction: %v", err)
	}
	defer tx.Rollback() // Will be no-op if committed

	// Insert user
	var userEmail = "new@example.com"
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO users (email) VALUES ($1)
		ON CONFLICT (email) DO NOTHING
	`, userEmail); err != nil {
		log.Printf("Failed to insert user: %v", err)
		return
	}

	// Add welcome email job in same transaction
	err = swigClient.AddJobWithTx(ctx, tx, &EmailWorker{
		To:      userEmail,
		Subject: "Welcome to our platform!",
		Body:    "Thanks for joining.",
	})
	if err != nil {
		log.Printf("Failed to add welcome email job: %v", err)
		return
	}

	// Commit the transaction
	if err := tx.Commit(); err != nil {
		log.Printf("Failed to commit transaction: %v", err)
		return
	}
	log.Printf("Successfully created user and queued welcome email")

	// Example 2: Using WithTx helper - Delete user and send goodbye email
	err = driver.WithTx(ctx, func(tx drivers.Transaction) error {
		// Delete user
		if err := tx.Exec(ctx, `
			DELETE FROM users WHERE email = $1
		`, userEmail); err != nil {
			return fmt.Errorf("failed to delete user: %w", err)
		}

		// Create a proper EmailWorker instead of using a map
		goodbyeEmail := &EmailWorker{
			To:      userEmail,
			Subject: "Sorry to see you go!",
			Body:    "We hope you'll come back soon.",
		}

		// Serialize to JSON
		emailJSON, err := json.Marshal(goodbyeEmail)
		if err != nil {
			return fmt.Errorf("failed to marshal email: %w", err)
		}

		// Queue goodbye email
		if err := tx.Exec(ctx, `
			INSERT INTO swig_jobs (
				kind, queue, payload, status
			) VALUES (
				'send_email',
				'priority',
				$1,
				'pending'
			)
		`, emailJSON); err != nil {
			return fmt.Errorf("failed to queue goodbye email: %w", err)
		}

		return nil
	})
	if err != nil {
		log.Printf("Transaction failed: %v", err)
	} else {
		log.Printf("Successfully deleted user and queued goodbye email")
	}

	// Regular non-transactional job examples...

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

	// Example: Batch job insertion
	
		batchJobs := []drivers.BatchJob{
			{
				Worker: &EmailWorker{To: "batch1@example.com", Subject: "Batch Welcome", Body: "Welcome to our platform!"},
				Opts:   drivers.JobOptions{Queue: "default", Priority: 1, RunAt: time.Now()},
			},
			{
				Worker: &EmailWorker{To: "batch2@example.com", Subject: "Batch Welcome", Body: "Welcome to our platform!"},
				Opts:   drivers.JobOptions{Queue: "default", Priority: 1, RunAt: time.Now()},
			},
			{
				Worker: &EmailWorker{To: "batch3@example.com", Subject: "Batch Welcome", Body: "Welcome to our platform!"},
				Opts:   drivers.JobOptions{Queue: "default", Priority: 1, RunAt: time.Now()},
			},
		}

		log.Println("Adding batch jobs...")
		if err := swigClient.AddJobs(ctx, batchJobs); err != nil {
			log.Printf("Failed to add batch jobs: %v", err)
		} else {
			log.Println("Batch jobs added successfully")
		}

		// Example: Transactional batch insertion
		tx, err = db.Begin()
		if err != nil {
			log.Printf("Failed to begin transaction: %v", err)
			return
		}
		defer tx.Rollback()

		txBatchJobs := []drivers.BatchJob{
			{
				Worker: &EmailWorker{To: "tx1@example.com", Subject: "Transactional Welcome", Body: "Welcome to our platform!"},
				Opts:   drivers.JobOptions{Queue: "default", Priority: 1, RunAt: time.Now()},
			},
			{
				Worker: &EmailWorker{To: "tx2@example.com", Subject: "Transactional Welcome", Body: "Welcome to our platform!"},
				Opts:   drivers.JobOptions{Queue: "default", Priority: 1, RunAt: time.Now()},
			},
		}

		log.Println("Adding transactional batch jobs...")
		if err := swigClient.AddJobsWithTx(ctx, tx, txBatchJobs); err != nil {
			log.Printf("Failed to add transactional batch jobs: %v", err)
			return
		}

		if err := tx.Commit(); err != nil {
			log.Printf("Failed to commit transaction: %v", err)
			return
		}
		log.Println("Transactional batch jobs added successfully")
	

	// Wait for shutdown signal
	<-ctx.Done()
	log.Println("Shutting down gracefully...")
}
