package app

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"

	"db/internal/driver"
)

// tableRef is the table a pane was opened for.
type tableRef struct {
	schema, table string
	// sql reads every row of the table. A query that starts with it can have its result edited.
	sql string
	drv driver.Driver
}

// selects reports whether sql reads rows of the table and of nothing else, so that a row of its result is a row of the table.
func (t *tableRef) selects(sql string) bool {
	rest, ok := strings.CutPrefix(strings.TrimSpace(sql), t.sql)
	if !ok || !streamable(sql) || rest != "" && !isSpace(rest[0]) && rest[0] != ';' {
		return false
	}
	// A word that only looks like one of these makes the result read-only, which loses nothing.
	rest = strings.ToLower(rest)
	for _, word := range []string{"union", "intersect", "except"} {
		if strings.Contains(rest, word) {
			return false
		}
	}
	return true
}

// cell is a place in a result: the row counts the rows read, or those shown, as its use says.
type cell struct{ row, col int }

// edits are the changes made to a result's rows and not yet committed to its table.
type edits struct {
	// table is the quoted name of the table, and columns the names of its columns, as the result has them.
	table   string
	columns []string
	// key lists the columns of the primary key, which tell a row from the others.
	key []int
	// locked marks the columns whose text is not what the database takes back.
	locked []bool

	// cells holds the new values, by row read.
	cells   map[cell]driver.Value
	deleted map[int]bool
	// inserted are the new rows, the newest first, shown above the rows read. A nil value is left to its default.
	inserted [][]*driver.Value

	// editing is set while the cell at, of the rows shown, is a text input of draft.
	editing bool
	at      cell
	draft   string
	// focused is set once that input had the focus, so that losing it ends the edit.
	focused bool

	// err is why the last commit failed.
	err error
}

// newEdits returns nil for a result that cannot be edited: one without the table's columns, or of a table without a primary key.
func newEdits(t *tableRef, info *driver.TableInfo, cols []driver.Column) *edits {
	if len(cols) != len(info.Columns) {
		return nil
	}
	e := &edits{
		table:   t.drv.QuoteIdent(t.schema) + "." + t.drv.QuoteIdent(t.table),
		locked:  make([]bool, len(cols)),
		cells:   map[cell]driver.Value{},
		deleted: map[int]bool{},
	}
	for i, col := range info.Columns {
		if col.Name != cols[i].Name {
			return nil
		}
		e.columns = append(e.columns, col.Name)
		// binary values show as hex, which MySQL would store as text. Parse them per driver to edit them.
		e.locked[i] = cols[i].Kind == driver.KindBinary
		if col.PrimaryKey {
			e.key = append(e.key, i)
		}
	}
	if len(e.key) == 0 {
		return nil
	}
	return e
}

func (e *edits) count() int {
	if e == nil {
		return 0
	}
	return len(e.cells) + len(e.deleted) + len(e.inserted)
}

func (e *edits) reset() {
	clear(e.cells)
	clear(e.deleted)
	e.inserted, e.editing, e.err = nil, false, nil
}

// statement is one change to the table, with the values of its placeholders.
type statement struct {
	sql  string
	args []driver.Value
}

// statements returns what commits the edits to the table, given the rows read.
func (e *edits) statements(drv driver.Driver, rows [][]driver.Value) []statement {
	// where names the row by the key it was read with, after the args before it.
	where := func(row int, args []driver.Value) (string, []driver.Value) {
		var parts []string
		for _, col := range e.key {
			args = append(args, rows[row][col])
			parts = append(parts, drv.QuoteIdent(e.columns[col])+" = "+drv.Placeholder(len(args)))
		}
		return " WHERE " + strings.Join(parts, " AND "), args
	}

	var out []statement
	for _, row := range slices.Sorted(maps.Keys(e.deleted)) {
		cond, args := where(row, nil)
		out = append(out, statement{"DELETE FROM " + e.table + cond, args})
	}

	changed := map[int][]int{}
	for at := range e.cells {
		if !e.deleted[at.row] {
			changed[at.row] = append(changed[at.row], at.col)
		}
	}
	for _, row := range slices.Sorted(maps.Keys(changed)) {
		var sets []string
		var args []driver.Value
		for _, col := range slices.Sorted(slices.Values(changed[row])) {
			args = append(args, e.cells[cell{row, col}])
			sets = append(sets, drv.QuoteIdent(e.columns[col])+" = "+drv.Placeholder(len(args)))
		}
		cond, args := where(row, args)
		out = append(out, statement{"UPDATE " + e.table + " SET " + strings.Join(sets, ", ") + cond, args})
	}

	for _, row := range slices.Backward(e.inserted) {
		var names, values []string
		var args []driver.Value
		for col, v := range row {
			if v != nil {
				args = append(args, *v)
				names = append(names, drv.QuoteIdent(e.columns[col]))
				values = append(values, drv.Placeholder(len(args)))
			}
		}
		// A row of defaults alone still has to name a column: PostgreSQL and MySQL share no other way to write it.
		if len(names) == 0 {
			names, values = []string{drv.QuoteIdent(e.columns[0])}, []string{"DEFAULT"}
		}
		out = append(out, statement{"INSERT INTO " + e.table + " (" + strings.Join(names, ", ") + ") VALUES (" + strings.Join(values, ", ") + ")", args})
	}
	return out
}

// shown returns the number of rows the table shows: the new rows, then the rows read.
func (r *result) shown() int {
	if r.edits == nil {
		return len(r.rows)
	}
	return len(r.edits.inserted) + len(r.rows)
}

// editable reports whether the cell at, of the rows shown, can be given a value.
func (r *result) editable(at cell) bool {
	e := r.edits
	if e == nil || at.row < 0 || at.row >= r.shown() || at.col < 0 || at.col >= len(e.columns) {
		return false
	}
	return !e.locked[at.col] && !e.deleted[at.row-len(e.inserted)]
}

func (r *result) setCell(at cell, v driver.Value) {
	e := r.edits
	if !r.editable(at) {
		return
	}
	if at.row < len(e.inserted) {
		e.inserted[at.row][at.col] = &v
		return
	}
	at.row -= len(e.inserted)
	if r.rows[at.row][at.col] == v {
		delete(e.cells, at)
	} else {
		e.cells[at] = v
	}
}

func (r *result) beginEdit(at cell) {
	e := r.edits
	if !r.editable(at) {
		return
	}
	r.endEdit(true)
	e.editing, e.at, e.focused, e.draft = true, at, false, ""
	if at.row < len(e.inserted) {
		if v := e.inserted[at.row][at.col]; v != nil {
			e.draft = v.Text
		}
		return
	}
	at.row -= len(e.inserted)
	v, ok := e.cells[at]
	if !ok {
		v = r.rows[at.row][at.col]
	}
	e.draft = v.Text
}

// endEdit closes the text input of the cell being edited, keeping what was typed or not.
func (r *result) endEdit(keep bool) {
	e := r.edits
	if e == nil || !e.editing {
		return
	}
	e.editing = false
	if keep {
		r.setCell(e.at, driver.Value{Text: e.draft})
	}
}

func (r *result) addRow() {
	e := r.edits
	r.endEdit(true)
	e.inserted = append([][]*driver.Value{make([]*driver.Value, len(e.columns))}, e.inserted...)
	// The rows chosen are held by their place, which the new row moved.
	r.chosen.Clear()
	r.row = -1
	r.table.ScrollTo(0, ui.Start)
}

// deleteRows marks the rows chosen, or the row clicked when it is not one of them, as deleted, or as kept when the row clicked was deleted.
// A new row is dropped instead.
func (r *result) deleteRows(clicked int) {
	e := r.edits
	r.endEdit(true)
	rows := []int{clicked}
	if r.chosen.Has(clicked) {
		rows = slices.Sorted(r.chosen.All())
	}
	remove := !e.deleted[clicked-len(e.inserted)]
	created := len(e.inserted)
	for _, row := range slices.Backward(rows) {
		switch {
		case row >= r.shown():
		case row >= len(e.inserted) && remove:
			e.deleted[row-len(e.inserted)] = true
		case row >= len(e.inserted):
			delete(e.deleted, row-len(e.inserted))
		case remove:
			e.inserted = slices.Delete(e.inserted, row, row+1)
		}
	}
	if len(e.inserted) != created {
		r.chosen.Clear()
		r.row = -1
	}
}

// cellView builds the cell at, of the rows shown.
func (p *QueryPane) cellView(c *ui.Context, r *result, at cell) {
	t := c.Theme()
	e := r.edits
	if e != nil && e.editing && e.at == at {
		in := ui.TextInput(c, &e.draft).Label(e.columns[at.col]).AutoFocus().Grow(1).MinWidth(0)
		switch {
		case in.Submitted():
			r.endEdit(true)
			r.table.Focus(c)
		case in.Shortcut(0, ui.KeyEscape):
			r.endEdit(false)
			r.table.Focus(c)
		case in.Focused():
			e.focused = true
		case e.focused:
			r.endEdit(true)
		}
		return
	}

	created := 0
	if e != nil {
		created = len(e.inserted)
	}
	var v driver.Value
	color := t.Text
	deleted := false
	if at.row < created {
		set := e.inserted[at.row][at.col]
		if set == nil {
			ui.Text(c, "DEFAULT").Italic().TextColor(t.Success).Font(monospace).FontSize(smallText).SingleLine()
			return
		}
		v, color = *set, t.Success
	} else {
		at.row -= created
		v = r.rows[at.row][at.col]
		if e != nil {
			if edited, ok := e.cells[at]; ok {
				v, color = edited, t.Warning
			}
			if deleted = e.deleted[at.row]; deleted {
				color = t.Danger
			}
		}
		at.row += created
	}
	text := ui.Text(c, v.Text)
	if v.Null {
		text = ui.Text(c, "NULL").Italic()
		if color == t.Text {
			color = t.TextMuted
		}
	}
	if deleted {
		text.Strikethrough()
	}
	text.TextColor(color).Font(monospace).FontSize(smallText).SingleLine()
}

// cellMenu is the context menu of the cells of a result that can be edited.
func (p *QueryPane) cellMenu(r *result) func(m *ui.Menu, row, col int) {
	return func(m *ui.Menu, row, col int) {
		e := r.edits
		at := cell{row, col}
		if m.Item("Edit").Disabled(!r.editable(at)).Chosen() {
			r.beginEdit(at)
		}
		if m.Item("Set NULL").Disabled(!r.editable(at)).Chosen() {
			r.endEdit(false)
			r.setCell(at, driver.Value{Null: true})
		}
		m.Separator()
		if m.Item("Add Row").Chosen() {
			r.addRow()
		}
		if e.deleted[row-len(e.inserted)] {
			if m.Item("Restore Row").Chosen() {
				r.deleteRows(row)
			}
		} else if m.Item("Delete Row").Danger().Chosen() {
			r.deleteRows(row)
		}
	}
}

// editBar builds the buttons that add a row and that commit or discard the edits, at the end of the status bar.
func (p *QueryPane) editBar(c *ui.Context, r *result) {
	t := c.Theme()
	e := r.edits
	ui.Button(c, "Add Row").Height(24).OnClick(r.addRow)
	n := e.count()
	if n == 0 {
		return
	}
	label := "1 change"
	if n > 1 {
		label = group(n) + " changes"
	}
	ui.Text(c, label).TextColor(t.Warning).SingleLine()
	ui.Button(c, "Discard").Height(24).OnClick(e.reset)
	ui.PrimaryButton(c, "Commit").Height(24).Tooltip("Commit the changes to the table").OnClick(p.commit)
}

// commit writes the edits of the result shown to its table in one transaction, and reads the table again.
func (p *QueryPane) commit() {
	r := p.result
	if r == nil || r.edits.count() == 0 || p.busy() {
		return
	}
	e := r.edits
	r.endEdit(true)
	e.err = nil
	stmts := e.statements(p.table.drv, r.rows)

	// The session is used for the transaction, so the rows left unread are given up, as Run gives them up.
	old, oldCancel := p.detachStream()
	ctx, cancel := context.WithCancel(context.Background())
	p.running, p.cancel = true, cancel
	go func() {
		p.session.Lock()
		if old != nil {
			oldCancel()
			old.Close()
		}
		err := p.apply(ctx, stmts)
		p.dropDeadConn(err)
		p.session.Unlock()
		p.update(func() {
			p.running = false
			closing := p.closing
			p.settle(r)
			if err != nil {
				e.err = err
				return
			}
			e.reset()
			if !closing {
				p.Run(r.sql)
			}
		})
	}()
}

func (p *QueryPane) apply(ctx context.Context, stmts []statement) error {
	if p.conn == nil {
		conn, err := p.open(ctx)
		if err != nil {
			return err
		}
		p.conn = conn
	}
	if _, err := p.conn.Exec(ctx, "BEGIN"); err != nil {
		return err
	}
	for _, s := range stmts {
		n, err := p.conn.Exec(ctx, s.sql, s.args...)
		// ponytail: a row gone since it was read changes no row and is not reported: MySQL also counts 0 for a row
		// that already had the values. Compare what was read in WHERE to catch edits made elsewhere.
		if err == nil && n > 1 {
			err = fmt.Errorf("%s would change %s rows instead of one", s.sql, strconv.FormatInt(n, 10))
		}
		if err != nil {
			// The cancel that stopped the statement must not stop its rollback.
			undo, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			p.conn.Exec(undo, "ROLLBACK")
			cancel()
			return err
		}
	}
	_, err := p.conn.Exec(ctx, "COMMIT")
	return err
}
