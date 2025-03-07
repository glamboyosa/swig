package main

// import (
// 	"context"
// 	"database/sql"
// 	"fmt"
// 	"log"
// 	"time"

// 	swig "github.com/glamboyosa/swig"
// 	"github.com/glamboyosa/swig/drivers"
// 	_ "github.com/lib/pq"
// )

// // EmailWorker demonstrates a basic worker implementation
// type EmailWorker struct {
// 	To      string `json:"to"`
// 	Subject string `json:"subject"`
// 	Body    string `json:"body"`
// }

// func (w *EmailWorker) JobName() string {
// 	return "send_email"
// }

// func (w *EmailWorker) Process(ctx context.Context) error {
// 	fmt.Printf("Sending email to: %s with subject: %s\n", w.To, w.Subject)
// 	return nil
// }

// func main() {
// 	ctx := context.Background()

// 	// Connect to PostgreSQL using database/sql
// 	db, err := sql.Open("postgres", "postgres://postgres:postgres@localhost:5432/swig_example?sslmode=disable")
// 	if err != nil {
// 		log.Fatalf("Unable to connect to database: %v", err)
// 	}
// 	defer db.Close()

// 	// Create SQL driver
// 	driver, err := drivers.NewSQLDriver(db)
// 	if err != nil {
// 		log.Fatalf("Unable to create driver: %v", err)
// 	}

// 	// Create and register workers
// 	workers := swig.NewWorkerRegistry()
// 	workers.RegisterWorker(&EmailWorker{})

// 	// Configure queues
// 	configs := []swig.SwigQueueConfig{
// 		{QueueType: swig.Default, MaxWorkers: 5},
// 		{QueueType: swig.Priority, MaxWorkers: 2},
// 	}

// 	// Create and start Swig
// 	swigClient := swig.NewSwig(driver, configs, workers)
// 	swigClient.Start(ctx)

// 	// Add some example jobs
// 	err = swigClient.AddJob(ctx, &EmailWorker{
// 		To:      "user@example.com",
// 		Subject: "Welcome to Swig!",
// 		Body:    "This is a regular priority job",
// 	})
// 	if err != nil {
// 		log.Printf("Failed to add regular job: %v", err)
// 	}

// 	// Add a high priority job
// 	err = swigClient.AddJob(ctx, &EmailWorker{
// 		To:      "urgent@example.com",
// 		Subject: "Urgent Notice!",
// 		Body:    "This is a high priority job",
// 	}, swig.JobOptions{
// 		Queue:    swig.Priority,
// 		Priority: 5,
// 	})
// 	if err != nil {
// 		log.Printf("Failed to add priority job: %v", err)
// 	}

// 	// Add a scheduled job
// 	err = swigClient.AddJob(ctx, &EmailWorker{
// 		To:      "scheduled@example.com",
// 		Subject: "Scheduled Email",
// 		Body:    "This job will run in 1 minute",
// 	}, swig.JobOptions{
// 		RunAt: time.Now().Add(time.Minute),
// 	})
// 	if err != nil {
// 		log.Printf("Failed to add scheduled job: %v", err)
// 	}

// 	// Keep the program running
// 	select {}
// }
