package app

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"

	"db/internal/driver"
)

// maxRows is how many rows are kept of a result that is read to its end.
const maxRows = 1000

const (
	// pageSize is how many rows of a streamed result are read at a time.
	pageSize = 500
	// loadAhead is how many rows before the last one loaded the table shows when the next page is read.
	loadAhead = 100
	// idleLimit is how long a streamed result stays open without being read.
	// An open result holds its locks on the server, so it is not left open for good.
	idleLimit = 5 * time.Minute
)

// QueryPane is a SQL editor with the result of its last run below it.
type QueryPane struct {
	// Connection and Address name the database the pane runs on, shown beside the Run button.
	Connection, Address string
	// Color marks the pane as its connection's, when it is not the zero color.
	Color ui.Color
	// ReadOnly says the connection takes no writes, so the rows shown are not offered for editing.
	ReadOnly bool
	// Trailing builds what goes at the end of the toolbar, as the switch between the views of a table.
	Trailing func(c *ui.Context)

	// open connects to the database. It is called on the first run and after the connection is lost.
	open func(ctx context.Context) (driver.Conn, error)
	// update runs fn on the UI thread and redraws, as Window.Update does.
	update func(fn func())

	// table is the table the pane was opened for, whose rows can be edited, or nil for a blank query.
	table *tableRef

	editor *editor.State
	split  float32

	running bool
	cancel  context.CancelFunc
	result  *result
	runs    int

	// session is held by the goroutine using conn or a result's stream.
	session sync.Mutex
	conn    driver.Conn
	// closing is set when the connection is to be closed once the running query ends.
	closing bool
}

// result represents the last result set produced by a single execution, or an error.
type result struct {
	// sql is the query that produced the result.
	sql     string
	cols    []driver.Column
	columns []ui.TableColumn
	rows    [][]driver.Value
	// total counts the rows of a result read to its end, including those past maxRows that were not kept.
	total   int
	command string
	elapsed time.Duration
	err     error

	// stream is the result still open on the server, while rows remain to read.
	stream driver.Results
	// loading is set while the next page is being read.
	loading bool
	// stopped is set when the stream was closed before its last row.
	stopped bool
	idle    *time.Timer

	table ui.ListState
	// chosen holds the rows chosen by their place among those shown, and row is the one the keys move from.
	chosen ui.Selection[int]
	row    int
	// edits is nil for a result that cannot be edited.
	edits *edits
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
	c.OnWindowShortcut(ui.Cmd, ui.KeyS, p.commit)

	ui.Column(c).Fill().Children(func() {
		bar := ui.Row(c).FillWidth().Height(barHeight).Shrink(0).Padding(0, t.Space(2)).Gap(t.Space(2)).AlignItems(ui.Center)
		// A faint wash of the connection's color over the whole bar tells where the query runs without shouting it.
		if p.Color != (ui.Color{}) {
			bar.Background(p.Color.Alpha(0.08))
		}
		bar.Children(func() {
			primaryIconButton(c, iconRun, "Run").Disabled(p.busy() || p.unsaved()).OnClick(p.run)
			if p.busy() {
				iconButton(c, iconStop, "Cancel").OnClick(p.cancelRun)
			}
			if p.Connection != "" {
				ui.Row(c).Gap(t.Space(1.5)).AlignItems(ui.Center).Shrink(0).Children(func() {
					if p.Color != (ui.Color{}) {
						colorDot(c, p.Color)
					}
					ui.Text(c, p.Connection).FontSize(smallText).FontWeight(600).SingleLine()
				})
				if p.ReadOnly {
					ui.Row(c).Padding(1, t.Space(2)).Radius(999).Shrink(0).Border(1, t.Border).Children(func() {
						ui.Text(c, "Read-only").FontSize(tinyText).TextColor(t.TextMuted).SingleLine()
					})
				}
				ui.Text(c, p.Address).FontSize(smallText).TextColor(t.TextMuted).SingleLine()
			}
			ui.Spacer(c)
			if p.Trailing != nil {
				p.Trailing(c)
			}
		})
		hairline(c)
		ui.SplitVerticalPercent(c, &p.split, func() {
			editor.View(c, p.editor).Fill().AutoFocus()
		}, func() {
			p.resultView(c)
		}).Grow(1).FillWidth()
		hairline(c)
		p.statusView(c)
	})
}

func (p *QueryPane) resultView(c *ui.Context) {
	r := p.result
	switch {
	case r == nil || len(r.columns) == 0:
		ui.Column(c).Fill()
	default:
		c.Key(r.id)
		r.table.Selection, r.table.Selected, r.table.CellSelect = &r.chosen, &r.row, true
		// A wash of the accent rather than a solid block: the cells keep their own text colors,
		// which also tell NULLs and pending edits apart, and the cursor's border stays visible on it.
		t := c.Theme()
		r.table.CellsStyle = ui.CellsStyle{
			Cell:     t.Accent.Alpha(0.16),
			CellText: t.Accent,
			Row:      t.Accent.Alpha(0.05),
		}
		r.table.RowHeight = rowHeight
		r.table.CellMenu = nil
		// The rows stay as they are while a commit runs, which ends by reading them again.
		editing := r.edits != nil && !p.running
		if editing {
			r.table.CellMenu = p.cellMenu(r)
		}
		table := ui.Table(c, &r.table, r.columns, r.shown(), func(row, col int) {
			// The table builds only the rows in view, so building one near the end means the user scrolled there.
			if col == 0 && row >= r.shown()-loadAhead {
				p.loadMore()
			}
			p.cellView(c, r, cell{row, col})
		}).Fill()
		// A double click or Enter edits the cell the keys are on.
		if col, err := strconv.Atoi(r.table.Cells.Cursor); editing && err == nil && table.Submitted() {
			r.beginEdit(cell{r.row, col})
		}
	}
}

// unsaved reports whether the result shown has edits, which another run would lose.
func (p *QueryPane) unsaved() bool {
	return p.result != nil && p.result.edits.count() > 0
}

func (p *QueryPane) statusView(c *ui.Context) {
	t := c.Theme()
	bar := ui.Row(c).FillWidth().MinHeight(rowHeight).Shrink(0).Padding(t.Space(0.5), t.Space(3)).Gap(t.Space(2)).AlignItems(ui.Center).Background(t.Surface)
	bar.FontSize(smallText).Children(func() {
		switch r := p.result; {
		case p.running:
			ui.Spinner(c)
			ui.Text(c, "Running…").TextColor(t.TextMuted)
		case r == nil:
			ui.Text(c, "Ready").TextColor(t.TextMuted)
		case r.err != nil:
			ui.Text(c, errorText(r.err)).TextColor(t.Danger).Selectable()
		default:
			if r.loading {
				ui.Spinner(c)
			}
			switch {
			case r.edits != nil && r.edits.err != nil:
				ui.Text(c, errorText(r.edits.err)).TextColor(t.Danger).Selectable().Grow(1).MinWidth(0)
			case p.table != nil && r.edits == nil && len(r.columns) > 0:
				ui.Text(c, r.summary()+" · Read-only").TextColor(t.TextMuted).Grow(1)
			default:
				ui.Text(c, r.summary()).TextColor(t.TextMuted).Grow(1)
			}
			if r.edits != nil {
				p.editBar(c, r)
			}
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
	if p.busy() || p.unsaved() || strings.TrimSpace(sql) == "" {
		return
	}

	// The rows left unread of the result shown are given up.
	old, oldCancel := p.detachStream()
	ctx, cancel := context.WithCancel(context.Background())
	p.running, p.cancel = true, cancel
	p.runs++
	id := p.runs
	// Taken here, as the pane's fields belong to the UI thread.
	editable := !p.ReadOnly
	go func() {
		p.session.Lock()
		if old != nil {
			oldCancel()
			old.Close()
		}
		r := p.execute(ctx, sql, editable)
		p.session.Unlock()
		r.id = id
		p.update(func() {
			p.running, p.result = false, r
			p.settle(r)
		})
	}()
}

func (p *QueryPane) busy() bool {
	return p.running || p.result != nil && p.result.loading
}

// settle follows a run or a page read: an open stream waits for the next read, and anything else ends the query.
func (p *QueryPane) settle(r *result) {
	switch {
	case p.closing:
		p.closing = false
		p.closeConn()
	case r.stream != nil:
		r.idle = time.AfterFunc(idleLimit, func() {
			p.update(func() {
				if p.result == r && !p.busy() {
					p.stopStream()
				}
			})
		})
	default:
		p.cancelRun()
		p.cancel = nil
	}
}

func (p *QueryPane) loadMore() {
	r := p.result
	if p.running || r == nil || r.stream == nil || r.loading {
		return
	}
	r.loading = true
	r.idle.Stop()
	res := r.stream
	go func() {
		p.session.Lock()
		var page [][]driver.Value
		var command string
		var err error
		done := readPage(res, &page)
		if done {
			command, err = finish(res)
			p.dropDeadConn(err)
		}
		p.session.Unlock()
		p.update(func() {
			r.rows = append(r.rows, page...)
			r.loading = false
			if done {
				r.stream, r.command, r.err = nil, command, err
			}
			p.settle(r)
		})
	}()
}

// detachStream takes the open stream of the result shown, with the cancel of its query, for the caller to close.
func (p *QueryPane) detachStream() (driver.Results, context.CancelFunc) {
	r := p.result
	if r == nil || r.stream == nil {
		return nil, nil
	}
	res, cancel := r.stream, p.cancel
	r.stream, r.stopped, p.cancel = nil, true, nil
	r.idle.Stop()
	return res, cancel
}

func (p *QueryPane) stopStream() {
	res, cancel := p.detachStream()
	if res == nil {
		return
	}
	go func() {
		p.session.Lock()
		defer p.session.Unlock()
		cancel()
		res.Close()
	}()
}

func (p *QueryPane) SetText(sql string) {
	p.editor.Buffer.Set(sql)
}

// Close cancels the running query and closes the connection.
// The pane connects again if it runs another query.
func (p *QueryPane) Close() {
	if p.busy() {
		p.closing = true
		p.cancelRun()
		return
	}
	p.closeConn()
}

func (p *QueryPane) closeConn() {
	res, cancel := p.detachStream()
	conn := p.conn
	p.conn = nil
	if res == nil && conn == nil {
		return
	}
	go func() {
		p.session.Lock()
		defer p.session.Unlock()
		if res != nil {
			cancel()
			res.Close()
		}
		if conn != nil {
			conn.Close()
		}
	}()
}

func (p *QueryPane) cancelRun() {
	if p.cancel != nil {
		p.cancel()
	}
}

func (p *QueryPane) execute(ctx context.Context, sql string, editable bool) *result {
	start := time.Now()
	r := &result{sql: sql, row: -1}
	r.err = p.read(ctx, sql, r, editable)
	r.elapsed = time.Since(start)
	if r.stream == nil {
		p.dropDeadConn(r.err)
	}
	return r
}

// dropDeadConn closes the connection if err came from losing it, so that the next run connects again.
func (p *QueryPane) dropDeadConn(err error) {
	if err != nil && p.conn != nil && !p.alive() {
		p.conn.Close()
		p.conn = nil
	}
}

func (p *QueryPane) read(ctx context.Context, sql string, r *result, editable bool) error {
	if p.conn == nil {
		conn, err := p.open(ctx)
		if err != nil {
			return err
		}
		p.conn = conn
	}
	// The table's key is read before the query, whose open result keeps the session busy.
	// Without it the result still shows, read-only.
	var info *driver.TableInfo
	if editable && p.table != nil && p.table.selects(sql) {
		info, _ = p.conn.Describe(ctx, p.table.schema, p.table.table)
	}
	res, err := p.conn.Query(ctx, sql)
	if err != nil {
		return err
	}
	if streamable(sql) {
		err := readFirst(res, r)
		if err == nil && info != nil {
			r.edits = newEdits(p.table, info, r.cols)
		}
		return err
	}
	for res.NextResultSet() {
		r.setColumns(res.Columns())
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

// readFirst reads the first page of a streamable query, and leaves the result open in r.stream when rows remain.
func readFirst(res driver.Results, r *result) error {
	if !res.NextResultSet() {
		return res.Close()
	}
	r.setColumns(res.Columns())
	if !readPage(res, &r.rows) {
		r.stream = res
		return nil
	}
	var err error
	r.command, err = finish(res)
	return err
}

// readPage appends the next rows of res to rows, and reports whether they were its last.
func readPage(res driver.Results, rows *[][]driver.Value) (done bool) {
	for range pageSize {
		if !res.Next() {
			return true
		}
		*rows = append(*rows, res.Row())
	}
	return false
}

func finish(res driver.Results) (command string, err error) {
	command = res.Command()
	for res.NextResultSet() {
	}
	return command, res.Close()
}

func (r *result) setColumns(cols []driver.Column) {
	r.cols = cols
	r.columns = make([]ui.TableColumn, len(cols))
	for i, col := range cols {
		r.columns[i] = ui.TableColumn{Title: col.Name, ID: strconv.Itoa(i), Width: 160, MinWidth: 40}
		if col.Kind == driver.KindNumber {
			r.columns[i].Align = ui.End
		}
	}
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
	case r.stream != nil:
		s = group(len(r.rows)) + "+ rows"
	case r.stopped:
		s = fmt.Sprintf("First %s rows, reading stopped", group(len(r.rows)))
	case len(r.rows) == 1:
		s = "1 row"
	default:
		s = group(len(r.rows)) + " rows"
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
