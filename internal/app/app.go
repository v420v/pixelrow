// Package app holds the screens of the app.
package app

import (
	"context"
	"slices"
	"strconv"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"

	"github.com/v420v/pixelrow/internal/config"
	"github.com/v420v/pixelrow/internal/driver"
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

	// catalogs holds the tables known for completion, by connection.
	catalogs map[string]*catalog

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
	// structure and relations are nil for a blank query.
	structure *structure
	relations *relations
	// diagram is set for the tab of a schema's diagram, which shows nothing else.
	diagram *erDiagram
	// view is 0 for the rows, 1 for the structure and 2 for the relations.
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
	a.connections.onDiagram = a.openDiagram
	a.connections.onRefresh = func(conn string) { delete(a.catalogs, conn) }
	a.connections.onRunFile = a.fileRunner.ask
	a.connections.footer = a.jobs.view
	return a
}

// Close ends what would outlive the app, as it quits.
func (a *App) Close() {
	a.connections.Close()
}

func (a *App) View(c *ui.Context) {
	useTheme(c)
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
		ui.Column(c).Fill().Center().Gap(t.Space(1.5)).Children(func() {
			ui.Text(c, "No query open").FontSize(15).FontWeight(600)
			ui.Text(c, "Choose a table on the left, or right-click a connection and choose New Query.").FontSize(smallText).TextColor(t.TextMuted)
		})
		return
	}
	a.active = min(a.active, len(a.tabs)-1)

	act := tabAction{close: -1, from: -1}
	ui.Column(c).Fill().Children(func() {
		bar := ui.ScrollHorizontal(c).FillWidth().Shrink(0).Background(t.Surface)
		bar.Children(func() {
			a.tabBar(c, &act)
		})
		// Let go past the last tab, a dragged tab goes to the end.
		if id, ok := ui.Drop[draggedTab](bar); ok {
			act.from, act.to = a.tabIndex(int(id)), len(a.tabs)-1
		}
		hairline(c)
		// The key gives each tab its own editor and table state.
		tb := a.tabs[a.active]
		// Read each time, so a color or readonly changed in the config file shows after a reload.
		if conf, ok := a.connections.conf(tb.conn); ok {
			a.mark(tb.pane, conf)
		}
		c.Key([2]int{tb.id, tb.view})
		ui.Column(c).Grow(1).FillWidth().Children(func() {
			switch {
			case tb.diagram != nil:
				tb.diagram.view(c)
			case tb.structure != nil && tb.view == 1:
				tb.structure.view(c)
			case tb.relations != nil && tb.view == 2:
				tb.relations.view(c)
			default:
				tb.pane.View(c)
			}
		})
	})
	switch {
	case act.close >= 0:
		a.closeTab(act.close)
	case act.from >= 0 && act.from != act.to:
		a.moveTab(act.from, act.to)
	}
}

// tabAction is what the user did to the tabs while they were built, to apply once the loop over them is done.
type tabAction struct {
	// close is the index of the tab to close, or -1.
	close int
	// from and to are where a dragged tab was and where it goes, and from is -1 when none was dropped.
	from, to int
}

// draggedTab is the id of the tab a drag carries.
type draggedTab int

func (a *App) tabBar(c *ui.Context, act *tabAction) {
	t := c.Theme()
	tabs := ui.TabsBase(c, &a.active, len(a.tabs))
	tabs.List.Children(func() {
		for i, tb := range a.tabs {
			c.Key(tb.id)
			// The tabs keep their width and the bar scrolls, rather than squeeze them until none can be read.
			item := tabs.Tab(i).Row().Shrink(0).Height(barHeight).Gap(t.Space(1.5)).AlignItems(ui.Center).Padding(0, t.Space(2), 0, t.Space(3)).
				BorderWidth(0, 1, 0, 0).BorderColor(t.Border)
			// The tab shown is cut out of the bar, on the background of what it shows, and the others stay back.
			name, title := t.TextMuted, t.TextMuted
			switch {
			case i == a.active:
				name, title = t.Text, t.Text
				item.Background(t.Background)
			case item.Hovered():
				item.Background(t.SurfaceHover)
			}
			if i == a.active {
				// A tab opened or chosen from elsewhere, as from the tree, can be out of view.
				if a.revealed != tb.id {
					a.revealed = tb.id
					item.ScrollIntoView()
				}
			}
			item.ContextMenu(func(m *ui.Menu) {
				if m.Item("Close Tab").Chosen() {
					act.close = i
				}
			})
			item.Drag(draggedTab(tb.id))
			if item.Dragging() {
				item.Opacity(0.4)
			}
			// A line on the edge the dragged tab would land beside: it takes this tab's place, pushing it the other way.
			if id, over := ui.DragOver[draggedTab](item); over && int(id) != tb.id {
				edge := float32(2)
				if a.tabIndex(int(id)) < i {
					edge = -2
				}
				item.InsetShadow(edge, 0, 0, 0, t.Accent)
			}
			if id, ok := ui.Drop[draggedTab](item); ok {
				act.from, act.to = a.tabIndex(int(id)), i
			}
			// The connection's color lines the top edge of its tabs, in full on the one shown.
			if conf, _ := a.connections.conf(tb.conn); conf.Color != "" {
				line := ui.Hex(conf.Color)
				if i != a.active {
					line = line.Alpha(0.45)
				}
				item.InsetShadow(0, 2, 0, 0, line)
			}
			item.Children(func() {
				ui.Text(c, tb.conn).FontSize(smallText).FontWeight(600).TextColor(name).SingleLine()
				ui.Text(c, tb.title).FontSize(smallText).TextColor(title).SingleLine()
				if iconButton(c, iconClose, "Close "+tb.conn+" "+tb.title).Size(18, 18).Clicked() {
					act.close = i
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
	tb.relations = &relations{
		dial:   tb.structure.dial,
		update: a.update,
		schema: schema,
		table:  table,
		// A table of the diagram opens on its own relations, so that the user can walk from table to table.
		open: func(schema, table string) {
			a.openTable(conn, schema, table)
			a.tabs[a.active].view = 2
		},
	}
	// Each view of the table ends its bar with the switch to the others.
	views := func(c *ui.Context) {
		for view, button := range []struct {
			icon  *ui.SVG
			label string
		}{{iconRows, "Data"}, {iconFields, "Structure"}, {iconLinks, "Relations"}} {
			toggleButton(c, button.icon, button.label, tb.view == view).OnClick(func() { tb.view = view })
		}
	}
	tb.pane.Trailing, tb.structure.trailing, tb.relations.trailing = views, views, views
	tb.pane.table = &tableRef{schema: schema, table: table, sql: sql, drv: drv}
	tb.pane.SetText(sql)
	tb.pane.Run(sql)
}

func (a *App) openDiagram(conn, schema string) {
	// No table has a name with this byte, so the key cannot be that of a table's tab.
	key := "\x00diagram\x00" + schema
	for i, tb := range a.tabs {
		if tb.conn == conn && tb.table == key {
			a.active = i
			return
		}
	}
	tb := a.addTab(conn, key, schema)
	tb.diagram = &erDiagram{
		dial:   func(ctx context.Context) (driver.Conn, error) { return a.connections.dial(ctx, conn) },
		update: a.update,
		schema: schema,
		open:   func(table string) { a.openTable(conn, schema, table) },
	}
}

func (a *App) addTab(conn, table, title string) *tab {
	a.ids++
	conf, _ := a.connections.conf(conn)
	pane := NewQueryPane(func(ctx context.Context) (driver.Conn, error) { return a.connections.dial(ctx, conn) }, a.update)
	pane.Connection = conn
	a.mark(pane, conf)
	pane.Complete = func(text string, offset int, show func(start int, items []editor.Completion)) {
		a.catalog(conn).complete(text, offset, show)
	}
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

func (a *App) tabIndex(id int) int {
	return slices.IndexFunc(a.tabs, func(tb *tab) bool { return tb.id == id })
}

// The tab shown stays shown.
func (a *App) moveTab(from, to int) {
	if from < 0 || to < 0 || from >= len(a.tabs) || to >= len(a.tabs) {
		return
	}
	shown := a.tabs[a.active]
	moved := a.tabs[from]
	a.tabs = slices.Delete(a.tabs, from, from+1)
	a.tabs = slices.Insert(a.tabs, to, moved)
	a.active = slices.Index(a.tabs, shown)
}

// catalog returns what is known of the tables of the connection named conn, shared by its tabs.
func (a *App) catalog(conn string) *catalog {
	if cat := a.catalogs[conn]; cat != nil {
		return cat
	}
	conf, _ := a.connections.conf(conn)
	cat := &catalog{
		dial:   func(ctx context.Context) (driver.Conn, error) { return a.connections.dial(ctx, conn) },
		update: a.update,
		drv:    a.drivers[conf.Kind],
	}
	if cat.drv != nil {
		cat.defaultSchema = cat.drv.DefaultSchema(conf.Database)
	}
	if a.catalogs == nil {
		a.catalogs = map[string]*catalog{}
	}
	a.catalogs[conn] = cat
	return cat
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
	// The next completion reads the tables again, as they may be what changed.
	delete(a.catalogs, conn)
	for _, tb := range a.tabs {
		if tb.conn == conn {
			tb.pane.Close()
		}
	}
}
