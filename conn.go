package sqlschema

import (
	"context"
)

// DB is an interface that abstracts QueryRowContext and QueryContext
// across sql.DB, sqlx.DB, and pgx.Conn.
type DB interface {
	QueryRowContext(ctx context.Context, query string, args ...interface{}) Row
	QueryContext(ctx context.Context, query string, args ...interface{}) (Rows, error)
}

// Row abstracts sql.Row, sqlx.Row, and pgx.Row
type Row interface {
	Scan(dest ...interface{}) error
}

// Rows abstracts sql.Rows, sqlx.Rows, and pgx.Rows
type Rows interface {
	Scan(dest ...interface{}) error
	Next() bool
	Close() error
	Err() error
}
