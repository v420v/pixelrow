// Package app holds the screens of the app.
package app

import (
	"context"
	"strconv"

	"github.com/egoist/mygo/ui"

	"db/internal/config"
	"db/internal/driver"
)

// App is the main window: the tree of connections beside the tabs of queries.
type App struct {
	drivers map[string]driver.Driver
	update  func(fn func())

	connections  *connections
	dumper       *dumper
	fileRunner   *fileRunner
	jobs         *jobs
	sidebarWidth float32

	tabs   []*tab
	active int
	// revealed is the id of the tab the bar last scrolled to.
	revealed int
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
	// structure is nil for a blank query.
	structure *structure
	// view is 0 for the rows and 1 for the structure.
	view int
}

// Options is what the app needs from the system around it.
type Options struct {
	// Drivers maps a connection's kind to its driver.
	Drivers    map[string]driver.Driver
	ConfigPath string
	// Update runs a function on the UI thread and redraws, as Window.Update does.
	Update   func(fn func())
	OpenFile func(path string) error
	// SaveFile asks where to save a file named name, and returns "" when the user cancels.
	SaveFile func(name string) (string, error)
	// ChooseFile asks for a file to open, and returns "" when the user cancels.
	ChooseFile func() (string, error)
	// Askpass is the path of a program that calls tools.Askpass, for ssh to get passphrases from: the app's own executable.
	Askpass string
}

func New(o Options) *App {
	a := &App{drivers: o.Drivers, update: o.Update, sidebarWidth: 260}
	a.connections = &connections{
		path:         o.ConfigPath,
		drivers:      o.Drivers,
		update:       o.Update,
		openFile:     o.OpenFile,
		askpass:      o.Askpass,
		onQuery:      a.newQuery,
		onTable:      a.openTable,
		onDisconnect: a.disconnected,
	}
	a.jobs = &jobs{update: o.Update}
	a.dumper = &dumper{connections: a.connections, update: o.Update, jobs: a.jobs, saveFile: o.SaveFile}
	a.fileRunner = &fileRunner{connections: a.connections, jobs: a.jobs, chooseFile: o.ChooseFile}
	a.connections.onDump = a.dumper.ask
	a.connections.onRunFile = a.fileRunner.ask
	a.connections.footer = a.jobs.view
	return a
}

// Close ends what would outlive the app, as it quits.
func (a *App) Close() {
	a.connections.Close()
}

func (a *App) View(c *ui.Context) {
	c.OnWindowShortcut(ui.Cmd, ui.KeyT, func() {
		if a.connections.selected != "" {
			a.newQuery(a.connections.selected)
		}
	})
	ui.Split(c, &a.sidebarWidth, func() { a.connections.view(c) }, func() { a.tabsView(c) }).Fill()
	a.dumper.dialog(c)
	a.fileRunner.dialog(c)
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
		ui.ScrollHorizontal(c).FillWidth().Shrink(0).Children(func() {
			a.tabBar(c, &closing)
		})
		// A band across the whole tab, so its connection is told at a glance and not only by reading the name.
		if conf, _ := a.connections.conf(a.tabs[a.active].conn); conf.Color != "" {
			ui.Column(c).FillWidth().Height(3).Shrink(0).Background(ui.Hex(conf.Color))
		} else {
			ui.Divider(c)
		}
		// The key gives each tab its own editor and table state.
		tb := a.tabs[a.active]
		if tb.structure != nil {
			ui.Row(c).FillWidth().Padding(t.Space(1), t.Space(2)).Gap(t.Space(1)).Children(func() {
				for view, button := range []struct {
					icon  *ui.SVG
					label string
				}{{iconRows, "Data"}, {iconFields, "Structure"}} {
					build := iconButton
					if tb.view == view {
						build = primaryIconButton
					}
					build(c, button.icon, button.label).OnClick(func() { tb.view = view })
				}
			})
			ui.Divider(c)
		}
		// Read each time, so a color or readonly changed in the config file shows after a reload.
		if conf, ok := a.connections.conf(tb.conn); ok {
			a.mark(tb.pane, conf)
		}
		c.Key([2]int{tb.id, tb.view})
		ui.Column(c).Grow(1).FillWidth().Children(func() {
			if tb.structure != nil && tb.view == 1 {
				tb.structure.view(c)
			} else {
				tb.pane.View(c)
			}
		})
	})
	if closing >= 0 {
		a.closeTab(closing)
	}
}

// tabBar builds the tabs, and sets closing to the index of one the user closes.
func (a *App) tabBar(c *ui.Context, closing *int) {
	t := c.Theme()
	tabs := ui.TabsBase(c, &a.active, len(a.tabs))
	tabs.List.Gap(t.Space(1)).Padding(t.Space(1), t.Space(2)).Children(func() {
		for i, tb := range a.tabs {
			c.Key(tb.id)
			// The tabs keep their width and the bar scrolls, rather than squeeze them until none can be read.
			item := tabs.Tab(i).Row().Shrink(0).Gap(t.Space(1)).AlignItems(ui.Center).Padding(t.Space(1), t.Space(2)).Radius(6)
			if i == a.active {
				item.Background(t.Surface)
				// A tab opened or chosen from elsewhere, as from the tree, can be out of view.
				if a.revealed != tb.id {
					a.revealed = tb.id
					item.ScrollIntoView()
				}
			}
			item.ContextMenu(func(m *ui.Menu) {
				if m.Item("Close Tab").Chosen() {
					*closing = i
				}
			})
			conf, _ := a.connections.conf(tb.conn)
			item.Children(func() {
				colorChip(c, conf)
				ui.Text(c, tb.conn).Bold().SingleLine()
				ui.Text(c, tb.title).SingleLine()
				if iconButton(c, iconClose, "Close "+tb.conn+" "+tb.title).Size(18, 18).Clicked() {
					*closing = i
				}
			})
		}
	})
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
	sql := "SELECT * FROM " + drv.QuoteIdent(schema) + "." + drv.QuoteIdent(table)
	key := schema + "\x00" + table
	for i, tb := range a.tabs {
		if tb.conn == conn && tb.table == key {
			a.active = i
			tb.pane.Run(sql)
			return
		}
	}
	tb := a.addTab(conn, key, table)
	tb.structure = &structure{
		dial:   func(ctx context.Context) (driver.Conn, error) { return a.connections.dial(ctx, conn) },
		update: a.update,
		schema: schema,
		table:  table,
	}
	tb.pane.table = &tableRef{schema: schema, table: table, sql: sql, drv: drv}
	tb.pane.SetText(sql)
	tb.pane.Run(sql)
}

func (a *App) addTab(conn, table, title string) *tab {
	a.ids++
	conf, _ := a.connections.conf(conn)
	pane := NewQueryPane(func(ctx context.Context) (driver.Conn, error) { return a.connections.dial(ctx, conn) }, a.update)
	pane.Connection = conn
	a.mark(pane, conf)
	tb := &tab{id: a.ids, conn: conn, table: table, title: title, pane: pane}
	a.tabs = append(a.tabs, tb)
	a.active = len(a.tabs) - 1
	return tb
}

func (a *App) mark(pane *QueryPane, conf config.Connection) {
	pane.Address, pane.ReadOnly = address(conf), conf.ReadOnly
	pane.Color = ui.Color{}
	if conf.Color != "" {
		pane.Color = ui.Hex(conf.Color)
	}
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
