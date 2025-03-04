package drivers

import (
	"context"
	"database/sql"
	"errors"
)

type SQLDriver struct {
	db *sql.DB
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

func NewSQLDriver(db *sql.DB) (Driver, error) {
	if db == nil {
		return nil, errors.New("nil database connection")
	}
	return &SQLDriver{db: db}, nil
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
