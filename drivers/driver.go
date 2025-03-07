package drivers

import (
	"context"
	"database/sql"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Core database operations needed for the job queue
type Driver interface {
	WithTx(ctx context.Context, fn func(tx Transaction) error) error
	// Basic operations
	Exec(ctx context.Context, sql string, args ...interface{}) error
	Query(ctx context.Context, sql string, args ...interface{}) (Rows, error)
	QueryRow(ctx context.Context, sql string, args ...interface{}) Row

	// Postgres-specific operations
	Listen(ctx context.Context, channel string) error
	Notify(ctx context.Context, channel string, payload string) error
	// New method to handle external transactions
	AddJobWithTx(ctx context.Context, tx interface{}) (Transaction, error)
	WaitForNotification(ctx context.Context) (*Notification, error)
}

// Transaction represents our internal transaction interface
type Transaction interface {
	Exec(ctx context.Context, sql string, args ...interface{}) error
	Query(ctx context.Context, sql string, args ...interface{}) (Rows, error)
	QueryRow(ctx context.Context, sql string, args ...interface{}) Row
}

// PgxTx represents a pgx transaction that can be passed in
type PgxTx interface {
	Exec(ctx context.Context, sql string, args ...interface{}) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...interface{}) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...interface{}) pgx.Row
}

// SQLTx represents a database/sql transaction that can be passed in
type SQLTx interface {
	ExecContext(ctx context.Context, sql string, args ...interface{}) (sql.Result, error)
	QueryContext(ctx context.Context, sql string, args ...interface{}) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, sql string, args ...interface{}) *sql.Row
}

// Row/Rows interfaces (minimal required functionality)
type Row interface {
	Scan(dest ...interface{}) error
}

type Rows interface {
	Next() bool
	Scan(dest ...interface{}) error
	Close() error
}

// Notification represents a PostgreSQL notification
type Notification struct {
	Channel string
	Payload string
}
