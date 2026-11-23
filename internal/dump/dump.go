// Package dump writes a database to a SQL file with the database's own dump tool: mysqldump or pg_dump.
package dump

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
)

// Content is which part of the tables a dump holds.
type Content int

const (
	StructureAndData Content = iota
	StructureOnly
	DataOnly
)

type Options struct {
	Host     string
	Port     int
	User     string
	Password string
	// Database is the database to connect to. PostgreSQL needs it, and MySQL takes Schema instead.
	Database string

	// Schema is the schema to dump: for MySQL, a database.
	Schema string
	// Tables limits the dump to these tables of Schema. Empty dumps all of it.
	Tables  []string
	Content Content
	// Drop adds statements that drop each object before creating it, so the dump replaces what a database has.
	Drop bool
	// Extras turns the tool's own options on, by the Key of the Extra.
	Extras map[string]bool
}

// Extra is an option of one tool, offered as a check box.
type Extra struct {
	Key     string
	Label   string
	Default bool
	// WholeSchema marks an option that only applies when all of a schema is dumped.
	WholeSchema bool
}

type Tool struct {
	// Name is the program's file name, such as "pg_dump".
	Name   string
	Extras []Extra
	// command returns the arguments and the environment for o. The password goes in the environment, never in the arguments, which other users of the machine can list.
	command func(o Options) (args, env []string)
}

// Tools maps a connection's kind to its tool.
var Tools = map[string]*Tool{
	"mysql":    {Name: "mysqldump", Extras: mysqlExtras, command: mysqldump},
	"postgres": {Name: "pg_dump", Extras: pgExtras, command: pgDump},
}

var mysqlExtras = []Extra{
	{Key: "routines", Label: "Stored procedures, functions and events", Default: true, WholeSchema: true},
	{Key: "triggers", Label: "Triggers", Default: true},
}

func mysqldump(o Options) (args, env []string) {
	args = []string{
		"--host=" + o.Host, "--user=" + o.User,
		// One transaction gives a consistent copy of InnoDB tables without locking them.
		"--single-transaction",
		"--default-character-set=utf8mb4",
	}
	if o.Port != 0 {
		// A port only counts over TCP, and "localhost" alone means the socket file.
		args = append(args, fmt.Sprintf("--port=%d", o.Port), "--protocol=TCP")
	}
	switch o.Content {
	case StructureOnly:
		args = append(args, "--no-data")
	case DataOnly:
		args = append(args, "--no-create-info")
	}
	if !o.Drop {
		args = append(args, "--skip-add-drop-table")
	}
	if o.Extras["routines"] && len(o.Tables) == 0 {
		args = append(args, "--routines", "--events")
	}
	if !o.Extras["triggers"] {
		args = append(args, "--skip-triggers")
	}
	args = append(args, o.Schema)
	args = append(args, o.Tables...)
	return args, []string{"MYSQL_PWD=" + o.Password}
}

var pgExtras = []Extra{
	{Key: "owner", Label: "Owners and privileges"},
}

func pgDump(o Options) (args, env []string) {
	args = []string{
		"--host=" + o.Host, "--username=" + o.User, "--dbname=" + o.Database,
		// Without a terminal nobody could answer a password prompt.
		"--no-password",
	}
	if o.Port != 0 {
		args = append(args, fmt.Sprintf("--port=%d", o.Port))
	}
	switch o.Content {
	case StructureOnly:
		args = append(args, "--schema-only")
	case DataOnly:
		args = append(args, "--data-only")
	}
	if o.Drop {
		args = append(args, "--clean", "--if-exists")
	}
	if !o.Extras["owner"] {
		args = append(args, "--no-owner", "--no-privileges")
	}
	// The names are patterns to pg_dump, and quotes make it take them as they are.
	if len(o.Tables) == 0 {
		args = append(args, "--schema="+pgName(o.Schema))
	}
	for _, table := range o.Tables {
		args = append(args, "--table="+pgName(o.Schema)+"."+pgName(table))
	}
	return args, []string{"PGPASSWORD=" + o.Password}
}

func pgName(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

// A desktop app does not get the PATH of a shell, which is why PATH alone is not enough.
func Find(name, configured string) (string, error) {
	if configured != "" {
		if _, err := os.Stat(configured); err != nil {
			return "", fmt.Errorf("%s is not at %s", name, configured)
		}
		return configured, nil
	}
	if path, err := exec.LookPath(name); err == nil {
		return path, nil
	}
	home, _ := os.UserHomeDir()
	for _, dir := range []string{
		"/opt/homebrew/bin", "/usr/local/bin",
		"/opt/homebrew/opt/libpq/bin", "/usr/local/opt/libpq/bin",
		"/opt/homebrew/opt/mysql-client/bin", "/usr/local/opt/mysql-client/bin",
		"/Applications/Postgres.app/Contents/Versions/latest/bin",
		"/usr/local/mysql/bin",
		"/etc/profiles/per-user/" + os.Getenv("USER") + "/bin", "/run/current-system/sw/bin",
		filepath.Join(home, ".nix-profile", "bin"),
	} {
		path := filepath.Join(dir, name)
		if info, err := os.Stat(path); err == nil && !info.IsDir() {
			return path, nil
		}
	}
	return "", fmt.Errorf("%s was not found", name)
}

// written, if not nil, is kept at the count of bytes written so far, for another goroutine to read.
// A dump that fails or is canceled leaves no file.
func (t *Tool) Run(ctx context.Context, program, path string, o Options, written *atomic.Int64) (int64, error) {
	// The file takes its name only once it is whole, so a dump that failed is never mistaken for one that worked.
	part := path + ".part"
	f, err := os.OpenFile(part, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return 0, err
	}
	n, err := t.run(ctx, program, f, o, written)
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(part, path)
	}
	if err != nil {
		os.Remove(part)
	}
	return n, err
}

func (t *Tool) run(ctx context.Context, program string, out io.Writer, o Options, written *atomic.Int64) (int64, error) {
	args, env := t.command(o)
	cmd := exec.CommandContext(ctx, program, args...)
	cmd.Env = append(os.Environ(), env...)
	counter := &countingWriter{w: out, n: written}
	if counter.n == nil {
		counter.n = new(atomic.Int64)
	}
	var stderr bytes.Buffer
	cmd.Stdout = counter
	cmd.Stderr = &stderr

	err := cmd.Run()
	if ctx.Err() != nil {
		return counter.n.Load(), ctx.Err()
	}
	if err != nil {
		// What the tool printed says why, where the exit status only says that it failed.
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return counter.n.Load(), errors.New(msg)
		}
		return counter.n.Load(), fmt.Errorf("%s: %w", t.Name, err)
	}
	return counter.n.Load(), nil
}

type countingWriter struct {
	w io.Writer
	n *atomic.Int64
}

func (c *countingWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n.Add(int64(n))
	return n, err
}
