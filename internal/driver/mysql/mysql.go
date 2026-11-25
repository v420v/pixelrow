// Package mysql implements driver.Driver for MySQL.
package mysql

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-sql-driver/mysql"

	"db/internal/driver"
)

type Driver struct{}

var _ driver.Driver = Driver{}

func (Driver) Open(ctx context.Context, cfg driver.Config) (driver.Conn, error) {
	port := cfg.Port
	if port == 0 {
		port = Driver{}.DefaultPort()
	}
	mc := mysql.NewConfig()
	mc.Net = "tcp"
	mc.Addr = net.JoinHostPort(cfg.Host, strconv.Itoa(port))
	mc.User = cfg.User
	mc.Passwd = cfg.Password
	mc.DBName = cfg.Database
	mc.MultiStatements = true
	connector, err := mysql.NewConnector(mc)
	if err != nil {
		return nil, err
	}

	// A Conn is one session, so the pool is only a way to reach a single pinned connection.
	db := sql.OpenDB(connector)
	sc, err := db.Conn(ctx)
	if err != nil {
		db.Close()
		return nil, wrapErr(ctx, err)
	}
	c := &conn{db: db, sc: sc}
	// The ID is what KILL QUERY names to cancel a query of this session.
	if err := sc.QueryRowContext(ctx, "SELECT CONNECTION_ID()").Scan(&c.id); err != nil {
		c.Close()
		return nil, wrapErr(ctx, err)
	}
	// A result read a page at a time waits between reads, and the server drops a client it cannot write to for this long.
	if _, err := sc.ExecContext(ctx, "SET SESSION net_write_timeout = 600"); err != nil {
		c.Close()
		return nil, wrapErr(ctx, err)
	}
	if cfg.ReadOnly {
		if _, err := sc.ExecContext(ctx, "SET SESSION TRANSACTION READ ONLY"); err != nil {
			c.Close()
			return nil, wrapErr(ctx, err)
		}
	}
	return c, nil
}

func (Driver) DefaultPort() int { return 3306 }

func (Driver) QuoteIdent(name string) string {
	return "`" + strings.ReplaceAll(name, "`", "``") + "`"
}

func (Driver) Placeholder(int) string { return "?" }

type conn struct {
	db *sql.DB
	sc *sql.Conn
	id int64
}

func (c *conn) Ping(ctx context.Context) error {
	return wrapErr(ctx, c.sc.PingContext(ctx))
}

func (c *conn) Close() error {
	return errors.Join(c.sc.Close(), c.db.Close())
}

// Databases and Schemas return the same names: a MySQL schema is a database.
func (c *conn) Databases(ctx context.Context) ([]string, error) {
	return c.Schemas(ctx)
}

func (c *conn) Schemas(ctx context.Context) ([]string, error) {
	// System schemas sort last.
	rows, err := c.sc.QueryContext(ctx, `
		SELECT schema_name FROM information_schema.schemata
		ORDER BY schema_name IN ('information_schema', 'mysql', 'performance_schema', 'sys'), schema_name`)
	if err != nil {
		return nil, wrapErr(ctx, err)
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		names = append(names, name)
	}
	return names, wrapErr(ctx, rows.Err())
}

func (c *conn) Tables(ctx context.Context, schema string) ([]driver.Table, error) {
	rows, err := c.sc.QueryContext(ctx, `
		SELECT table_name, table_type FROM information_schema.tables
		WHERE table_schema = ? ORDER BY table_name`, schema)
	if err != nil {
		return nil, wrapErr(ctx, err)
	}
	defer rows.Close()
	var tables []driver.Table
	for rows.Next() {
		var name, kind string
		if err := rows.Scan(&name, &kind); err != nil {
			return nil, err
		}
		table := driver.Table{Name: name}
		if strings.Contains(kind, "VIEW") {
			table.Kind = driver.KindView
		}
		tables = append(tables, table)
	}
	return tables, wrapErr(ctx, rows.Err())
}

func (c *conn) Describe(ctx context.Context, schema, table string) (*driver.TableInfo, error) {
	info := &driver.TableInfo{}

	rows, err := c.sc.QueryContext(ctx, `
		SELECT column_name, column_type, is_nullable = 'YES', column_default, column_key = 'PRI'
		FROM information_schema.columns
		WHERE table_schema = ? AND table_name = ? ORDER BY ordinal_position`, schema, table)
	if err != nil {
		return nil, wrapErr(ctx, err)
	}
	for rows.Next() {
		var col driver.ColumnInfo
		var def sql.NullString
		if err := rows.Scan(&col.Name, &col.Type, &col.Nullable, &def, &col.PrimaryKey); err != nil {
			rows.Close()
			return nil, err
		}
		col.Default = driver.Value{Text: def.String, Null: !def.Valid}
		info.Columns = append(info.Columns, col)
	}
	if err := closeRows(ctx, rows); err != nil {
		return nil, err
	}

	rows, err = c.sc.QueryContext(ctx, `
		SELECT index_name, NOT non_unique, index_name = 'PRIMARY',
			COALESCE(GROUP_CONCAT(COALESCE(column_name, expression) ORDER BY seq_in_index SEPARATOR ', '), '')
		FROM information_schema.statistics
		WHERE table_schema = ? AND table_name = ?
		GROUP BY index_name, non_unique ORDER BY index_name = 'PRIMARY' DESC, index_name`, schema, table)
	if err != nil {
		return nil, wrapErr(ctx, err)
	}
	for rows.Next() {
		var idx driver.Index
		if err := rows.Scan(&idx.Name, &idx.Unique, &idx.Primary, &idx.Columns); err != nil {
			rows.Close()
			return nil, err
		}
		info.Indexes = append(info.Indexes, idx)
	}
	if err := closeRows(ctx, rows); err != nil {
		return nil, err
	}

	rows, err = c.sc.QueryContext(ctx, `
		SELECT constraint_name, GROUP_CONCAT(column_name ORDER BY ordinal_position SEPARATOR ', '),
			referenced_table_schema, referenced_table_name,
			GROUP_CONCAT(referenced_column_name ORDER BY ordinal_position SEPARATOR ', ')
		FROM information_schema.key_column_usage
		WHERE table_schema = ? AND table_name = ? AND referenced_table_name IS NOT NULL
		GROUP BY constraint_name, referenced_table_schema, referenced_table_name ORDER BY constraint_name`, schema, table)
	if err != nil {
		return nil, wrapErr(ctx, err)
	}
	for rows.Next() {
		var fk driver.ForeignKey
		if err := rows.Scan(&fk.Name, &fk.Columns, &fk.RefSchema, &fk.RefTable, &fk.RefColumns); err != nil {
			rows.Close()
			return nil, err
		}
		info.ForeignKeys = append(info.ForeignKeys, fk)
	}
	if err := closeRows(ctx, rows); err != nil {
		return nil, err
	}

	// The statement is the second column for a table and for a view, whatever the columns after it.
	ddl, err := c.sc.QueryContext(ctx, "SHOW CREATE TABLE "+Driver{}.QuoteIdent(schema)+"."+Driver{}.QuoteIdent(table))
	if err != nil {
		return nil, wrapErr(ctx, err)
	}
	if cols, _ := ddl.Columns(); len(cols) >= 2 && ddl.Next() {
		dest := make([]any, len(cols))
		raw := make([]sql.RawBytes, len(cols))
		for i := range dest {
			dest[i] = &raw[i]
		}
		if err := ddl.Scan(dest...); err != nil {
			ddl.Close()
			return nil, err
		}
		info.DDL = string(raw[1]) + ";"
	}
	if err := closeRows(ctx, ddl); err != nil {
		return nil, err
	}
	return info, nil
}

func (c *conn) References(ctx context.Context, schema, table string) ([]driver.Reference, error) {
	rows, err := c.sc.QueryContext(ctx, `
		SELECT table_schema, table_name, constraint_name,
			GROUP_CONCAT(column_name ORDER BY ordinal_position SEPARATOR ', '),
			GROUP_CONCAT(referenced_column_name ORDER BY ordinal_position SEPARATOR ', ')
		FROM information_schema.key_column_usage
		WHERE referenced_table_schema = ? AND referenced_table_name = ?
		GROUP BY table_schema, table_name, constraint_name ORDER BY table_schema, table_name, constraint_name`, schema, table)
	if err != nil {
		return nil, wrapErr(ctx, err)
	}
	var refs []driver.Reference
	for rows.Next() {
		var ref driver.Reference
		if err := rows.Scan(&ref.Schema, &ref.Table, &ref.Name, &ref.Columns, &ref.RefColumns); err != nil {
			rows.Close()
			return nil, err
		}
		refs = append(refs, ref)
	}
	return refs, closeRows(ctx, rows)
}

func closeRows(ctx context.Context, rows *sql.Rows) error {
	err := rows.Err()
	rows.Close()
	return wrapErr(ctx, err)
}

// ponytail: a cancel drops the connection rather than KILL the statement as Query does; the server then rolls back.
func (c *conn) Exec(ctx context.Context, query string, args ...driver.Value) (int64, error) {
	params := make([]any, len(args))
	for i, a := range args {
		if !a.Null {
			params[i] = a.Text
		}
	}
	res, err := c.sc.ExecContext(ctx, query, params...)
	if err != nil {
		return 0, wrapErr(ctx, err)
	}
	return res.RowsAffected()
}

func (c *conn) Query(ctx context.Context, query string) (driver.Results, error) {
	r := &results{ctx: ctx, c: c, done: make(chan struct{})}
	r.run = context.WithoutCancel(ctx)
	r.watch.Go(func() {
		select {
		case <-ctx.Done():
			c.kill()
		case <-r.done:
		}
	})
	rows, err := c.sc.QueryContext(r.run, query)
	if err != nil {
		r.err = wrapErr(ctx, err)
		r.finished = true
		return r, nil
	}
	r.rows = rows
	return r, nil
}

func (c *conn) kill() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c.db.ExecContext(ctx, "KILL QUERY "+strconv.FormatInt(c.id, 10))
}

type results struct {
	ctx  context.Context
	run  context.Context
	c    *conn
	rows *sql.Rows

	// done is closed when the results are, and watch waits for the goroutine that cancels the query.
	done  chan struct{}
	watch sync.WaitGroup

	started bool
	// finished is set once every result set has been read.
	finished bool
	// status is set while the current result is that of a statement that returned no rows.
	status bool
	closed bool

	cols     []driver.Column
	binary   []bool
	raw      []sql.RawBytes
	dest     []any
	row      []driver.Value
	affected int64
	err      error
}

func (r *results) NextResultSet() bool {
	if r.closed || r.finished || r.err != nil {
		return false
	}
	r.cols, r.row, r.status, r.affected = nil, nil, false, 0

	// The driver passes over the statements that return no rows, so each result set here has columns,
	// except the first when the query's first statements return none.
	more := true
	if r.started || !r.hasColumns() {
		more = r.rows.NextResultSet()
	}
	r.started = true
	if more {
		return r.readColumns()
	}
	if err := r.rows.Err(); err != nil {
		r.err = wrapErr(r.ctx, err)
		r.finished = true
		return false
	}

	// ROW_COUNT is -1 after a statement that returned rows, whose result was read above.
	// Otherwise the last statement returned none, and its count stands for a result of its own.
	r.finished = true
	var count int64
	if err := r.c.sc.QueryRowContext(r.run, "SELECT ROW_COUNT()").Scan(&count); err != nil || count < 0 {
		return false
	}
	r.status, r.affected = true, count
	return true
}

func (r *results) hasColumns() bool {
	cols, err := r.rows.Columns()
	return err == nil && len(cols) > 0
}

func (r *results) readColumns() bool {
	types, err := r.rows.ColumnTypes()
	if err != nil {
		r.err = wrapErr(r.ctx, err)
		return false
	}
	r.cols = make([]driver.Column, len(types))
	r.binary = make([]bool, len(types))
	r.raw = make([]sql.RawBytes, len(types))
	r.dest = make([]any, len(types))
	for i, t := range types {
		name := t.DatabaseTypeName()
		kind := kindOf(name)
		r.cols[i] = driver.Column{Name: t.Name(), Type: strings.ToLower(name), Kind: kind}
		r.binary[i] = kind == driver.KindBinary
		r.dest[i] = &r.raw[i]
	}
	return true
}

func (r *results) Next() bool {
	if r.closed || r.status || r.cols == nil || r.err != nil {
		return false
	}
	if !r.rows.Next() {
		if err := r.rows.Err(); err != nil {
			r.err = wrapErr(r.ctx, err)
		}
		return false
	}
	if err := r.rows.Scan(r.dest...); err != nil {
		r.err = wrapErr(r.ctx, err)
		return false
	}
	r.row = make([]driver.Value, len(r.raw))
	for i, v := range r.raw {
		switch {
		case v == nil:
			r.row[i].Null = true
		case r.binary[i]:
			r.row[i].Text = "0x" + hex.EncodeToString(v)
		default:
			r.row[i].Text = string(v)
		}
	}
	return true
}

func (r *results) Columns() []driver.Column { return r.cols }
func (r *results) Row() []driver.Value      { return r.row }
func (r *results) RowsAffected() int64      { return r.affected }
func (r *results) Err() error               { return r.err }

func (r *results) Command() string {
	if !r.status {
		return ""
	}
	if r.affected == 1 {
		return "Query OK, 1 row affected"
	}
	return fmt.Sprintf("Query OK, %d rows affected", r.affected)
}

func (r *results) Close() error {
	if r.closed {
		return r.err
	}
	r.closed = true
	if r.rows != nil {
		// Closing reads the rows that remain, so stop the query first rather than receive them all.
		if !r.finished && r.err == nil {
			r.c.kill()
		}
		r.rows.Close()
	}
	// Some statements end without an error when killed, as SLEEP does, so the cancel is reported here.
	if r.err == nil && r.ctx.Err() != nil {
		r.err = r.ctx.Err()
	}
	close(r.done)
	// A cancel on its way must land before the next query starts, or it would stop that one.
	r.watch.Wait()
	return r.err
}

func wrapErr(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	if ctx.Err() != nil {
		return fmt.Errorf("%w: %v", ctx.Err(), err)
	}
	if me, ok := errors.AsType[*mysql.MySQLError](err); ok {
		return &driver.Error{Code: strconv.Itoa(int(me.Number)), Message: me.Message}
	}
	return err
}

// kindOf classifies a type name as the driver reports it, such as "UNSIGNED BIGINT" or "VARCHAR".
func kindOf(name string) driver.ColumnKind {
	switch {
	case name == "JSON":
		return driver.KindJSON
	case strings.Contains(name, "INT"), name == "DECIMAL", name == "FLOAT", name == "DOUBLE", name == "YEAR",
		strings.HasPrefix(name, "UNSIGNED "):
		return driver.KindNumber
	case name == "DATE", name == "DATETIME", name == "TIMESTAMP", name == "TIME":
		return driver.KindTime
	case strings.Contains(name, "BLOB"), strings.Contains(name, "BINARY"), name == "BIT", name == "GEOMETRY":
		return driver.KindBinary
	}
	return driver.KindText
}
