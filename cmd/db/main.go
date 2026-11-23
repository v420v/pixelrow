package main

import (
	"log"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"db/internal/app"
	"db/internal/config"
	"db/internal/driver"
	"db/internal/driver/mysql"
	"db/internal/driver/postgres"
)

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
		path, err := config.Path()
		if err != nil {
			log.Fatal(err)
		}
		drivers := map[string]driver.Driver{
			"postgres": postgres.Driver{},
			"mysql":    mysql.Driver{},
		}
		a := app.New(app.Options{
			Drivers:    drivers,
			ConfigPath: path,
			Update:     update,
			OpenFile:   mygo.Shell.OpenPath,
			SaveFile: func(name string) (string, error) {
				return mygo.Dialog.Save(mygo.SaveDialogOptions{Parent: win, DefaultPath: name})
			},
		})
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
