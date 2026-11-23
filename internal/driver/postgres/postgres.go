// Package postgres implements driver.Driver for PostgreSQL.
package postgres

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgconn/ctxwatch"
	"github.com/jackc/pgx/v5/pgtype"

	"db/internal/driver"
)

type Driver struct{}

var _ driver.Driver = Driver{}

func (Driver) Open(ctx context.Context, cfg driver.Config) (driver.Conn, error) {
	port := cfg.Port
	if port == 0 {
		port = 5432
	}
	u := url.URL{
		Scheme: "postgres",
		User:   url.UserPassword(cfg.User, cfg.Password),
		Host:   net.JoinHostPort(cfg.Host, strconv.Itoa(port)),
		Path:   "/" + cfg.Database,
	}
	pc, err := pgconn.ParseConfig(u.String())
	if err != nil {
		return nil, err
	}
	// On ctx cancel, send a cancel request to stop the query instead of dropping the connection.
	// If the query has not stopped after the delay, drop the connection.
	pc.BuildContextWatcherHandler = func(c *pgconn.PgConn) ctxwatch.Handler {
		return &pgconn.CancelRequestContextWatcherHandler{Conn: c, DeadlineDelay: 5 * time.Second}
	}
	c, err := pgconn.ConnectConfig(ctx, pc)
	if err != nil {
		return nil, wrapErr(ctx, err)
	}
	return &conn{pg: c}, nil
}

func (Driver) QuoteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

type conn struct {
	pg *pgconn.PgConn
}

func (c *conn) Ping(ctx context.Context) error {
	return wrapErr(ctx, c.pg.Ping(ctx))
}

func (c *conn) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	return c.pg.Close(ctx)
}

func (c *conn) Databases(ctx context.Context) ([]string, error) {
	return c.names(ctx, `
		SELECT datname FROM pg_database
		WHERE NOT datistemplate AND datallowconn
		ORDER BY datname`)
}

func (c *conn) Schemas(ctx context.Context) ([]string, error) {
	// System schemas sort last.
	return c.names(ctx, `
		SELECT nspname FROM pg_namespace
		WHERE nspname !~ '^pg_(toast|temp_|toast_temp_)'
		ORDER BY nspname IN ('pg_catalog', 'information_schema'), nspname`)
}

func (c *conn) Tables(ctx context.Context, schema string) ([]driver.Table, error) {
	rows, err := c.read(ctx, `
		SELECT c.relname, c.relkind
		FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = $1 AND c.relkind IN ('r', 'p', 'f', 'v', 'm')
		ORDER BY c.relname`, schema)
	if err != nil {
		return nil, err
	}
	tables := make([]driver.Table, len(rows))
	for i, r := range rows {
		tables[i].Name = string(r[0])
		switch string(r[1]) {
		case "v":
			tables[i].Kind = driver.KindView
		case "m":
			tables[i].Kind = driver.KindMaterializedView
		}
	}
	return tables, nil
}

func (c *conn) names(ctx context.Context, sql string) ([]string, error) {
	rows, err := c.read(ctx, sql)
	if err != nil {
		return nil, err
	}
	names := make([]string, len(rows))
	for i, r := range rows {
		names[i] = string(r[0])
	}
	return names, nil
}

func (c *conn) read(ctx context.Context, sql string, args ...string) ([][][]byte, error) {
	params := make([][]byte, len(args))
	for i, a := range args {
		params[i] = []byte(a)
	}
	res := c.pg.ExecParams(ctx, sql, params, nil, nil, nil).Read()
	if res.Err != nil {
		return nil, wrapErr(ctx, res.Err)
	}
	return res.Rows, nil
}

func (c *conn) Query(ctx context.Context, sql string) (driver.Results, error) {
	ctx, cancel := context.WithCancel(ctx)
	return &results{ctx: ctx, cancel: cancel, mrr: c.pg.Exec(ctx, sql)}, nil
}

type results struct {
	ctx    context.Context
	cancel context.CancelFunc
	mrr    *pgconn.MultiResultReader
	rr     *pgconn.ResultReader

	cols   []driver.Column
	row    []driver.Value
	tag    pgconn.CommandTag
	err    error
	closed bool
}

func (r *results) NextResultSet() bool {
	if r.closed || r.err != nil {
		return false
	}
	r.finish()
	r.cols, r.row, r.tag = nil, nil, pgconn.CommandTag{}
	if r.err != nil {
		return false
	}
	if !r.mrr.NextResult() {
		r.Close()
		return false
	}
	r.rr = r.mrr.ResultReader()
	fds := r.rr.FieldDescriptions()
	r.cols = make([]driver.Column, len(fds))
	for i, fd := range fds {
		r.cols[i] = driver.Column{Name: fd.Name, Type: typeName(fd.DataTypeOID), Kind: kindOf(fd.DataTypeOID)}
	}
	return true
}

func (r *results) Next() bool {
	if r.rr == nil {
		return false
	}
	if !r.rr.NextRow() {
		r.finish()
		return false
	}
	raw := r.rr.Values()
	r.row = make([]driver.Value, len(raw))
	for i, v := range raw {
		if v == nil {
			r.row[i].Null = true
		} else {
			r.row[i].Text = string(v)
		}
	}
	return true
}

func (r *results) finish() {
	if r.rr == nil {
		return
	}
	tag, err := r.rr.Close()
	r.rr = nil
	r.tag = tag
	if err != nil && r.err == nil {
		r.err = wrapErr(r.ctx, err)
	}
}

func (r *results) Columns() []driver.Column { return r.cols }
func (r *results) Row() []driver.Value      { return r.row }
func (r *results) Command() string          { return r.tag.String() }
func (r *results) RowsAffected() int64      { return r.tag.RowsAffected() }
func (r *results) Err() error               { return r.err }

func (r *results) Close() error {
	if r.closed {
		return r.err
	}
	r.closed = true
	abandoned := r.rr != nil && r.err == nil
	if abandoned {
		r.cancel()
	}
	err := r.mrr.Close()
	if err != nil && r.err == nil && !abandoned {
		r.err = wrapErr(r.ctx, err)
	}
	r.rr = nil
	r.cancel()
	return r.err
}

func wrapErr(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	if ctx.Err() != nil {
		return fmt.Errorf("%w: %v", ctx.Err(), err)
	}
	var pe *pgconn.PgError
	if errors.As(err, &pe) {
		return &driver.Error{
			Code:     pe.Code,
			Message:  pe.Message,
			Detail:   pe.Detail,
			Hint:     pe.Hint,
			Position: int(pe.Position),
		}
	}
	return err
}

var typeMap = pgtype.NewMap()

func typeName(oid uint32) string {
	if t, ok := typeMap.TypeForOID(oid); ok {
		return t.Name
	}
	return "oid " + strconv.FormatUint(uint64(oid), 10)
}

func kindOf(oid uint32) driver.ColumnKind {
	switch oid {
	case pgtype.Int2OID, pgtype.Int4OID, pgtype.Int8OID, pgtype.Float4OID, pgtype.Float8OID,
		pgtype.NumericOID, pgtype.OIDOID:
		return driver.KindNumber
	case pgtype.BoolOID:
		return driver.KindBool
	case pgtype.DateOID, pgtype.TimeOID, pgtype.TimetzOID, pgtype.TimestampOID, pgtype.TimestamptzOID:
		return driver.KindTime
	case pgtype.ByteaOID:
		return driver.KindBinary
	case pgtype.JSONOID, pgtype.JSONBOID:
		return driver.KindJSON
	}
	return driver.KindText
}

