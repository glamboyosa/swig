package drivers

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/lib/pq"
)

type SQLDriver struct {
	db      *sql.DB
	connStr string
}

type sqlTxAdapter struct {
	tx *sql.Tx
}

type sqlRowsAdapter struct {
	rows *sql.Rows
}

func (r *sqlRowsAdapter) Next() bool {
	return r.rows.Next()
}

func (r *sqlRowsAdapter) Scan(dest ...interface{}) error {
	return r.rows.Scan(dest...)
}

func (r *sqlRowsAdapter) Close() error {
	return r.rows.Close()
}

func (tx *sqlTxAdapter) Exec(ctx context.Context, sql string, args ...interface{}) error {
	_, err := tx.tx.ExecContext(ctx, sql, args...)
	return err
}

func (tx *sqlTxAdapter) Query(ctx context.Context, sql string, args ...interface{}) (Rows, error) {
	rows, err := tx.tx.QueryContext(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	return &sqlRowsAdapter{rows: rows}, nil
}

func (tx *sqlTxAdapter) QueryRow(ctx context.Context, sql string, args ...interface{}) Row {
	return tx.tx.QueryRowContext(ctx, sql, args...)
}

// NewSQLDriver creates a new database/sql driver implementation for PostgreSQL.
// It requires both a database connection and the original connection string because
// the lib/pq notification listener needs the connection string for establishing
// its own connection.
//
// Parameters:
//   - db: An initialized *sql.DB connection pool
//   - connStr: The PostgreSQL connection string (e.g., "postgres://user:pass@localhost:5432/dbname")
//
// Returns:
//   - Driver: The database driver implementation
//   - error: Non-nil if the database connection is nil
//
// Example:
//
//	db, _ := sql.Open("postgres", "postgres://localhost:5432/myapp")
//	driver, err := NewSQLDriver(db, "postgres://localhost:5432/myapp")
func NewSQLDriver(db *sql.DB, connStr string) (Driver, error) {
	if db == nil {
		return nil, errors.New("nil database connection")
	}
	return &SQLDriver{
		db:      db,
		connStr: connStr,
	}, nil
}

func (d *SQLDriver) WithTx(ctx context.Context, fn func(tx Transaction) error) error {
	sqlTx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer sqlTx.Rollback()

	if err := fn(&sqlTxAdapter{tx: sqlTx}); err != nil {
		return err
	}
	return sqlTx.Commit()
}

func (d *SQLDriver) Exec(ctx context.Context, sql string, args ...interface{}) error {
	_, err := d.db.ExecContext(ctx, sql, args...)
	return err
}

func (d *SQLDriver) Query(ctx context.Context, sql string, args ...interface{}) (Rows, error) {
	rows, err := d.db.QueryContext(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	return &sqlRowsAdapter{rows: rows}, nil
}

func (d *SQLDriver) QueryRow(ctx context.Context, sql string, args ...interface{}) Row {
	return d.db.QueryRowContext(ctx, sql, args...)
}

func (d *SQLDriver) Listen(ctx context.Context, channel string) error {
	_, err := d.db.ExecContext(ctx, "LISTEN "+channel)
	return err
}

func (d *SQLDriver) Notify(ctx context.Context, channel string, payload string) error {
	_, err := d.db.ExecContext(ctx, "SELECT pg_notify($1, $2)", channel, payload)
	return err
}

// AddJobWithTx accepts an external database/sql transaction and wraps it in our Transaction interface
func (d *SQLDriver) AddJobWithTx(ctx context.Context, tx interface{}) (Transaction, error) {
	if sqlTx, ok := tx.(*sql.Tx); ok {
		return &sqlTxAdapter{tx: sqlTx}, nil
	}
	return nil, errors.New("invalid transaction type: expected *sql.Tx")
}

// WaitForNotification waits for a notification on any channel this connection is listening on
func (d *SQLDriver) WaitForNotification(ctx context.Context) (*Notification, error) {
	listener := pq.NewListener(d.connStr,
		10*time.Second, // Max reconnect wait
		time.Minute,    // Max ping interval
		func(ev pq.ListenerEventType, err error) {
			if err != nil {
				log.Printf("Listener error: %v\n", err)
			}
		})
	defer listener.Close()

	// Wait for notification or context cancellation
	select {
	case notification := <-listener.Notify:
		if notification == nil {
			return nil, fmt.Errorf("received nil notification")
		}
		return &Notification{
			Channel: notification.Channel,
			Payload: notification.Extra,
		}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
