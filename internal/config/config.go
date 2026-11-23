// Package config reads the connections from the app's config file.
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
)

// Connection is one [connections.<name>] table of the config file.
type Connection struct {
	Name     string `toml:"-"`
	Kind     string `toml:"kind"`
	Host     string `toml:"host"`
	Port     int    `toml:"port"`
	User     string `toml:"user"`
	Password string `toml:"password"`
	Database string `toml:"database"`
}

// DefaultKind is the kind of a connection that names none.
const DefaultKind = "postgres"

// template is written when there is no config file yet.
const template = `# Connections of the db client.
# Each [connections.<name>] table is one connection. Quote names with spaces: [connections."My DB"].
# This file holds passwords in plain text, so keep it readable by you alone.

# [connections.local]
# kind = "postgres"
# host = "127.0.0.1"
# port = 5432
# user = "postgres"
# password = "postgres"
# database = "postgres"
`

func Path() (string, error) {
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "db", "config.toml"), nil
}

// Create writes a commented example to path unless a file is already there.
func Create(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, fs.ErrExist) {
		return nil
	}
	if err != nil {
		return err
	}
	_, err = f.WriteString(template)
	return errors.Join(err, f.Close())
}

// Load returns the connections of the file at path, in the order it lists them.
func Load(path string) ([]Connection, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var file struct {
		Connections map[string]Connection `toml:"connections"`
	}
	md, err := toml.Decode(string(data), &file)
	if err != nil {
		var pe toml.ParseError
		if errors.As(err, &pe) {
			return nil, fmt.Errorf("line %d: %s", pe.Position.Line, pe.Message)
		}
		return nil, err
	}
	if unknown := md.Undecoded(); len(unknown) > 0 {
		return nil, fmt.Errorf("unknown key %q", unknown[0].String())
	}

	var conns []Connection
	for _, key := range md.Keys() {
		if len(key) != 2 || key[0] != "connections" {
			continue
		}
		conn := file.Connections[key[1]]
		conn.Name = key[1]
		if conn.Kind == "" {
			conn.Kind = DefaultKind
		}
		if strings.TrimSpace(conn.Host) == "" {
			return nil, fmt.Errorf("connection %q has no host", conn.Name)
		}
		if conn.Port < 0 || conn.Port > 65535 {
			return nil, fmt.Errorf("connection %q has an invalid port %d", conn.Name, conn.Port)
		}
		conns = append(conns, conn)
	}
	return conns, nil
}
