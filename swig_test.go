package swig

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/glamboyosa/swig/drivers"
	"github.com/glamboyosa/swig/workers"
	"github.com/jackc/pgx/v5/pgxpool"
)

// fastOptions keeps the integration tests quick without long sleeps.
var fastOptions = SwigOptions{
	JobLease:       2 * time.Second,
	PollInterval:   100 * time.Millisecond,
	LeaderInterval: 50 * time.Millisecond,
	RetryBackoff:   50 * time.Millisecond,
}

// recorder tracks every Process call by job number. Tests run sequentially,
// so a package-level recorder is safe.
var recorder = &jobRecorder{}

type jobRecorder struct {
	mu   sync.Mutex
	runs map[int][]time.Time
}

func (r *jobRecorder) reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.runs = make(map[int][]time.Time)
}

// record stores a run of job n and returns how many times n has run.
func (r *jobRecorder) record(n int) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.runs[n] = append(r.runs[n], time.Now())
	return len(r.runs[n])
}

func (r *jobRecorder) runsOf(n int) []time.Time {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]time.Time(nil), r.runs[n]...)
}

type countingWorker struct {
	N int `json:"n"`
}

func (w *countingWorker) JobName() string { return "test_counting" }

func (w *countingWorker) Process(ctx context.Context) error {
	recorder.record(w.N)
	return nil
}

type flakyWorker struct {
	N         int `json:"n"`
	FailTimes int `json:"fail_times"`
}

func (w *flakyWorker) JobName() string { return "test_flaky" }

func (w *flakyWorker) Process(ctx context.Context) error {
	if recorder.record(w.N) <= w.FailTimes {
		return errTestFailure
	}
	return nil
}

type slowWorker struct {
	N        int           `json:"n"`
	Duration time.Duration `json:"duration"`
}

func (w *slowWorker) JobName() string { return "test_slow" }

func (w *slowWorker) Process(ctx context.Context) error {
	recorder.record(w.N)
	select {
	case <-time.After(w.Duration):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

var errTestFailure = errors.New("intentional test failure")

// testEnv is one database driver under test plus a way to open caller-owned
// transactions for it.
type testEnv struct {
	driver  drivers.Driver
	beginTx func(t *testing.T) (tx interface{}, commit, rollback func() error)
}

// forEachDriver runs fn against the pgx and database/sql drivers. It skips when
// POSTGRES_URL is unset, matching the CI workflow.
func forEachDriver(t *testing.T, fn func(t *testing.T, env testEnv)) {
	url := os.Getenv("POSTGRES_URL")
	if url == "" {
		t.Skip("POSTGRES_URL is not set; skipping PostgreSQL integration test")
	}

	t.Run("pgx", func(t *testing.T) {
		config, err := pgxpool.ParseConfig(url)
		if err != nil {
			t.Fatalf("parse config: %v", err)
		}
		// Every worker holds a listener connection, so the default pool is too small.
		config.MaxConns = 40
		pool, err := pgxpool.NewWithConfig(context.Background(), config)
		if err != nil {
			t.Fatalf("open pool: %v", err)
		}
		t.Cleanup(pool.Close)

		driver, err := drivers.NewPgxDriver(pool)
		if err != nil {
			t.Fatalf("new pgx driver: %v", err)
		}
		fn(t, testEnv{
			driver: driver,
			beginTx: func(t *testing.T) (interface{}, func() error, func() error) {
				ctx := context.Background()
				tx, err := pool.Begin(ctx)
				if err != nil {
					t.Fatalf("begin: %v", err)
				}
				return tx, func() error { return tx.Commit(ctx) }, func() error { return tx.Rollback(ctx) }
			},
		})
	})

	t.Run("database_sql", func(t *testing.T) {
		// lib/pq defaults to sslmode=require, but the CI service has no TLS.
		sqlURL := url
		if !strings.Contains(sqlURL, "sslmode=") {
			separator := "?"
			if strings.Contains(sqlURL, "?") {
				separator = "&"
			}
			sqlURL += separator + "sslmode=disable"
		}
		db, err := sql.Open("postgres", sqlURL)
		if err != nil {
			t.Fatalf("open db: %v", err)
		}
		t.Cleanup(func() { db.Close() })

		driver, err := drivers.NewSQLDriver(db, sqlURL)
		if err != nil {
			t.Fatalf("new sql driver: %v", err)
		}
		fn(t, testEnv{
			driver: driver,
			beginTx: func(t *testing.T) (interface{}, func() error, func() error) {
				tx, err := db.BeginTx(context.Background(), nil)
				if err != nil {
					t.Fatalf("begin: %v", err)
				}
				return tx, tx.Commit, tx.Rollback
			},
		})
	})
}

func newTestRegistry() workers.WorkerRegistry {
	registry := workers.NewWorkerRegistry()
	for _, w := range []interface{}{&countingWorker{}, &flakyWorker{}, &slowWorker{}} {
		if err := registry.RegisterWorker(w); err != nil {
			panic(err)
		}
	}
	return *registry
}

// resetSchema drops all Swig tables and clears the recorder.
func resetSchema(t *testing.T, env testEnv) {
	t.Helper()
	recorder.reset()
	if err := NewSwig(env.driver, nil, newTestRegistry()).Close(context.Background()); err != nil {
		t.Fatalf("drop schema: %v", err)
	}
}

// startSwig starts an instance against the existing schema and stops it when
// the test ends.
func startSwig(t *testing.T, env testEnv, opts SwigOptions) *Swig {
	t.Helper()
	s := NewSwig(env.driver, []SwigQueueConfig{{QueueType: Default, MaxWorkers: 5}}, newTestRegistry(), opts)
	s.Start(context.Background())
	t.Cleanup(func() {
		if err := s.Stop(context.Background()); err != nil {
			t.Errorf("stop: %v", err)
		}
	})
	return s
}

func waitFor(t *testing.T, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out after %v waiting for %s", timeout, what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

type jobRow struct {
	status    string
	attempts  int
	lastError sql.NullString
}

// jobState returns the row for job number n, or ok=false when it does not exist.
func jobState(t *testing.T, env testEnv, n int) (row jobRow, ok bool) {
	t.Helper()
	err := env.driver.QueryRow(context.Background(), `
		SELECT status, attempts, last_error
		FROM swig_jobs
		WHERE (payload->>'n')::int = $1`, n).Scan(&row.status, &row.attempts, &row.lastError)
	if err != nil {
		if err == sql.ErrNoRows || err.Error() == "no rows in result set" {
			return jobRow{}, false
		}
		t.Fatalf("query job %d: %v", n, err)
	}
	return row, true
}

func countJobs(t *testing.T, env testEnv, status string) int {
	t.Helper()
	var count int
	if err := env.driver.QueryRow(context.Background(),
		`SELECT count(*) FROM swig_jobs WHERE status = $1`, status).Scan(&count); err != nil {
		t.Fatalf("count jobs: %v", err)
	}
	return count
}

func hasStatus(t *testing.T, env testEnv, n int, status string) func() bool {
	return func() bool {
		row, ok := jobState(t, env, n)
		return ok && row.status == status
	}
}

func TestConcurrentWorkersProcessEachJobOnce(t *testing.T) {
	forEachDriver(t, func(t *testing.T, env testEnv) {
		resetSchema(t, env)
		// Two instances on one database, ten workers in total.
		s := startSwig(t, env, fastOptions)
		startSwig(t, env, fastOptions)

		const jobCount = 200
		batch := make([]drivers.BatchJob, jobCount)
		for i := range batch {
			batch[i] = drivers.BatchJob{
				Worker: &countingWorker{N: i},
				Opts:   drivers.JobOptions{Queue: string(Default), RunAt: time.Now()},
			}
		}
		if err := s.AddJobs(context.Background(), batch); err != nil {
			t.Fatalf("add jobs: %v", err)
		}

		waitFor(t, 30*time.Second, "all jobs to complete", func() bool {
			return countJobs(t, env, "completed") == jobCount
		})

		for i := 0; i < jobCount; i++ {
			if runs := len(recorder.runsOf(i)); runs != 1 {
				t.Errorf("job %d ran %d times, want 1", i, runs)
			}
		}
	})
}

func TestAddJobWithTxFollowsTransaction(t *testing.T) {
	forEachDriver(t, func(t *testing.T, env testEnv) {
		resetSchema(t, env)
		s := startSwig(t, env, fastOptions)
		ctx := context.Background()

		tx, _, rollback := env.beginTx(t)
		if err := s.AddJobWithTx(ctx, tx, &countingWorker{N: 1}); err != nil {
			t.Fatalf("add job in rolled back tx: %v", err)
		}
		if err := rollback(); err != nil {
			t.Fatalf("rollback: %v", err)
		}

		tx, commit, _ := env.beginTx(t)
		if err := s.AddJobWithTx(ctx, tx, &countingWorker{N: 2}); err != nil {
			t.Fatalf("add job in committed tx: %v", err)
		}
		if err := commit(); err != nil {
			t.Fatalf("commit: %v", err)
		}

		waitFor(t, 10*time.Second, "committed job to complete", hasStatus(t, env, 2, "completed"))

		if _, ok := jobState(t, env, 1); ok {
			t.Errorf("job from rolled back transaction exists")
		}
		if runs := len(recorder.runsOf(1)); runs != 0 {
			t.Errorf("job from rolled back transaction ran %d times", runs)
		}
		if runs := len(recorder.runsOf(2)); runs != 1 {
			t.Errorf("committed job ran %d times, want 1", runs)
		}
	})
}

func TestFailingJobRetriesWithBackoff(t *testing.T) {
	forEachDriver(t, func(t *testing.T, env testEnv) {
		resetSchema(t, env)
		s := startSwig(t, env, fastOptions)
		ctx := context.Background()

		if err := s.AddJob(ctx, &flakyWorker{N: 1, FailTimes: 2}); err != nil {
			t.Fatalf("add recovering job: %v", err)
		}
		if err := s.AddJob(ctx, &flakyWorker{N: 2, FailTimes: 100}); err != nil {
			t.Fatalf("add always failing job: %v", err)
		}

		waitFor(t, 10*time.Second, "recovering job to complete", hasStatus(t, env, 1, "completed"))
		waitFor(t, 10*time.Second, "failing job to be marked failed", hasStatus(t, env, 2, "failed"))

		recovered, _ := jobState(t, env, 1)
		if recovered.attempts != 3 {
			t.Errorf("recovering job attempts = %d, want 3", recovered.attempts)
		}
		runs := recorder.runsOf(1)
		if len(runs) != 3 {
			t.Fatalf("recovering job ran %d times, want 3", len(runs))
		}
		// After attempt n the job waits RetryBackoff * 2^n.
		for i, want := range []time.Duration{2 * fastOptions.RetryBackoff, 4 * fastOptions.RetryBackoff} {
			if gap := runs[i+1].Sub(runs[i]); gap < want {
				t.Errorf("gap before attempt %d = %v, want at least %v", i+2, gap, want)
			}
		}

		failed, _ := jobState(t, env, 2)
		if failed.attempts != 3 {
			t.Errorf("failing job attempts = %d, want 3", failed.attempts)
		}
		if failed.lastError.String != errTestFailure.Error() {
			t.Errorf("failing job last_error = %q, want %q", failed.lastError.String, errTestFailure.Error())
		}
		if runs := len(recorder.runsOf(2)); runs != 3 {
			t.Errorf("failing job ran %d times, want 3", runs)
		}
	})
}

func TestExpiredLeaseIsRequeued(t *testing.T) {
	forEachDriver(t, func(t *testing.T, env testEnv) {
		resetSchema(t, env)
		startSwig(t, env, fastOptions)

		// Simulate workers that were killed mid-job: rows stay 'processing'
		// with a lock older than the lease.
		insertSQL := `
			INSERT INTO swig_jobs (kind, queue, payload, status, attempts, instance_id, worker_id, locked_at)
			VALUES ('test_counting', 'default', $1, 'processing', $2,
				gen_random_uuid(), gen_random_uuid(), NOW() - interval '1 hour')`
		if err := env.driver.Exec(context.Background(), insertSQL, `{"n": 1}`, 1); err != nil {
			t.Fatalf("insert stuck job: %v", err)
		}
		if err := env.driver.Exec(context.Background(), insertSQL, `{"n": 2}`, 3); err != nil {
			t.Fatalf("insert exhausted stuck job: %v", err)
		}

		waitFor(t, 10*time.Second, "stuck job to complete", hasStatus(t, env, 1, "completed"))
		waitFor(t, 10*time.Second, "exhausted stuck job to fail", hasStatus(t, env, 2, "failed"))

		recovered, _ := jobState(t, env, 1)
		if recovered.attempts != 2 {
			t.Errorf("recovered job attempts = %d, want 2", recovered.attempts)
		}
		if runs := len(recorder.runsOf(1)); runs != 1 {
			t.Errorf("recovered job ran %d times, want 1", runs)
		}

		exhausted, _ := jobState(t, env, 2)
		if exhausted.lastError.String != "job lease expired" {
			t.Errorf("exhausted job last_error = %q, want %q", exhausted.lastError.String, "job lease expired")
		}
		if runs := len(recorder.runsOf(2)); runs != 0 {
			t.Errorf("exhausted job ran %d times, want 0", runs)
		}
	})
}

func TestHeartbeatKeepsLongJobLeased(t *testing.T) {
	forEachDriver(t, func(t *testing.T, env testEnv) {
		resetSchema(t, env)
		opts := fastOptions
		opts.JobLease = time.Second
		s := startSwig(t, env, opts)

		// The job outlives its lease; only heartbeats keep it claimed.
		if err := s.AddJob(context.Background(), &slowWorker{N: 1, Duration: 2500 * time.Millisecond}); err != nil {
			t.Fatalf("add job: %v", err)
		}

		waitFor(t, 10*time.Second, "slow job to complete", hasStatus(t, env, 1, "completed"))

		row, _ := jobState(t, env, 1)
		if row.attempts != 1 {
			t.Errorf("slow job attempts = %d, want 1", row.attempts)
		}
		if runs := len(recorder.runsOf(1)); runs != 1 {
			t.Errorf("slow job ran %d times, want 1", runs)
		}
	})
}

func TestScheduledJobRunsWithoutNewInsert(t *testing.T) {
	forEachDriver(t, func(t *testing.T, env testEnv) {
		resetSchema(t, env)
		s := startSwig(t, env, fastOptions)

		runAt := time.Now().Add(700 * time.Millisecond)
		opts := DefaultJobOptions()
		opts.RunAt = runAt
		if err := s.AddJob(context.Background(), &countingWorker{N: 1}, opts); err != nil {
			t.Fatalf("add job: %v", err)
		}

		// Its only notification arrives before it is due, so polling must pick it up.
		waitFor(t, 10*time.Second, "scheduled job to complete", hasStatus(t, env, 1, "completed"))

		runs := recorder.runsOf(1)
		if len(runs) != 1 {
			t.Fatalf("scheduled job ran %d times, want 1", len(runs))
		}
		// Allow a little skew between the host clock and the database clock.
		if earliest := runAt.Add(-50 * time.Millisecond); runs[0].Before(earliest) {
			t.Errorf("scheduled job ran at %v, before its run time %v", runs[0], runAt)
		}
	})
}

func TestStartAgainstExistingSchema(t *testing.T) {
	forEachDriver(t, func(t *testing.T, env testEnv) {
		resetSchema(t, env)
		first := NewSwig(env.driver, []SwigQueueConfig{{QueueType: Default, MaxWorkers: 3}}, newTestRegistry(), fastOptions)
		first.Start(context.Background())
		if err := first.Stop(context.Background()); err != nil {
			t.Fatalf("stop first instance: %v", err)
		}

		second := startSwig(t, env, fastOptions)
		if err := second.AddJob(context.Background(), &countingWorker{N: 1}); err != nil {
			t.Fatalf("add job: %v", err)
		}

		waitFor(t, 10*time.Second, "job on restarted instance to complete", hasStatus(t, env, 1, "completed"))
	})
}

func TestBacklogDrainsWithoutWaitingForPoll(t *testing.T) {
	forEachDriver(t, func(t *testing.T, env testEnv) {
		resetSchema(t, env)
		setup := NewSwig(env.driver, nil, newTestRegistry(), fastOptions)
		setup.Start(context.Background())
		if err := setup.Stop(context.Background()); err != nil {
			t.Fatalf("stop setup instance: %v", err)
		}

		// Jobs enqueued while no worker is listening get no usable notification.
		const jobCount = 30
		for i := 0; i < jobCount; i++ {
			if err := setup.AddJob(context.Background(), &countingWorker{N: i}); err != nil {
				t.Fatalf("add job: %v", err)
			}
		}

		opts := fastOptions
		opts.PollInterval = time.Minute
		startSwig(t, env, opts)

		waitFor(t, 10*time.Second, "backlog to drain", func() bool {
			return countJobs(t, env, "completed") == jobCount
		})
	})
}
