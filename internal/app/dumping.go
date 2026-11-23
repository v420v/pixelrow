package app

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/egoist/mygo/ui"

	"db/internal/config"
	"db/internal/driver"
	"db/internal/dump"
)

// dumper asks what to dump, runs one dump at a time with the database's own tool, and shows how it goes at the foot of the sidebar.
type dumper struct {
	connections *connections
	update      func(fn func())
	// saveFile asks where to save a file named name, and returns "" when the user cancels.
	saveFile func(name string) (string, error)

	// asking is set while the dialog shows, and form holds what it edits.
	asking bool
	form   *dumpForm
	// programs keeps the path typed for each tool, for the next dump of this run of the app.
	programs map[string]string

	running bool
	cancel  context.CancelFunc
	label   string
	written atomic.Int64
	// last is the outcome of the dump that ended last, until dismissed.
	last *dumpOutcome
}

type dumpForm struct {
	conf config.Connection
	tool *dump.Tool
	// schema is what to dump, and table narrows it to one table when not "".
	schema string
	table  string

	program string
	content dump.Content
	drop    bool
	extras  []bool

	// tables are those of the schema, to choose from, and checked says which are chosen.
	tables  []driver.Table
	checked []bool
	loading bool
	// err is why the dump cannot start, or why the tables could not be listed.
	err string
}

type dumpOutcome struct {
	label string
	path  string
	bytes int64
	err   error
}

// ask opens the dialog to dump schema on the connection named conn, or only table when it is not "".
func (d *dumper) ask(conn, schema, table string) {
	conf, ok := d.connections.conf(conn)
	tool := dump.Tools[conf.Kind]
	if !ok || tool == nil {
		return
	}
	f := &dumpForm{conf: conf, tool: tool, schema: schema, table: table, extras: make([]bool, len(tool.Extras))}
	for i, extra := range tool.Extras {
		f.extras[i] = extra.Default
	}
	f.program = d.programs[tool.Name]
	if f.program == "" {
		program, err := dump.Find(tool.Name, d.connections.tools[tool.Name])
		if err != nil {
			f.err = err.Error() + ". Enter its path below."
		}
		f.program = program
	}
	d.form, d.asking = f, true

	if table != "" {
		return
	}
	f.loading = true
	go func() {
		tables, err := d.listTables(conn, schema)
		d.update(func() {
			f.loading = false
			if err != nil {
				f.err = "Could not list the tables: " + errorText(err)
				return
			}
			f.tables, f.checked = tables, make([]bool, len(tables))
			for i := range f.checked {
				f.checked[i] = true
			}
		})
	}()
}

func (d *dumper) listTables(conn, schema string) ([]driver.Table, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	c, err := d.connections.dial(ctx, conn)
	if err != nil {
		return nil, err
	}
	defer c.Close()
	return c.Tables(ctx, schema)
}

// dialog builds the dialog while it is asked for. It is built outside the sidebar, over the whole window.
func (d *dumper) dialog(c *ui.Context) {
	f := d.form
	if f == nil {
		return
	}
	t := c.Theme()
	ui.Modal(c, &d.asking, func() {
		ui.Column(c).Width(480).Gap(t.Space(3)).Children(func() {
			ui.Column(c).Gap(2).Children(func() {
				ui.Text(c, "Dump "+f.target()).FontSize(16).Bold()
				ui.Text(c, f.conf.Name+" · "+address(f.conf)).TextColor(t.TextMuted).FontSize(12)
			})

			ui.Field(c, f.tool.Name, func() {
				ui.TextInput(c, &f.program).Placeholder("/path/to/" + f.tool.Name)
			}).Description("Set " + f.tool.Name + " under [tools] in the config file to keep a path.")

			ui.RadioGroup(c, func() {
				ui.Radio(c, &f.content, dump.StructureAndData, "Structure and data")
				ui.Radio(c, &f.content, dump.StructureOnly, "Structure only")
				ui.Radio(c, &f.content, dump.DataOnly, "Data only")
			}).Label("Content")

			ui.Column(c).Gap(t.Space(1)).Children(func() {
				ui.Checkbox(c, &f.drop, "Drop each object before creating it")
				for i, extra := range f.tool.Extras {
					if extra.WholeSchema && f.table != "" {
						continue
					}
					ui.Checkbox(c, &f.extras[i], extra.Label)
				}
			})

			if f.table == "" {
				f.tablesView(c)
			}
			if f.err != "" {
				ui.Text(c, f.err).TextColor(t.Danger).Selectable()
			}
			ui.Row(c).Gap(t.Space(2)).Justify(ui.End).Children(func() {
				ui.Button(c, "Cancel").OnClick(func() { d.asking = false })
				ui.PrimaryButton(c, "Dump…").Disabled(f.loading).OnClick(d.start)
			})
		})
	})
}

func (f *dumpForm) tablesView(c *ui.Context) {
	t := c.Theme()
	ui.Column(c).Gap(t.Space(1)).Children(func() {
		ui.Row(c).Gap(t.Space(2)).AlignItems(ui.Center).Children(func() {
			ui.Text(c, "Tables").Bold()
			ui.Spacer(c)
			ui.Button(c, "All").OnClick(func() { f.checkAll(true) })
			ui.Button(c, "None").OnClick(func() { f.checkAll(false) })
		})
		if f.loading {
			ui.Text(c, "Loading…").TextColor(t.TextMuted)
			return
		}
		ui.Scroll(c).FillWidth().Height(160).Children(func() {
			ui.Column(c).Gap(t.Space(1)).Children(func() {
				for i, table := range f.tables {
					ui.Checkbox(c, &f.checked[i], table.Name)
				}
			})
		})
	})
}

func (f *dumpForm) checkAll(on bool) {
	for i := range f.checked {
		f.checked[i] = on
	}
}

func (f *dumpForm) target() string {
	if f.table != "" {
		return f.schema + "." + f.table
	}
	return f.schema
}

func (f *dumpForm) options() (dump.Options, error) {
	o := dump.Options{
		Host:     f.conf.Host,
		Port:     f.conf.Port,
		User:     f.conf.User,
		Password: f.conf.Password,
		Database: f.conf.Database,
		Schema:   f.schema,
		Content:  f.content,
		Drop:     f.drop,
		Extras:   map[string]bool{},
	}
	for i, extra := range f.tool.Extras {
		o.Extras[extra.Key] = f.extras[i]
	}
	if f.table != "" {
		o.Tables = []string{f.table}
		return o, nil
	}
	// With every table chosen the schema is dumped as a whole, which also takes what belongs to no table.
	all := true
	for i, table := range f.tables {
		if f.checked[i] {
			o.Tables = append(o.Tables, table.Name)
		} else {
			all = false
		}
	}
	switch {
	case all:
		o.Tables = nil
	case len(o.Tables) == 0:
		return o, fmt.Errorf("Choose at least one table.")
	}
	return o, nil
}

func (d *dumper) start() {
	f := d.form
	if d.running {
		f.err = "Another dump is still running."
		return
	}
	f.program = strings.TrimSpace(f.program)
	if _, err := dump.Find(f.tool.Name, f.program); err != nil || f.program == "" {
		f.err = "Enter the path of " + f.tool.Name + "."
		if err != nil && f.program != "" {
			f.err = err.Error() + "."
		}
		return
	}
	o, err := f.options()
	if err != nil {
		f.err = err.Error()
		return
	}
	path, err := d.saveFile(fileName(f.conf.Name+"-"+f.target()) + ".sql")
	if err != nil {
		f.err = err.Error()
		return
	}
	if path == "" {
		return
	}
	if d.programs == nil {
		d.programs = map[string]string{}
	}
	d.programs[f.tool.Name] = f.program

	ctx, cancel := context.WithCancel(context.Background())
	label := f.target()
	d.asking, d.running, d.cancel, d.label, d.last = false, true, cancel, label, nil
	d.written.Store(0)
	// The size written is read as the view builds, and this only asks for the frames that show it growing.
	go func() {
		tick := time.NewTicker(300 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				d.update(func() {})
			}
		}
	}()
	go func() {
		n, err := f.tool.Run(ctx, f.program, path, o, &d.written)
		cancel()
		d.update(func() {
			d.running, d.cancel = false, nil
			d.last = &dumpOutcome{label: label, path: path, bytes: n, err: err}
		})
	}()
}

func (d *dumper) view(c *ui.Context) {
	if !d.running && d.last == nil {
		return
	}
	t := c.Theme()
	ui.Divider(c)
	ui.Row(c).FillWidth().Padding(t.Space(2)).Gap(t.Space(2)).AlignItems(ui.Center).Children(func() {
		switch {
		case d.running:
			ui.Spinner(c)
			ui.Text(c, "Dumping "+d.label+" · "+formatBytes(d.written.Load())).FontSize(12).TextColor(t.TextMuted).Grow(1)
			iconButton(c, iconStop, "Cancel Dump").OnClick(d.cancel)
		case d.last.err != nil:
			// A tool can print many lines, of which the first says what went wrong.
			msg := errorText(d.last.err)
			first, _, _ := strings.Cut(msg, "\n")
			ui.Text(c, "Dump failed: "+first).FontSize(12).TextColor(t.Danger).Grow(1).Tooltip(msg).Selectable()
			iconButton(c, iconClose, "Dismiss").OnClick(func() { d.last = nil })
		default:
			status := fmt.Sprintf("Dumped %s to %s · %s", d.last.label, filepath.Base(d.last.path), formatBytes(d.last.bytes))
			ui.Text(c, status).FontSize(12).TextColor(t.TextMuted).Grow(1).Tooltip(d.last.path).Selectable()
			iconButton(c, iconClose, "Dismiss").OnClick(func() { d.last = nil })
		}
	})
}

// fileName replaces what a file name cannot hold everywhere.
func fileName(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			return r
		}
		return '_'
	}, s)
}

func formatBytes(n int64) string {
	switch {
	case n < 1<<10:
		return fmt.Sprintf("%d B", n)
	case n < 1<<20:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	case n < 1<<30:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	}
	return fmt.Sprintf("%.2f GB", float64(n)/(1<<30))
}
