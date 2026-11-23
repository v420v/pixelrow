package main

import (
	"cmp"
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
		// Results arrive from other goroutines: RunOnMain reads win on the thread that sets it.
		update := func(fn func()) {
			mygo.RunOnMain(func() {
				fn()
				win.Invalidate()
			})
		}
		a := app.New(postgres.Driver{}, envConfig(), update)
		win = mygo.NewWindow(mygo.WindowOptions{
			Title:   "db client",
			Width:   1280,
			Height:  800,
			Content: ui.View(a.View),
		})
	})
	if err := mygo.App.Run(); err != nil {
		log.Fatal(err)
	}
}

