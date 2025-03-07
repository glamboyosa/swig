package drivers

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type PgxDriver struct {
	pool *pgxpool.Pool
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
	_, err := d.pool.Exec(ctx, "LISTEN "+channel)
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
