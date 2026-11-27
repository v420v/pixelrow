package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"

	"github.com/egoist/mygo/ui"

	"github.com/v420v/pixelrow/internal/config"
	"github.com/v420v/pixelrow/internal/tools"
)

// fileRunner asks which SQL file to run on a connection and runs it with the database's own client.
type fileRunner struct {
	connections *connections
	jobs        *jobs
	// chooseFile asks for a file to open, and returns "" when the user cancels.
	chooseFile func() (string, error)

	// asking is set while the dialog shows, and form holds what it edits.
	asking bool
	form   *fileForm
	// programs keeps the path typed for each client, for the next file of this run of the app.
	programs map[string]string
}

type fileForm struct {
	conf   config.Connection
	client *tools.Client
	// database is where the file runs, or "" for the file to choose.
	database string
	path     string
	size     int64

	program   string
	keepGoing bool
	single    bool
	err       string
}

func (r *fileRunner) ask(conn, database string) {
	conf, ok := r.connections.conf(conn)
	client := tools.Clients[conf.Kind]
	if !ok || client == nil || conf.ReadOnly {
		return
	}
	path, err := r.chooseFile()
	if err != nil || path == "" {
		return
	}
	info, err := os.Stat(path)
	if err != nil {
		return
	}
	if database == "" {
		database = conf.Database
	}
	f := &fileForm{conf: conf, client: client, database: database, path: path, size: info.Size()}
	f.program = r.programs[client.Name]
	if f.program == "" {
		program, err := tools.Find(client.Name, r.connections.tools[client.Name])
		if err != nil {
			f.err = err.Error() + ". Enter its path."
		}
		f.program = program
	}
	r.form, r.asking = f, true
}

func (r *fileRunner) dialog(c *ui.Context) {
	f := r.form
	if f == nil {
		return
	}
	t := c.Theme()
	ui.Modal(c, &r.asking, func() {
		ui.Column(c).Width(480).Gap(t.Space(3)).Children(func() {
			ui.Text(c, "Run SQL File").FontSize(16).Bold()
			ui.Column(c).Gap(t.Space(1)).Children(func() {
				ui.Row(c).Gap(t.Space(3)).Children(func() {
					ui.Text(c, "File").TextColor(t.TextMuted).Width(70)
					ui.Text(c, filepath.Base(f.path)+" · "+formatBytes(f.size)).Tooltip(f.path).Selectable()
				})
				ui.Row(c).Gap(t.Space(3)).Children(func() {
					ui.Text(c, "Runs on").TextColor(t.TextMuted).Width(70)
					if f.conf.Color != "" {
						colorDot(c, ui.Hex(f.conf.Color))
					}
					ui.Text(c, f.target()).Bold().Selectable()
				})
			})
			ui.Text(c, "The statements of the file change this database. What they did is not undone by canceling.").TextColor(t.TextMuted)

			ui.Field(c, f.client.Name, func() {
				ui.TextInput(c, &f.program).Placeholder("/path/to/" + f.client.Name)
			}).Description("Set " + f.client.Name + " under [tools] in the config file to keep a path.")

			ui.Column(c).Gap(t.Space(1)).Children(func() {
				ui.Checkbox(c, &f.keepGoing, "Keep going after a statement fails")
				if f.client.SingleTransaction {
					ui.Checkbox(c, &f.single, "Run in one transaction, undone as a whole if it fails")
				}
			})

			if f.err != "" {
				ui.Text(c, f.err).TextColor(t.Danger).Selectable()
			}
			ui.Row(c).Gap(t.Space(2)).Justify(ui.End).Children(func() {
				ui.Button(c, "Cancel").OnClick(func() { r.asking = false })
				ui.PrimaryButton(c, "Run").OnClick(r.start)
			})
		})
	})
}

func (f *fileForm) target() string {
	conf := f.conf
	conf.Database = f.database
	return f.conf.Name + " · " + address(conf)
}

func (r *fileRunner) start() {
	f := r.form
	f.program = strings.TrimSpace(f.program)
	if _, err := tools.Find(f.client.Name, f.program); err != nil || f.program == "" {
		f.err = "Enter the path of " + f.client.Name + "."
		if err != nil && f.program != "" {
			f.err = err.Error() + "."
		}
		return
	}
	o := tools.RunOptions{
		Target:            target(f.conf, f.database),
		KeepGoing:         f.keepGoing,
		SingleTransaction: f.single && f.client.SingleTransaction,
	}
	name := filepath.Base(f.path)
	canceled := "Run of " + name + " canceled. What already ran is not undone."
	if o.SingleTransaction {
		canceled = "Run of " + name + " canceled and undone."
	}
	var read atomic.Int64
	started := r.jobs.start("Running "+name,
		func() (string, float64) {
			n := read.Load()
			if f.size == 0 {
				return formatBytes(n), -1
			}
			return formatBytes(n) + " of " + formatBytes(f.size), float64(n) / float64(f.size)
		},
		canceled,
		func(ctx context.Context) (string, string, error) {
			var err error
			if o.Host, o.Port, err = r.connections.endpoint(ctx, f.conf); err != nil {
				return "", "", err
			}
			err = f.client.Run(ctx, f.program, f.path, o, &read)
			return "Ran " + name + " on " + f.conf.Name, f.path, err
		},
		// Whatever the outcome, the file may have created or dropped tables.
		func(error) { r.connections.refresh(f.conf.Name) })
	if !started {
		f.err = "Another dump or SQL file is still running."
		return
	}
	if r.programs == nil {
		r.programs = map[string]string{}
	}
	r.programs[f.client.Name] = f.program
	r.asking = false
}
