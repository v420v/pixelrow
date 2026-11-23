// Package driver defines the common interface that hides differences between databases.
package driver

import "context"

type Config struct {
	Host     string
	Port     int
	User     string
	Password string
	Database string
}

type Driver interface {
	// Open establishes a connection. Canceling ctx aborts the attempt.
	Open(ctx context.Context, cfg Config) (Conn, error)
	QuoteIdent(name string) string
}

// Conn is a single database session.
// To read the schema while a query is running, open another Conn.
type Conn interface {
	Ping(ctx context.Context) error

	Databases(ctx context.Context) ([]string, error)
	// Schemas returns the schema names in the connected database.
	Schemas(ctx context.Context) ([]string, error)
	// Tables returns the tables and views in schema, sorted by name.
	Tables(ctx context.Context, schema string) ([]Table, error)

	// Query runs sql, which may contain multiple statements.
	// Canceling ctx stops the running query on the server.
	// The resulting error matches errors.Is(err, context.Canceled).
	// The Conn stays usable after a cancel.
	Query(ctx context.Context, sql string) (Results, error)

	Close() error
}

type TableKind int

const (
	KindTable TableKind = iota
	KindView
	KindMaterializedView
)

type Table struct {
	Name string
	Kind TableKind
}

// Results streams the results of a Query, one statement at a time.
//
//	for res.NextResultSet() {
//		cols := res.Columns()
//		for res.Next() {
//			row := res.Row()
//		}
//		tag := res.Command()
//	}
//	err := res.Close()
//
// Always call Close when done.
type Results interface {
	// NextResultSet advances to the next statement's result. Call it before reading the first one too.
	NextResultSet() bool
	// Columns returns the columns of the current result. Empty for statements that return no rows, such as UPDATE.
	Columns() []Column
	Next() bool
	// Row returns the current row. The caller may keep the returned slice.
	Row() []Value
	// Command returns the current statement's command tag, such as "UPDATE 3". Valid after Next returns false.
	Command() string
	// RowsAffected returns the number of rows the current statement affected. Valid after Next returns false.
	RowsAffected() int64
	Err() error
	// Close releases the Conn for the next operation and returns the same error as Err.
	// If rows remain unread, it stops the running query.
	Close() error
}

// ColumnKind is a rough category of a column's values, used to choose alignment and sort order.
type ColumnKind int

const (
	KindText ColumnKind = iota
	KindNumber
	KindBool
	KindTime
	KindBinary
	KindJSON
)

type Column struct {
	Name string
	// Type is the database's type name, such as "int4" or "timestamptz".
	Type string
	Kind ColumnKind
}

// Value is a single cell, holding the text representation the database returned.
// Null tells NULL apart from an empty string.
type Value struct {
	Text string
	Null bool
}

// Error is an error returned by the database server.
type Error struct {
	// Code is the database-specific error code (SQLSTATE for PostgreSQL).
	Code    string
	Message string
	Detail  string
	Hint    string
	// Position is the 1-based character offset of the error within sql, or 0 if unknown.
	Position int
}

func (e *Error) Error() string { return e.Message }

