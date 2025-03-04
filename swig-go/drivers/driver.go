package drivers

import "context"

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
}

type Transaction interface {
	Exec(ctx context.Context, sql string, args ...interface{}) error
	Query(ctx context.Context, sql string, args ...interface{}) (Rows, error)
	QueryRow(ctx context.Context, sql string, args ...interface{}) Row
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
