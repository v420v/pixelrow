// Package app holds the screens of the app.
package app

import (
	"context"
	"fmt"
	"strconv"

	"github.com/egoist/mygo/ui"

	"db/internal/driver"
)

// App is the main window: the tree of connections beside the tabs of queries.
type App struct {
	drivers map[string]driver.Driver
	update  func(fn func())

	connections  *connections
	sidebarWidth float32

	tabs []*tab
	active int
	// ids counts the tabs opened, and queries those opened as blank queries, to tell them apart.
	ids     int
	queries int
}

// tab is one query pane, with a database session of its own on one connection.
type tab struct {
	id   int
	conn string
	// table is the key of the table the tab was opened for, or "" for a blank query.
	table string
	title string
	pane  *QueryPane
}

func New(drivers map[string]driver.Driver, configPath string, update func(fn func()), openFile func(path string) error) *App {
	a := &App{drivers: drivers, update: update, sidebarWidth: 260}
	a.connections = &connections{
		path:         configPath,
		drivers:      drivers,
		update:       update,
		openFile:     openFile,
		onQuery:      a.newQuery,
		onTable:      a.openTable,
		onDisconnect: a.disconnected,
	}
	return a
}

func (a *App) View(c *ui.Context) {
	c.OnWindowShortcut(ui.Cmd, ui.KeyT, func() {
		if a.connections.selected != "" {
			a.newQuery(a.connections.selected)
		}
	})
	ui.Split(c, &a.sidebarWidth, func() { a.connections.view(c) }, func() { a.tabsView(c) }).Fill()
}

func (a *App) tabsView(c *ui.Context) {
	t := c.Theme()
	if len(a.tabs) == 0 {
		ui.Column(c).Fill().Center().Gap(t.Space(2)).Children(func() {
			ui.Text(c, "No query open").FontSize(18).Bold()
			ui.Text(c, "Choose a table on the left, or right-click a connection and choose New Query.").TextColor(t.TextMuted)
		})
		return
	}
	a.active = min(a.active, len(a.tabs)-1)

	closing := -1
	ui.Column(c).Fill().Children(func() {
		tabs := ui.TabsBase(c, &a.active, len(a.tabs))
		tabs.List.FillWidth().Gap(t.Space(1)).Padding(t.Space(1), t.Space(2)).Children(func() {
			for i, tb := range a.tabs {
				c.Key(tb.id)
				item := tabs.Tab(i).Row().Gap(t.Space(1)).AlignItems(ui.Center).Padding(t.Space(1), t.Space(2)).Radius(6)
				if i == a.active {
					item.Background(t.Surface)
				}
				item.ContextMenu(func(m *ui.Menu) {
					if m.Item("Close Tab").Chosen() {
						closing = i
					}
				})
				item.Children(func() {
					ui.Text(c, tb.conn).Bold().SingleLine()
					ui.Text(c, tb.title).SingleLine()
					if iconButton(c, iconClose, "Close "+tb.conn+" "+tb.title).Size(18, 18).Clicked() {
						closing = i
					}
				})
			}
		})
		ui.Divider(c)
		// The key gives each tab its own editor and table state.
		tb := a.tabs[a.active]
		c.Key(tb.id)
		ui.Column(c).Grow(1).FillWidth().Children(func() { tb.pane.View(c) })
	})
	if closing >= 0 {
		a.closeTab(closing)
	}
}

func (a *App) newQuery(conn string) {
	a.queries++
	a.addTab(conn, "", "Query "+strconv.Itoa(a.queries))
}

func (a *App) openTable(conn, schema, table string) {
	conf, ok := a.connections.conf(conn)
	if !ok {
		return
	}
	drv := a.drivers[conf.Kind]
	if drv == nil {
		return
	}
	sql := fmt.Sprintf("SELECT * FROM %s.%s LIMIT %d", drv.QuoteIdent(schema), drv.QuoteIdent(table), maxRows)
	key := schema + "\x00" + table
	for i, tb := range a.tabs {
		if tb.conn == conn && tb.table == key {
			a.active = i
			tb.pane.Run(sql)
			return
		}
	}
	tb := a.addTab(conn, key, table)
	tb.pane.SetText(sql)
	tb.pane.Run(sql)
}

func (a *App) addTab(conn, table, title string) *tab {
	a.ids++
	conf, _ := a.connections.conf(conn)
	pane := NewQueryPane(func(ctx context.Context) (driver.Conn, error) { return a.connections.dial(ctx, conn) }, a.update)
	pane.Connection, pane.Address = conn, address(conf)
	tb := &tab{id: a.ids, conn: conn, table: table, title: title, pane: pane}
	a.tabs = append(a.tabs, tb)
	a.active = len(a.tabs) - 1
	return tb
}

func (a *App) closeTab(i int) {
	a.tabs[i].pane.Close()
	a.tabs = append(a.tabs[:i], a.tabs[i+1:]...)
	if a.active > i || a.active >= len(a.tabs) {
		a.active = max(a.active-1, 0)
	}
}

// disconnected closes the sessions of the tabs on the connection named conn.
// The tabs stay, with their text, and connect again when they next run.
func (a *App) disconnected(conn string) {
	for _, tb := range a.tabs {
		if tb.conn == conn {
			tb.pane.Close()
		}
	}
}

