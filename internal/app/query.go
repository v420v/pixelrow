package app

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"

	"db/internal/driver"
)

// maxRows is how many rows of a result are kept and shown.
const maxRows = 1000

// QueryPane is a SQL editor with the result of its last run below it.
type QueryPane struct {
	// Connection and Address name the database the pane runs on, shown beside the Run button.
	Connection, Address string

	// open connects to the database. It is called on the first run and after the connection is lost.
	open func(ctx context.Context) (driver.Conn, error)
	// update runs fn on the UI thread and redraws, as Window.Update does.
	update func(fn func())

	editor *editor.State
	split  float32

	running bool
	cancel  context.CancelFunc
	result  *result
	runs    int

	// conn is used only by the goroutine of the current run.
	conn driver.Conn
	// closing is set when the connection is to be closed once the running query ends.
	closing bool
}

// result represents the last result set produced by a single execution, or an error.
type result struct {
	columns []ui.TableColumn
	rows    [][]driver.Value
	// total counts the rows of the result set, including those past maxRows that were not kept.
	total   int
	command string
	elapsed time.Duration
	err     error

	table ui.ListState
	// id keys the table so that each result starts with fresh scroll and column state.
	id int
}

func NewQueryPane(open func(ctx context.Context) (driver.Conn, error), update func(fn func())) *QueryPane {
	buf := editor.NewBuffer("")
	buf.SetLanguage(editor.LanguageNamed("SQL"))
	return &QueryPane{
		open:   open,
		update: update,
		split:  40,
		editor: &editor.State{
			Buffer:      buf,
			Label:       "SQL",
			Placeholder: "SELECT …",
			// Cmd+Enter runs the query instead of opening a line below.
			Bindings: []editor.Binding{{Mods: ui.Cmd, Key: ui.KeyEnter, Command: editor.NoCommand}},
		},
	}
}

func (p *QueryPane) View(c *ui.Context) {
	t := c.Theme()
	c.OnWindowShortcut(ui.Cmd, ui.KeyEnter, p.run)

	ui.Column(c).Fill().Children(func() {
		ui.Row(c).FillWidth().Padding(t.Space(2)).Gap(t.Space(2)).AlignItems(ui.Center).Children(func() {
			primaryIconButton(c, iconRun, "Run").Disabled(p.running).OnClick(p.run)
			if p.running {
				iconButton(c, iconStop, "Cancel").OnClick(p.cancelRun)
			}
			if p.Connection != "" {
				ui.Text(c, p.Connection).Bold().SingleLine()
				ui.Text(c, p.Address).TextColor(t.TextMuted).SingleLine()
			}
		})
		ui.Divider(c)
		ui.SplitVerticalPercent(c, &p.split, func() {
			editor.View(c, p.editor).Fill().AutoFocus()
		}, func() {
			p.resultView(c)
		}).Grow(1).FillWidth()
		ui.Divider(c)
		p.statusView(c)
	})
}

func (p *QueryPane) resultView(c *ui.Context) {
	t := c.Theme()
	r := p.result
	switch {
	case r == nil || r.err != nil || len(r.columns) == 0:
		ui.Column(c).Fill()
	default:
		c.Key(r.id)
		ui.Table(c, &r.table, r.columns, len(r.rows), func(row, col int) {
			v := r.rows[row][col]
			text := ui.Text(c, v.Text)
			if v.Null {
				text = ui.Text(c, "NULL").Italic().TextColor(t.TextMuted)
			}
			if r.table.CellSelected(row, col) {
				text.TextColor(t.AccentText)
			}
			text.SingleLine()
		}).Fill()
	}
}

func (p *QueryPane) statusView(c *ui.Context) {
	t := c.Theme()
	ui.Row(c).FillWidth().Padding(t.Space(1), t.Space(2)).Gap(t.Space(2)).AlignItems(ui.Center).Children(func() {
		switch r := p.result; {
		case p.running:
			ui.Spinner(c)
			ui.Text(c, "Running…").TextColor(t.TextMuted)
		case r == nil:
			ui.Text(c, "Ready").TextColor(t.TextMuted)
		case r.err != nil:
			ui.Text(c, errorText(r.err)).TextColor(t.Danger).Selectable()
		default:
			ui.Text(c, r.summary()).TextColor(t.TextMuted)
		}
	})
}

// run executes the selected text, or the whole buffer when nothing is selected.
func (p *QueryPane) run() {
	sql := p.editor.Buffer.String()
	if sel := p.editor.Selection(); sel.Start() != sel.End() {
		sql = p.editor.Buffer.Slice(sel.Start(), sel.End())
	}
	p.Run(sql)
}

// Run executes sql and shows its result, leaving the editor's text as it is.
func (p *QueryPane) Run(sql string) {
	if p.running || strings.TrimSpace(sql) == "" {
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	p.running, p.cancel = true, cancel
	p.runs++
	id := p.runs
	go func() {
		r := p.execute(ctx, sql)
		r.id = id
		cancel()
		p.update(func() {
			p.running, p.cancel, p.result = false, nil, r
			if p.closing {
				p.closing = false
				p.closeConn()
			}
		})
	}()
}

func (p *QueryPane) SetText(sql string) {
	p.editor.Buffer.Set(sql)
}

// Close cancels the running query and closes the connection.
// The pane connects again if it runs another query.
func (p *QueryPane) Close() {
	if p.running {
		p.closing = true
		p.cancelRun()
		return
	}
	p.closeConn()
}

func (p *QueryPane) closeConn() {
	if conn := p.conn; conn != nil {
		p.conn = nil
		go conn.Close()
	}
}

func (p *QueryPane) cancelRun() {
	if p.cancel != nil {
		p.cancel()
	}
}

func (p *QueryPane) execute(ctx context.Context, sql string) *result {
	start := time.Now()
	r := &result{}
	r.err = p.read(ctx, sql, r)
	r.elapsed = time.Since(start)
	if r.err != nil && p.conn != nil && !p.alive() {
		p.conn.Close()
		p.conn = nil
	}
	return r
}

func (p *QueryPane) read(ctx context.Context, sql string, r *result) error {
	if p.conn == nil {
		conn, err := p.open(ctx)
		if err != nil {
			return err
		}
		p.conn = conn
	}
	res, err := p.conn.Query(ctx, sql)
	if err != nil {
		return err
	}
	for res.NextResultSet() {
		cols := res.Columns()
		r.columns = make([]ui.TableColumn, len(cols))
		for i, col := range cols {
			r.columns[i] = ui.TableColumn{Title: col.Name, ID: strconv.Itoa(i), Width: 160, MinWidth: 40}
			if col.Kind == driver.KindNumber {
				r.columns[i].Align = ui.End
			}
		}
		r.rows, r.total = nil, 0
		for res.Next() {
			if r.total < maxRows {
				r.rows = append(r.rows, res.Row())
			}
			r.total++
		}
		r.command = res.Command()
	}
	return res.Close()
}

func (p *QueryPane) alive() bool {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return p.conn.Ping(ctx) == nil
}

func (r *result) summary() string {
	var s string
	switch {
	case len(r.columns) == 0:
		s = r.command
		if s == "" {
			s = "Done"
		}
	case r.total > len(r.rows):
		s = fmt.Sprintf("Showing first %s of %s rows", group(len(r.rows)), group(r.total))
	case r.total == 1:
		s = "1 row"
	default:
		s = group(r.total) + " rows"
	}
	return s + " · " + formatElapsed(r.elapsed)
}

func errorText(err error) string {
	if errors.Is(err, context.Canceled) {
		return "Canceled"
	}
	var de *driver.Error
	if !errors.As(err, &de) {
		return err.Error()
	}
	s := "ERROR: " + de.Message
	if de.Detail != "" {
		s += " · " + de.Detail
	}
	if de.Hint != "" {
		s += " · Hint: " + de.Hint
	}
	return s
}

// group formats n with thousands separators.
func group(n int) string {
	s := strconv.Itoa(n)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}

func formatElapsed(d time.Duration) string {
	if d < time.Second {
		return fmt.Sprintf("%d ms", d.Milliseconds())
	}
	return fmt.Sprintf("%.2f s", d.Seconds())
}
