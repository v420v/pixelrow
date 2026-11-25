package app

import (
	"context"
	"sync"
	"time"

	"github.com/egoist/mygo/ui"

	"db/internal/driver"
)

// browser loads the schemas and tables of one connection and builds their tree items.
// It has a connection of its own, so the tree can load while queries run.
type browser struct {
	open   func(ctx context.Context) (driver.Conn, error)
	update func(fn func())

	loading bool
	// loaded is set once the schemas have been read, which also means the connection works.
	loaded  bool
	err     error
	schemas []*schemaNode

	mu     sync.Mutex
	conn   driver.Conn
	closed bool
}

// treeActions is what the items of one connection do.
type treeActions struct {
	openTable func(schema, table string)
	// dump asks to dump table of schema, or the whole schema when table is "".
	dump func(schema, table string)
	// runFile asks to run a SQL file in schema. It is nil where a schema is not something a file can be aimed at.
	runFile func(schema string)
}

type schemaNode struct {
	name    string
	open    bool
	loading bool
	loaded  bool
	err     error
	tables  []driver.Table
}

// items builds the tree items of the schemas, inside the item of their connection.
// selected is the key of the table chosen in the whole tree, and prefix what the keys of this connection start with.
func (b *browser) items(c *ui.Context, selected *string, prefix string, actions treeActions) {
	t := c.Theme()
	switch {
	case b.err != nil:
		ui.Text(c, errorText(b.err)).TextColor(t.Danger).Padding(t.Space(1), t.Space(2)).Selectable()
	case len(b.schemas) > 0:
		// Keep displaying known schemas while refreshing.
	case !b.loaded:
		ui.TreeItem(c, "Connecting…", nil, nil).Disabled(true)
	default:
		ui.TreeItem(c, "No schemas", nil, nil).Disabled(true)
	}
	for _, n := range b.schemas {
		b.schemaItem(c, n, selected, prefix, actions)
	}
}

func (b *browser) schemaItem(c *ui.Context, n *schemaNode, selected *string, prefix string, actions treeActions) {
	t := c.Theme()
	if n.open && !n.loaded && !n.loading && n.err == nil {
		b.loadTables(n)
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
			key := prefix + "\x00" + n.name + "\x00" + table.Name
			item := ui.TreeItem(c, table.Name, nil, nil)
			chosenLook(c, item, *selected == key)
			switch table.Kind {
			case driver.KindView:
				item.Tooltip("View")
			case driver.KindMaterializedView:
				item.Tooltip("Materialized view")
			}
			open := item.Clicked()
			item.ContextMenu(func(m *ui.Menu) {
				if m.Item("Open").Chosen() {
					open = true
				}
				if m.Item("Dump Table…").Chosen() {
					actions.dump(n.name, table.Name)
				}
			})
			if open {
				*selected = key
				actions.openTable(n.name, table.Name)
			}
		}
	})
	schema.ContextMenu(func(m *ui.Menu) {
		if m.Item("Dump Schema…").Chosen() {
			actions.dump(n.name, "")
		}
		if actions.runFile != nil && m.Item("Run SQL File…").Chosen() {
			actions.runFile(n.name)
		}
	})
	// A click on the name opens the schema, not only its arrow.
	if schema.Clicked() {
		n.open = !n.open
	}
}

// load reads the schema list, connecting first if needed, while preserving which schemas are open.
func (b *browser) load() {
	if b.loading {
		return
	}
	b.loading = true
	go func() {
		var names []string
		err := b.withConn(func(ctx context.Context, conn driver.Conn) (err error) {
			names, err = conn.Schemas(ctx)
			return err
		})
		b.update(func() {
			b.loading, b.err = false, err
			if err == nil {
				b.setSchemas(names)
			}
		})
	}()
}

func (b *browser) setSchemas(names []string) {
	first := !b.loaded
	b.loaded = true
	old := map[string]*schemaNode{}
	for _, n := range b.schemas {
		old[n.name] = n
	}
	b.schemas = make([]*schemaNode, len(names))
	for i, name := range names {
		n := old[name]
		if n == nil {
			n = &schemaNode{name: name}
		}
		n.loaded, n.err = false, nil
		b.schemas[i] = n
	}
	if first && len(b.schemas) > 0 {
		b.schemas[0].open = true
	}
}

func (b *browser) loadTables(n *schemaNode) {
	n.loading = true
	go func() {
		var tables []driver.Table
		err := b.withConn(func(ctx context.Context, conn driver.Conn) (err error) {
			tables, err = conn.Tables(ctx, n.name)
			return err
		})
		b.update(func() {
			n.loading, n.loaded, n.err, n.tables = false, err == nil, err, tables
		})
	}()
}

func (b *browser) withConn(fn func(ctx context.Context, conn driver.Conn) error) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if b.closed {
		return context.Canceled
	}
	if b.conn == nil {
		conn, err := b.open(ctx)
		if err != nil {
			return err
		}
		b.conn = conn
	}
	err := fn(ctx, b.conn)
	if err != nil && b.conn.Ping(ctx) != nil {
		b.conn.Close()
		b.conn = nil
	}
	return err
}

// close waits for any ongoing load to finish before closing the connection. The browser is not used again.
func (b *browser) close() {
	go func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		b.closed = true
		if b.conn != nil {
			b.conn.Close()
			b.conn = nil
		}
	}()
}
