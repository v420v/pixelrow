// Package config reads the connections from the app's config file.
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
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
	// SSH is the SSH server to reach Host through. Its zero value connects directly.
	SSH SSH `toml:"ssh"`
}

// SSH is the server an SSH tunnel goes through: the ssh table of a connection, or a string that is its host alone.
type SSH struct {
	// Host is the server, as the ssh command takes it: a name, an address, or a Host of ~/.ssh/config.
	Host string
	User string
	// Port is 0 for the port ssh would use by itself.
	Port int
	// Key is the path of the private key to sign in with, or "" for the keys ssh would try by itself.
	Key string
	// Passphrase unlocks the key, and Password signs in to the server. Either can be empty.
	Passphrase string
	Password   string
}

func (s SSH) Destination() string {
	if s.User != "" {
		return s.User + "@" + s.Host
	}
	return s.Host
}

func (s *SSH) UnmarshalTOML(v any) error {
	switch v := v.(type) {
	case string:
		s.Host = v
	case map[string]any:
		for key, value := range v {
			text, isText := value.(string)
			number, isNumber := value.(int64)
			switch {
			case key == "host" && isText:
				s.Host = text
			case key == "user" && isText:
				s.User = text
			case key == "key" && isText:
				s.Key = text
			case key == "passphrase" && isText:
				s.Passphrase = text
			case key == "password" && isText:
				s.Password = text
			case key == "port" && isNumber:
				s.Port = int(number)
			// The decoder cannot report what this method passes over, so a misspelled key is caught here.
			case key == "host" || key == "user" || key == "key" || key == "port" || key == "passphrase" || key == "password":
				return fmt.Errorf("ssh.%s has the wrong type", key)
			default:
				return fmt.Errorf("unknown key %q in ssh", key)
			}
		}
		if s.Host == "" {
			return errors.New("ssh has no host")
		}
	default:
		return errors.New("ssh must be a string or a table")
	}
	if s.Port < 0 || s.Port > 65535 {
		return fmt.Errorf("ssh has an invalid port %d", s.Port)
	}
	return nil
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

# A database that only an SSH server reaches: host is the database as that server sees it.
# The server's host key must already be known: connect to it once with ssh in a terminal.
# [connections.production]
# host = "db.internal"
# user = "app"
# password = "secret"
# database = "app"
#
# [connections.production.ssh]
# host = "bastion.example.com"
# user = "me"
# port = 22
# key = "~/.ssh/id_ed25519"
# passphrase = "the passphrase of the key"
# password = "the password of the server, where it asks for one"

# Where the programs that dump and that run SQL files are, when the app does not find them by itself.
# [tools]
# mysqldump = "/opt/homebrew/opt/mysql-client/bin/mysqldump"
# mysql = "/opt/homebrew/opt/mysql-client/bin/mysql"
# pg_dump = "/opt/homebrew/opt/libpq/bin/pg_dump"
# psql = "/opt/homebrew/opt/libpq/bin/psql"
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

type File struct {
	// Connections are in the order the file lists them.
	Connections []Connection
	// Tools maps the name of a program the app runs, such as "pg_dump", to its path.
	Tools map[string]string
}

// knownTools are the programs a [tools] table can place.
var knownTools = []string{"mysqldump", "pg_dump", "mysql", "psql", "ssh"}

// A missing file is an empty one.
func Load(path string) (File, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return File{}, nil
	}
	if err != nil {
		return File{}, err
	}
	var file struct {
		Connections map[string]Connection `toml:"connections"`
		Tools       map[string]string     `toml:"tools"`
	}
	md, err := toml.Decode(string(data), &file)
	if err != nil {
		if pe, ok := errors.AsType[toml.ParseError](err); ok {
			return File{}, fmt.Errorf("line %d: %s", pe.Position.Line, pe.Message)
		}
		return File{}, err
	}
	if unknown := md.Undecoded(); len(unknown) > 0 {
		return File{}, fmt.Errorf("unknown key %q", unknown[0].String())
	}
	for name := range file.Tools {
		if !slices.Contains(knownTools, name) {
			return File{}, fmt.Errorf("unknown tool %q: [tools] takes %s", name, strings.Join(knownTools, ", "))
		}
	}

	out := File{Tools: file.Tools}
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
			return File{}, fmt.Errorf("connection %q has no host", conn.Name)
		}
		if conn.Port < 0 || conn.Port > 65535 {
			return File{}, fmt.Errorf("connection %q has an invalid port %d", conn.Name, conn.Port)
		}
		out.Connections = append(out.Connections, conn)
	}
	return out, nil
}
