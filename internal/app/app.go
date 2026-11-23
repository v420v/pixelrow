// Package app holds the screens of the app.
package app

import (
	"context"
	"strconv"

	"github.com/egoist/mygo/ui"

	"db/internal/config"
	"db/internal/driver"
)

type App struct {
	drivers map[string]driver.Driver
	update  func(fn func())

	connections *connections
	// workspace is nil until a connection is opened.
	workspace *workspace
}

type workspace struct {
	conn         config.Connection
	sidebar      *sidebar
	query        *QueryPane
	sidebarWidth float32
}

func New(drivers map[string]driver.Driver, configPath string, update func(fn func()), openFile func(path string) error) *App {
	a := &App{drivers: drivers, update: update}
	a.connections = newConnections(configPath, drivers, update, openFile, a.connect)
	return a
}

func (a *App) View(c *ui.Context) {
	if a.workspace == nil {
		a.connections.view(c)
		return
	}
	a.workspace.view(c, a.disconnect)
}

func (a *App) connect(conn config.Connection) {
	dial := func(ctx context.Context) (driver.Conn, error) { return open(ctx, a.drivers, conn) }
	w := &workspace{conn: conn, query: NewQueryPane(dial, a.update), sidebarWidth: 240}
	w.sidebar = &sidebar{open: dial, update: a.update, onTable: func(schema, table string) {
		drv := a.drivers[conn.Kind]
		w.query.Run("SELECT * FROM " + drv.QuoteIdent(schema) + "." + drv.QuoteIdent(table) + " LIMIT " + strconv.Itoa(maxRows))
	}}
	a.workspace = w
}

func (a *App) disconnect() {
	a.workspace.sidebar.close()
	a.workspace.query.Close()
	a.workspace = nil
}

func (w *workspace) view(c *ui.Context, disconnect func()) {
	t := c.Theme()
	ui.Split(c, &w.sidebarWidth, func() {
		ui.Column(c).Fill().Children(func() {
			ui.Row(c).FillWidth().Padding(t.Space(2)).Gap(t.Space(2)).AlignItems(ui.Center).Children(func() {
				ui.Text(c, w.conn.Name).Bold().SingleLine().Grow(1).Tooltip(address(w.conn))
				ui.Button(c, "Disconnect").OnClick(disconnect)
			})
			ui.Divider(c)
			w.sidebar.view(c)
		})
	}, func() { w.query.View(c) }).Fill()
}

