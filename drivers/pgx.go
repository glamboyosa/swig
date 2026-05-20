package drivers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type PgxDriver struct {
	pool *pgxpool.Pool
}

type pgxListener struct {
	conn    *pgxpool.Conn
	channel string
}

type pgxAdvisoryLock struct {
	conn   *pgxpool.Conn
	lockID int64
}

type pgxTxAdapter struct {
	tx pgx.Tx
}

type pgxRowsAdapter struct {
	rows pgx.Rows
}

func (r *pgxRowsAdapter) Next() bool {
	return r.rows.Next()
}

func (r *pgxRowsAdapter) Scan(dest ...interface{}) error {
	return r.rows.Scan(dest...)
}

func (r *pgxRowsAdapter) Close() error {
	r.rows.Close()
	return nil
}

func (tx *pgxTxAdapter) Exec(ctx context.Context, sql string, args ...interface{}) error {
	_, err := tx.tx.Exec(ctx, sql, args...)
	return err
}

func (tx *pgxTxAdapter) Query(ctx context.Context, sql string, args ...interface{}) (Rows, error) {
	rows, err := tx.tx.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	return &pgxRowsAdapter{rows: rows}, nil
}

func (tx *pgxTxAdapter) QueryRow(ctx context.Context, sql string, args ...interface{}) Row {
	return tx.tx.QueryRow(ctx, sql, args...)
}

func (l *pgxListener) WaitForNotification(ctx context.Context) (*Notification, error) {
	notification, err := l.conn.Conn().WaitForNotification(ctx)
	if err != nil {
		return nil, fmt.Errorf("wait for notification error: %w", err)
	}

	return &Notification{
		Channel: notification.Channel,
		Payload: notification.Payload,
	}, nil
}

func (l *pgxListener) Close(ctx context.Context) error {
	defer l.conn.Release()
	_, err := l.conn.Exec(ctx, "UNLISTEN "+quoteIdentifier(l.channel))
	return err
}

func (l *pgxAdvisoryLock) Exec(ctx context.Context, sql string, args ...interface{}) error {
	_, err := l.conn.Exec(ctx, sql, args...)
	return err
}

func (l *pgxAdvisoryLock) Query(ctx context.Context, sql string, args ...interface{}) (Rows, error) {
	rows, err := l.conn.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	return &pgxRowsAdapter{rows: rows}, nil
}

func (l *pgxAdvisoryLock) QueryRow(ctx context.Context, sql string, args ...interface{}) Row {
	return l.conn.QueryRow(ctx, sql, args...)
}

func (l *pgxAdvisoryLock) Unlock(ctx context.Context) error {
	_, err := l.conn.Exec(ctx, `SELECT pg_advisory_unlock($1)`, l.lockID)
	return err
}

func (l *pgxAdvisoryLock) Close(ctx context.Context) error {
	defer l.conn.Release()
	return l.Unlock(ctx)
}

// NewPgxDriver creates a new pgx-based driver implementation for PostgreSQL.
// It uses pgx's native connection pool for better performance and features like
// automatic connection recovery, statement caching, and native LISTEN/NOTIFY support.
//
// Parameters:
//   - pool: A *pgxpool.Pool instance. Must be initialized and connected.
//     The pool handles connection lifecycle and maintains a connection pool
//     for optimal performance.
//
// Returns:
//   - Driver: The database driver implementation
//   - error: Non-nil if the pool is nil or of wrong type
//
// Example:
//
//	config, _ := pgxpool.ParseConfig("postgres://localhost:5432/myapp")
//	pool, _ := pgxpool.NewWithConfig(context.Background(), config)
//	driver, err := NewPgxDriver(pool)
func NewPgxDriver(pool interface{}) (Driver, error) {
	if pgxPool, ok := pool.(*pgxpool.Pool); ok {
		return &PgxDriver{pool: pgxPool}, nil
	}
	return nil, errors.New("invalid pool type")
}

func (d *PgxDriver) WithTx(ctx context.Context, fn func(tx Transaction) error) error {
	pgxTx, err := d.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer pgxTx.Rollback(ctx)

	if err := fn(&pgxTxAdapter{tx: pgxTx}); err != nil {
		return err
	}
	return pgxTx.Commit(ctx)
}

func (d *PgxDriver) Exec(ctx context.Context, sql string, args ...interface{}) error {
	_, err := d.pool.Exec(ctx, sql, args...)
	return err
}

func (d *PgxDriver) Query(ctx context.Context, sql string, args ...interface{}) (Rows, error) {
	rows, err := d.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	return &pgxRowsAdapter{rows: rows}, nil
}

func (d *PgxDriver) QueryRow(ctx context.Context, sql string, args ...interface{}) Row {
	return d.pool.QueryRow(ctx, sql, args...)
}

func (d *PgxDriver) Listen(ctx context.Context, channel string) error {
	_, err := d.pool.Exec(ctx, "LISTEN "+quoteIdentifier(channel))
	return err
}

func (d *PgxDriver) Notify(ctx context.Context, channel string, payload string) error {
	_, err := d.pool.Exec(ctx, "SELECT pg_notify($1, $2)", channel, payload)
	return err
}

// AddJobWithTx accepts an external pgx transaction and wraps it in our Transaction interface
func (d *PgxDriver) AddJobWithTx(ctx context.Context, tx interface{}) (Transaction, error) {
	if pgxTx, ok := tx.(pgx.Tx); ok {
		return &pgxTxAdapter{tx: pgxTx}, nil
	}
	return nil, errors.New("invalid transaction type: expected pgx.Tx")
}

// WaitForNotification waits for a notification on any channel this connection is listening on
func (d *PgxDriver) WaitForNotification(ctx context.Context) (*Notification, error) {
	conn, err := d.pool.Acquire(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to acquire connection: %w", err)
	}
	defer conn.Release()

	// Wait for notification
	pgxNotification, err := conn.Conn().WaitForNotification(ctx)
	if err != nil {
		return nil, fmt.Errorf("wait for notification error: %w", err)
	}

	return &Notification{
		Channel: pgxNotification.Channel,
		Payload: pgxNotification.Payload,
	}, nil
}

// NewListener creates a dedicated connection for LISTEN/NOTIFY. PostgreSQL
// listeners are session-scoped, so the same connection must both LISTEN and wait.
func (d *PgxDriver) NewListener(ctx context.Context, channel string) (Listener, error) {
	conn, err := d.pool.Acquire(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to acquire listener connection: %w", err)
	}

	if _, err := conn.Exec(ctx, "LISTEN "+quoteIdentifier(channel)); err != nil {
		conn.Release()
		return nil, fmt.Errorf("failed to listen on %s: %w", channel, err)
	}

	return &pgxListener{conn: conn, channel: channel}, nil
}

// TryAdvisoryLock acquires a session-scoped advisory lock on a dedicated
// connection and returns that connection wrapped as the lock owner.
func (d *PgxDriver) TryAdvisoryLock(ctx context.Context, lockID int64) (AdvisoryLock, bool, error) {
	conn, err := d.pool.Acquire(ctx)
	if err != nil {
		return nil, false, fmt.Errorf("failed to acquire advisory lock connection: %w", err)
	}

	var acquired bool
	if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock($1)`, lockID).Scan(&acquired); err != nil {
		conn.Release()
		return nil, false, fmt.Errorf("failed to acquire advisory lock: %w", err)
	}
	if !acquired {
		conn.Release()
		return nil, false, nil
	}

	return &pgxAdvisoryLock{conn: conn, lockID: lockID}, true, nil
}

// AddJobsWithTx adds multiple jobs as part of an existing transaction
func (d *PgxDriver) AddJobsWithTx(ctx context.Context, tx interface{}, jobs []BatchJob) error {
	if len(jobs) == 0 {
		return nil
	}

	// Get transaction adapter from driver
	txAdapter, err := d.AddJobWithTx(ctx, tx)
	if err != nil {
		return fmt.Errorf("invalid transaction for driver: %w", err)
	}

	// Build the values clause and args
	var values []string
	var args []interface{}
	argCount := 1

	for _, job := range jobs {
		// Type assert to check if it implements Worker interface
		if _, ok := job.Worker.(interface{ JobName() string }); !ok {
			return fmt.Errorf("worker must implement JobName() string")
		}

		// Serialize the worker
		argsJSON, err := json.Marshal(job.Worker)
		if err != nil {
			return fmt.Errorf("failed to serialize job args: %w", err)
		}

		// Add values for this job
		values = append(values, fmt.Sprintf("($%d, $%d, $%d, $%d, $%d, 'pending')",
			argCount, argCount+1, argCount+2, argCount+3, argCount+4))

		args = append(args,
			job.Worker.(interface{ JobName() string }).JobName(),
			string(job.Opts.Queue),
			argsJSON,
			job.Opts.Priority,
			job.Opts.RunAt,
		)
		argCount += 5
	}

	// Build and execute the insert query
	insertSQL := fmt.Sprintf(`
		INSERT INTO swig_jobs (
			kind,
			queue,
			payload,
			priority,
			scheduled_for,
			status
		) VALUES %s
	`, strings.Join(values, ","))

	return txAdapter.Exec(ctx, insertSQL, args...)
}
