package sqlschema

import (
	"context"
	"database/sql"

	"github.com/jmoiron/sqlx"
)

type SqlxDBWrapper struct {
	db *sqlx.DB
}

func NewSqlxDBWrapper(db *sqlx.DB) SqlxDBWrapper {
	return SqlxDBWrapper{db}
}

func (d SqlxDBWrapper) QueryRowContext(ctx context.Context, query string, args ...interface{}) Row {
	return sqlRowWrapper{d.db.QueryRowContext(ctx, query, args...)}
}

func (d SqlxDBWrapper) QueryContext(ctx context.Context, query string, args ...interface{}) (Rows, error) {
	rows, err := d.db.QueryxContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	return sqlRowsWrapper{rows.Rows}, nil
}

// Ensure sql.Row implements Row
type sqlRowWrapper struct {
	row *sql.Row
}

func (r sqlRowWrapper) Scan(dest ...interface{}) error {
	return r.row.Scan(dest...)
}

// Ensure sql.Rows implements Rows
type sqlRowsWrapper struct {
	rows *sql.Rows
}

func (r sqlRowsWrapper) Scan(dest ...interface{}) error {
	return r.rows.Scan(dest...)
}

func (r sqlRowsWrapper) Next() bool {
	return r.rows.Next()
}

func (r sqlRowsWrapper) Close() error {
	return r.rows.Close()
}

func (r sqlRowsWrapper) Err() error {
	return r.rows.Err()
}
