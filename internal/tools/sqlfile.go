package tools

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
)

type RunOptions struct {
	// Target's Database is the database the file runs in. It can be empty for MySQL, when the file chooses one itself.
	Target

	KeepGoing bool
	// SingleTransaction undoes everything the file did when it fails or is canceled. Only some clients offer it.
	SingleTransaction bool
}

// Client is a program that runs a SQL file, as the dump tools write them.
// A dump holds what only these programs read, such as the rows after COPY … FROM stdin and DELIMITER lines.
type Client struct {
	Name              string
	SingleTransaction bool
	// PerSchema says whether a schema of the tree is a database the client can run a file in.
	PerSchema bool
	command   func(o RunOptions) (args, env []string)
}

// Clients maps a connection's kind to its client.
var Clients = map[string]*Client{
	"mysql":    {Name: "mysql", PerSchema: true, command: mysqlClient},
	"postgres": {Name: "psql", SingleTransaction: true, command: psql},
}

func mysqlClient(o RunOptions) (args, env []string) {
	args = []string{"--host=" + o.Host, "--user=" + o.User, "--default-character-set=utf8mb4"}
	if o.Port != 0 {
		args = append(args, fmt.Sprintf("--port=%d", o.Port), "--protocol=TCP")
	}
	args = append(args, mysqlTLS(o.Target)...)
	if o.KeepGoing {
		args = append(args, "--force")
	}
	if o.Database != "" {
		args = append(args, o.Database)
	}
	return args, []string{"MYSQL_PWD=" + o.Password}
}

func psql(o RunOptions) (args, env []string) {
	connect, env := pgConnect(o.Target)
	args = []string{
		connect,
		"--no-password",
		// The user's own startup file could change how the file runs.
		"--no-psqlrc",
		"--quiet",
	}
	// psql carries on after an error unless told to stop.
	if !o.KeepGoing {
		args = append(args, "--set=ON_ERROR_STOP=1")
	}
	if o.SingleTransaction {
		args = append(args, "--single-transaction")
	}
	return args, env
}

// A file whose name ends in .gz is decompressed on the way.
// read, if not nil, is kept at the count of bytes read from the file so far, for another goroutine to read.
// The file is streamed, so its size does not matter to memory.
func (c *Client) Run(ctx context.Context, program, path string, o RunOptions, read *atomic.Int64) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	if read == nil {
		read = new(atomic.Int64)
	}
	var in io.Reader = &countingReader{r: f, n: read}
	if strings.HasSuffix(strings.ToLower(path), ".gz") {
		gz, err := gzip.NewReader(in)
		if err != nil {
			return fmt.Errorf("%s is not a gzip file: %w", path, err)
		}
		defer gz.Close()
		in = gz
	}

	args, env := c.command(o)
	cmd := exec.CommandContext(ctx, program, args...)
	cmd.Env = append(os.Environ(), env...)
	cmd.Stdin = in
	// A file can print a line per statement, of which only the last ones tell what went wrong.
	stderr := &tailBuffer{max: 16 << 10}
	cmd.Stderr = stderr

	err = cmd.Run()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	msg := strings.TrimSpace(stderr.String())
	switch {
	case err != nil && msg != "":
		return errors.New(msg)
	case err != nil:
		return fmt.Errorf("%s: %w", c.Name, err)
	// With KeepGoing the program ends well whatever failed, and what it printed is all that tells.
	case o.KeepGoing && msg != "":
		return errors.New(msg)
	}
	return nil
}

type countingReader struct {
	r io.Reader
	n *atomic.Int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n.Add(int64(n))
	return n, err
}

type tailBuffer struct {
	max int
	buf bytes.Buffer
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.buf.Write(p)
	if extra := t.buf.Len() - t.max; extra > 0 {
		t.buf.Next(extra)
	}
	return len(p), nil
}

func (t *tailBuffer) String() string { return t.buf.String() }
