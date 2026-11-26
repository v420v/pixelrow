package main

import (
	"log"
	"os"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"github.com/v420v/pixelrow/internal/app"
	"github.com/v420v/pixelrow/internal/config"
	"github.com/v420v/pixelrow/internal/driver"
	"github.com/v420v/pixelrow/internal/driver/mysql"
	"github.com/v420v/pixelrow/internal/driver/postgres"
	"github.com/v420v/pixelrow/internal/tools"
)

func main() {
	// ssh starts the app again to ask it for a passphrase, which must not open a window.
	if tools.Askpass() {
		return
	}
	executable, err := os.Executable()
	if err != nil {
		log.Fatal(err)
	}
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
			Askpass:    executable,
			SaveFile: func(name string) (string, error) {
				return mygo.Dialog.Save(mygo.SaveDialogOptions{Parent: win, DefaultPath: name})
			},
			ChooseFile: func() (string, error) {
				paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{Parent: win})
				if err != nil || len(paths) == 0 {
					return "", err
				}
				return paths[0], nil
			},
		})
		mygo.App.OnQuit(a.Close)
		win = mygo.NewWindow(mygo.WindowOptions{
			Title:   "PixelRow",
			Width:   1280,
			Height:  800,
			Content: ui.View(a.View),
		})
	})
	if err := mygo.App.Run(); err != nil {
		log.Fatal(err)
	}
}
