// Package app holds the screens of the app.
package app

import (
	"context"
	"strconv"

	"github.com/egoist/mygo/ui"

	"db/internal/driver"
)

type App struct {
	sidebar      *sidebar
	query        *QueryPane
	sidebarWidth float32
}

func New(drv driver.Driver, cfg driver.Config, update func(fn func())) *App {
	open := func(ctx context.Context) (driver.Conn, error) { return drv.Open(ctx, cfg) }
	a := &App{query: NewQueryPane(open, update), sidebarWidth: 240}
	a.sidebar = &sidebar{open: open, update: update, onTable: func(schema, table string) {
		a.query.Run("SELECT * FROM " + drv.QuoteIdent(schema) + "." + drv.QuoteIdent(table) + " LIMIT " + strconv.Itoa(maxRows))
	}}
	return a
}

func (a *App) View(c *ui.Context) {
	ui.Split(c, &a.sidebarWidth, func() { a.sidebar.view(c) }, func() { a.query.View(c) }).Fill()
}

