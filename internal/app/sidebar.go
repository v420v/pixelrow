package app

import (
	"context"
	"sync"
	"time"

	"github.com/egoist/mygo/ui"

	"db/internal/driver"
)

// sidebar is a tree of the connected database's schemas and their tables.
type sidebar struct {
	open    func(ctx context.Context) (driver.Conn, error)
	update  func(fn func())
	onTable func(schema, table string)

	started  bool
	loading  bool
	err      error
	schemas  []*schemaNode
	selected string

	mu     sync.Mutex
	conn   driver.Conn
	closed bool
}

type schemaNode struct {
	name    string
	open    bool
	loading bool
	loaded  bool
	err     error
	tables  []driver.Table
}

func (s *sidebar) view(c *ui.Context) {
	t := c.Theme()
	if !s.started {
		s.started = true
		s.loadSchemas()
	}

	ui.Column(c).Grow(1).FillWidth().Children(func() {
		ui.Row(c).FillWidth().Padding(t.Space(2)).Gap(t.Space(2)).AlignItems(ui.Center).Children(func() {
			ui.Text(c, "Schemas").Bold().SingleLine()
			ui.Spacer(c)
			if s.loading {
				ui.Spinner(c)
			}
			ui.Button(c, "Refresh").Disabled(s.loading).OnClick(s.loadSchemas)
		})
		ui.Divider(c)
		if s.err != nil {
			ui.Text(c, errorText(s.err)).TextColor(t.Danger).Padding(t.Space(2)).Selectable()
			return
		}
		ui.Scroll(c).Grow(1).FillWidth().Children(func() {
			ui.Tree(c, func() {
				for _, n := range s.schemas {
					s.schemaItem(c, n)
				}
			})
		})
	})
}

func (s *sidebar) schemaItem(c *ui.Context, n *schemaNode) {
	t := c.Theme()
	if n.open && !n.loaded && !n.loading && n.err == nil {
		s.loadTables(n)
	}
	schema := ui.TreeItem(c, n.name, &n.open, func() {
		switch {
		case n.err != nil:
			ui.Text(c, errorText(n.err)).TextColor(t.Danger).Padding(t.Space(1), t.Space(2)).Selectable()
		case len(n.tables) > 0:
			// Keep displaying known tables while refreshing.
		case !n.loaded:
			ui.TreeItem(c, "Loading…", nil, nil).Disabled(true)
		default:
			ui.TreeItem(c, "No tables", nil, nil).Disabled(true)
		}
		for _, table := range n.tables {
			key := n.name + "\x00" + table.Name
			item := ui.TreeItem(c, table.Name, nil, nil).Selected(s.selected == key)
			switch table.Kind {
			case driver.KindView:
				item.Tooltip("View")
			case driver.KindMaterializedView:
				item.Tooltip("Materialized view")
			}
			if item.Clicked() {
				s.selected = key
				s.onTable(n.name, table.Name)
			}
		}
	})
	// A click on the name opens the schema, not only its arrow.
	if schema.Clicked() {
		n.open = !n.open
	}
}

// loadSchemas refreshes the schema list while preserving their expanded state.
func (s *sidebar) loadSchemas() {
	if s.loading {
		return
	}
	s.loading = true
	go func() {
		var names []string
		err := s.withConn(func(ctx context.Context, conn driver.Conn) (err error) {
			names, err = conn.Schemas(ctx)
			return err
		})
		s.update(func() {
			s.loading, s.err = false, err
			if err == nil {
				s.setSchemas(names)
			}
		})
	}()
}

func (s *sidebar) setSchemas(names []string) {
	first := s.schemas == nil
	old := map[string]*schemaNode{}
	for _, n := range s.schemas {
		old[n.name] = n
	}
	s.schemas = make([]*schemaNode, len(names))
	for i, name := range names {
		n := old[name]
		if n == nil {
			n = &schemaNode{name: name}
		}
		n.loaded, n.err = false, nil
		s.schemas[i] = n
	}
	if first && len(s.schemas) > 0 {
		s.schemas[0].open = true
	}
}

func (s *sidebar) loadTables(n *schemaNode) {
	n.loading = true
	go func() {
		var tables []driver.Table
		err := s.withConn(func(ctx context.Context, conn driver.Conn) (err error) {
			tables, err = conn.Tables(ctx, n.name)
			return err
		})
		s.update(func() {
			n.loading, n.loaded, n.err, n.tables = false, err == nil, err, tables
		})
	}()
}

// withConn runs fn with the sidebar's connection, opening it if needed.
func (s *sidebar) withConn(fn func(ctx context.Context, conn driver.Conn) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if s.closed {
		return context.Canceled
	}
	if s.conn == nil {
		conn, err := s.open(ctx)
		if err != nil {
			return err
		}
		s.conn = conn
	}
	err := fn(ctx, s.conn)
	if err != nil && s.conn.Ping(ctx) != nil {
		s.conn.Close()
		s.conn = nil
	}
	return err
}

// close waits for any ongoing load to finish before closing the connection.
func (s *sidebar) close() {
	go func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.closed = true
		if s.conn != nil {
			s.conn.Close()
			s.conn = nil
		}
	}()
}

