package main

import (
	"cmp"
	"context"
	"log"
	"os"
	"strconv"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"db/internal/app"
	"db/internal/driver"
	"db/internal/driver/postgres"
)

func envConfig() driver.Config {
	port, _ := strconv.Atoi(cmp.Or(os.Getenv("DB_PORT"), "55432"))
	return driver.Config{
		Host:     cmp.Or(os.Getenv("DB_HOST"), "127.0.0.1"),
		Port:     port,
		User:     cmp.Or(os.Getenv("DB_USER"), "postgres"),
		Password: cmp.Or(os.Getenv("DB_PASSWORD"), "postgres"),
		Database: cmp.Or(os.Getenv("DB_NAME"), "postgres"),
	}
}

func main() {
	mygo.App.WhenReady(func() {
		var win *mygo.Window
		cfg := envConfig()
		pane := app.NewQueryPane(
			func(ctx context.Context) (driver.Conn, error) { return postgres.Driver{}.Open(ctx, cfg) },
			func(fn func()) { win.Update(fn) },
		)
		win = mygo.NewWindow(mygo.WindowOptions{
			Title:   "db client",
			Width:   1280,
			Height:  800,
			Content: ui.View(pane.View),
		})
	})
	if err := mygo.App.Run(); err != nil {
		log.Fatal(err)
	}
}
