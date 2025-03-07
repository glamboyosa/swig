package drivers

import (
	"context"
	"errors"

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
