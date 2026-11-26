package app

import (
	"context"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"

	"github.com/v420v/pixelrow/internal/driver"
)

// structure shows the columns, indexes and foreign keys of one table.
type structure struct {
	// dial opens a connection used for one load, so the view never waits for the tab's own session.
	dial   func(ctx context.Context) (driver.Conn, error)
	update func(fn func())
	schema string
	table  string
	// trailing builds what goes at the end of the bar, as the switch between the views of a table.
	trailing func(c *ui.Context)

	started bool
	loading bool
	err     error
	info    *driver.TableInfo

	columns ui.ListState
	indexes ui.ListState
	keys    ui.ListState
	ddl     *editor.State
}

func (s *structure) view(c *ui.Context) {
	t := c.Theme()
	if !s.started {
		s.started = true
		s.load()
	}

	ui.Column(c).Fill().Children(func() {
		ui.Row(c).FillWidth().Height(barHeight).Shrink(0).Padding(0, t.Space(2), 0, t.Space(3)).Gap(t.Space(2)).AlignItems(ui.Center).Children(func() {
			ui.Text(c, s.schema+"."+s.table).FontSize(smallText).FontWeight(600).Font(monospace).SingleLine()
			iconButton(c, iconReload, "Reload Structure").Disabled(s.loading).OnClick(s.load)
			if s.loading {
				ui.Spinner(c)
			}
			ui.Spacer(c)
			if s.trailing != nil {
				s.trailing(c)
			}
		})
		hairline(c)
		ui.Scroll(c).Grow(1).FillWidth().Children(func() {
			ui.Column(c).FillWidth().Padding(t.Space(4), t.Space(3)).Gap(t.Space(2)).Children(func() {
				switch {
				case s.err != nil:
					ui.Text(c, errorText(s.err)).TextColor(t.Danger).Selectable()
				case s.info != nil:
					s.infoView(c)
				}
			})
		})
	})
}

func (s *structure) infoView(c *ui.Context) {
	t := c.Theme()
	info := s.info
	text := func(v string) { ui.Text(c, v).Font(monospace).FontSize(smallText).SingleLine().Selectable() }
	first := true
	section := func(title string, n int) bool {
		// Room above each section but the first sets them apart, where a line between them would add weight.
		label := heading(c, title)
		if !first {
			label.Margin(t.Space(4), 0, 0, 0)
		}
		first = false
		if n == 0 {
			ui.Text(c, "None").FontSize(smallText).TextColor(t.TextMuted)
		}
		return n > 0
	}
	for _, state := range []*ui.ListState{&s.columns, &s.indexes, &s.keys} {
		state.RowHeight = rowHeight
	}

	if section("COLUMNS", len(info.Columns)) {
		cols := []ui.TableColumn{{Title: "Name", Width: 220}, {Title: "Type", Width: 200}, {Title: "Nullable", Width: 90}, {Title: "Default", Width: 280}, {Title: "Key", Width: 80}}
		ui.Table(c, &s.columns, cols, len(info.Columns), func(row, col int) {
			column := info.Columns[row]
			switch col {
			case 0:
				text(column.Name)
			case 1:
				text(column.Type)
			case 2:
				text(yesNo(column.Nullable))
			case 3:
				if !column.Default.Null {
					text(column.Default.Text)
				}
			case 4:
				if column.PrimaryKey {
					text("Primary")
				}
			}
		}).FillWidth()
	}

	if section("INDEXES", len(info.Indexes)) {
		cols := []ui.TableColumn{{Title: "Name", Width: 280}, {Title: "Columns", Width: 320}, {Title: "Kind", Width: 100}}
		ui.Table(c, &s.indexes, cols, len(info.Indexes), func(row, col int) {
			index := info.Indexes[row]
			switch col {
			case 0:
				text(index.Name)
			case 1:
				text(index.Columns)
			case 2:
				switch {
				case index.Primary:
					text("Primary")
				case index.Unique:
					text("Unique")
				}
			}
		}).FillWidth()
	}

	if section("FOREIGN KEYS", len(info.ForeignKeys)) {
		cols := []ui.TableColumn{{Title: "Name", Width: 280}, {Title: "Columns", Width: 220}, {Title: "References", Width: 360}}
		ui.Table(c, &s.keys, cols, len(info.ForeignKeys), func(row, col int) {
			key := info.ForeignKeys[row]
			switch col {
			case 0:
				text(key.Name)
			case 1:
				text(key.Columns)
			case 2:
				text(key.RefSchema + "." + key.RefTable + " (" + key.RefColumns + ")")
			}
		}).FillWidth()
	}

	if s.ddl != nil {
		heading(c, "DDL").Margin(t.Space(4), 0, 0, 0)
		editor.View(c, s.ddl).FillWidth()
	}
}

func (s *structure) load() {
	if s.loading {
		return
	}
	s.loading = true
	go func() {
		info, err := s.describe()
		s.update(func() {
			s.loading, s.err = false, err
			if err != nil {
				return
			}
			s.info, s.ddl = info, nil
			if info.DDL != "" {
				buf := editor.NewBuffer(info.DDL)
				buf.SetLanguage(editor.LanguageNamed("SQL"))
				s.ddl = &editor.State{Buffer: buf, Label: "DDL", ReadOnly: true, FitContent: true, NoActiveLine: true}
			}
		})
	}()
}

func (s *structure) describe() (*driver.TableInfo, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	conn, err := s.dial(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	return conn.Describe(ctx, s.schema, s.table)
}

func yesNo(b bool) string {
	if b {
		return "Yes"
	}
	return "No"
}
