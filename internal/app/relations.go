package app

import (
	"context"
	"slices"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/v420v/pixelrow/internal/driver"
)

// relations draws one table between the tables that refer to it, on its left, and those it refers to, on its right.
type relations struct {
	// dial opens a connection used for one load, so the view never waits for the tab's own session.
	dial   func(ctx context.Context) (driver.Conn, error)
	update func(fn func())
	schema string
	table  string
	// open shows another table of the diagram in a tab of its own.
	open func(schema, table string)
	// trailing builds what goes at the end of the bar, as the switch between the views of a table.
	trailing func(c *ui.Context)

	started bool
	loading bool
	err     error
	diagram *diagram
}

// The diagram is laid out on a fixed grid, which is what lets the lines be drawn to rows without measuring anything.
const (
	boxWidth  = 250
	boxHeader = 30
	rowSpan   = 22
	boxGapX   = 110
	boxGapY   = 22
	boxMargin = 28
	// neighborLimit caps the tables drawn on each side, as a table hundreds refer to would otherwise draw them all.
	neighborLimit = 40
)

type diagram struct {
	boxes []*box
	edges []edge
	// width and height are the size of the canvas that holds the boxes.
	width, height float32
	// in and out count the tables drawn on the left and on the right.
	in, out int
	// left and right count the tables past neighborLimit that were left out on each side.
	left, right int
	// selfRefs counts the foreign keys of the table that point at itself, which get no line.
	selfRefs int
}

type box struct {
	schema, table string
	center        bool
	rows          []boxRow
	// more counts the columns left out of a neighbor, which shows only its key and the columns of the relation.
	more int
	x, y float32
}

type boxRow struct {
	name, typ string
	primary   bool
	// linked marks a column that a line of the diagram ends on.
	linked bool
}

// edge is a foreign key: from the row of the column that holds it to the row of the column it points at.
type edge struct {
	from, to       *box
	fromRow, toRow int
}

func (b *box) height() float32 {
	n := len(b.rows)
	if b.more > 0 {
		n++
	}
	return boxHeader + float32(n)*rowSpan
}

// rowY returns the middle of the row at i, or of the header when the column is not among those shown.
func (b *box) rowY(i int) float32 {
	if i < 0 {
		return b.y + boxHeader/2
	}
	return b.y + boxHeader + (float32(i)+0.5)*rowSpan
}

func (b *box) row(column string) int {
	return slices.IndexFunc(b.rows, func(r boxRow) bool { return r.name == column })
}

func (r *relations) view(c *ui.Context) {
	t := c.Theme()
	if !r.started {
		r.started = true
		r.load()
	}

	ui.Column(c).Fill().Children(func() {
		ui.Row(c).FillWidth().Height(barHeight).Shrink(0).Padding(0, t.Space(2), 0, t.Space(3)).Gap(t.Space(2)).AlignItems(ui.Center).Children(func() {
			ui.Text(c, r.schema+"."+r.table).FontSize(smallText).FontWeight(600).Font(monospace).SingleLine()
			iconButton(c, iconReload, "Reload Relations").Disabled(r.loading).OnClick(r.load)
			if r.loading {
				ui.Spinner(c)
			}
			if d := r.diagram; d != nil {
				ui.Text(c, d.summary()).FontSize(smallText).TextColor(t.TextMuted).SingleLine()
			}
			ui.Spacer(c)
			if r.trailing != nil {
				r.trailing(c)
			}
		})
		hairline(c)
		switch {
		case r.err != nil:
			ui.Text(c, errorText(r.err)).TextColor(t.Danger).Padding(t.Space(4), t.Space(3)).Selectable()
		case r.diagram != nil:
			ui.ScrollBoth(c).Grow(1).FillWidth().Children(func() { r.canvas(c, r.diagram) })
		}
	})
}

func (r *relations) canvas(c *ui.Context, d *diagram) {
	t := c.Theme()
	line := t.TextMuted.Alpha(0.7)
	canvas := ui.Box(c).Size(d.width, d.height).Shrink(0)
	canvas.Draw(func(p *ui.Painter, at ui.Rect) {
		for _, e := range d.edges {
			x0, y0 := at.X+e.from.x+boxWidth, at.Y+e.from.rowY(e.fromRow)
			x1, y1 := at.X+e.to.x, at.Y+e.to.rowY(e.toRow)
			bend := (x1 - x0) / 2
			var path ui.Path
			path.MoveTo(x0, y0).CubeTo(x0+bend, y0, x1-bend, y1, x1, y1)
			p.StrokePath(&path, 1.25, line)
			// The dot is on the column pointed at, which tells the direction where an arrowhead would crowd the rows.
			var dot ui.Path
			dot.Circle(x1, y1, 3)
			p.FillPath(&dot, line)
		}
	})
	canvas.Children(func() {
		for i, b := range d.boxes {
			c.Key(i)
			r.boxView(c, b)
		}
	})
}

func (r *relations) boxView(c *ui.Context, b *box) {
	t := c.Theme()
	border := t.Border
	if b.center {
		border = t.Accent.Alpha(0.6)
	}
	frame := ui.Column(c).Absolute().Left(b.x).Top(b.y).Width(boxWidth).Radius(6).Border(1, border).Background(t.Background)
	frame.Children(func() {
		header := ui.Row(c).FillWidth().Height(boxHeader).Padding(0, t.Space(2.5)).Gap(t.Space(1.5)).AlignItems(ui.Center).Background(t.Surface).Radius(5, 5, 0, 0)
		if !b.center {
			header.Tooltip("Open " + b.schema + "." + b.table)
			switch {
			case header.Clicked():
				r.open(b.schema, b.table)
			case header.Hovered():
				header.Background(t.SurfaceHover)
			}
		}
		header.Children(func() {
			ui.Text(c, b.table).FontSize(smallText).FontWeight(600).Font(monospace).SingleLine().Shrink(1).MinWidth(0)
			if b.schema != r.schema {
				ui.Text(c, b.schema).FontSize(tinyText).TextColor(t.TextMuted).SingleLine()
			}
		})
		for _, row := range b.rows {
			ui.Row(c).FillWidth().Height(rowSpan).Padding(0, t.Space(2.5)).Gap(t.Space(2)).AlignItems(ui.Center).Children(func() {
				name := ui.Text(c, row.name).FontSize(smallText).Font(monospace).SingleLine().Shrink(1).MinWidth(0)
				switch {
				case row.primary:
					name.FontWeight(600)
				case !row.linked && !b.center:
					name.TextColor(t.TextMuted)
				}
				if row.primary {
					ui.Text(c, "PK").FontSize(tinyText).FontWeight(600).TextColor(t.Accent).SingleLine()
				}
				ui.Spacer(c)
				ui.Text(c, row.typ).FontSize(tinyText).TextColor(t.TextMuted).SingleLine().Shrink(2).MinWidth(0)
			})
		}
		if b.more > 0 {
			ui.Row(c).FillWidth().Height(rowSpan).Padding(0, t.Space(2.5)).AlignItems(ui.Center).Children(func() {
				ui.Text(c, count(b.more, "more column")).FontSize(tinyText).TextColor(t.TextMuted).SingleLine()
			})
		}
	})
}

func (d *diagram) summary() string {
	parts := []string{count(d.in+d.left, "table") + " refer to it", "it refers to " + count(d.out+d.right, "table")}
	if d.left+d.right > 0 {
		parts = append(parts, count(d.left+d.right, "table")+" not drawn")
	}
	if d.selfRefs > 0 {
		parts = append(parts, "refers to itself")
	}
	return strings.Join(parts, " · ")
}

func count(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return group(n) + " " + noun + "s"
}

func (r *relations) load() {
	if r.loading {
		return
	}
	r.loading = true
	go func() {
		d, err := r.read()
		r.update(func() {
			r.loading, r.err = false, err
			if err == nil {
				r.diagram = d
			}
		})
	}()
}

// read runs off the UI thread.
func (r *relations) read() (*diagram, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	conn, err := r.dial(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	info, err := conn.Describe(ctx, r.schema, r.table)
	if err != nil {
		return nil, err
	}
	refs, err := conn.References(ctx, r.schema, r.table)
	if err != nil {
		return nil, err
	}

	d := &diagram{}
	center := &box{schema: r.schema, table: r.table, center: true}
	for _, col := range info.Columns {
		center.rows = append(center.rows, boxRow{name: col.Name, typ: col.Type, primary: col.PrimaryKey})
	}

	// neighbor describes a table beside the center once, however many foreign keys join the two.
	type key struct{ schema, table string }
	described := map[key]*driver.TableInfo{}
	neighbor := func(schema, table string, linked []string, side *[]*box, left *int) (*box, error) {
		k := key{schema, table}
		for _, b := range *side {
			if b.schema == schema && b.table == table {
				b.link(described[k], linked)
				return b, nil
			}
		}
		if len(*side) == neighborLimit {
			*left++
			return nil, nil
		}
		if described[k] == nil {
			other, err := conn.Describe(ctx, schema, table)
			if err != nil {
				return nil, err
			}
			described[k] = other
		}
		b := &box{schema: schema, table: table}
		b.link(described[k], linked)
		*side = append(*side, b)
		return b, nil
	}

	var incoming, outgoing []*box
	type pending struct {
		from, to       *box
		fromCol, toCol string
	}
	var joins []pending
	for _, ref := range refs {
		cols, refCols := strings.Split(ref.Columns, ", "), strings.Split(ref.RefColumns, ", ")
		if ref.Schema == r.schema && ref.Table == r.table {
			continue
		}
		b, err := neighbor(ref.Schema, ref.Table, cols, &incoming, &d.left)
		if err != nil {
			return nil, err
		}
		if b != nil {
			joins = append(joins, pending{b, center, cols[0], refCols[0]})
			center.mark(refCols)
		}
	}
	for _, fk := range info.ForeignKeys {
		cols, refCols := strings.Split(fk.Columns, ", "), strings.Split(fk.RefColumns, ", ")
		center.mark(cols)
		if fk.RefSchema == r.schema && fk.RefTable == r.table {
			d.selfRefs++
			continue
		}
		b, err := neighbor(fk.RefSchema, fk.RefTable, refCols, &outgoing, &d.right)
		if err != nil {
			return nil, err
		}
		if b != nil {
			joins = append(joins, pending{center, b, cols[0], refCols[0]})
		}
	}

	x := float32(boxMargin)
	place := func(column []*box) float32 {
		y := float32(boxMargin)
		for _, b := range column {
			b.x, b.y = x, y
			y += b.height() + boxGapY
		}
		return y - boxGapY + boxMargin
	}
	if len(incoming) > 0 {
		d.height = max(d.height, place(incoming))
		x += boxWidth + boxGapX
	}
	d.height = max(d.height, place([]*box{center}))
	if len(outgoing) > 0 {
		x += boxWidth + boxGapX
		d.height = max(d.height, place(outgoing))
	}
	d.width = x + boxWidth + boxMargin
	d.in, d.out = len(incoming), len(outgoing)
	d.boxes = append(append(incoming, center), outgoing...)
	// The rows are final only now, as a later foreign key can add columns to a neighbor already drawn.
	for _, j := range joins {
		d.edges = append(d.edges, edge{from: j.from, to: j.to, fromRow: j.from.row(j.fromCol), toRow: j.to.row(j.toCol)})
	}
	return d, nil
}

func (b *box) link(info *driver.TableInfo, columns []string) {
	shown := map[string]bool{}
	for _, row := range b.rows {
		if row.linked {
			shown[row.name] = true
		}
	}
	for _, name := range columns {
		shown[name] = true
	}
	b.rows = b.rows[:0]
	for _, col := range info.Columns {
		if col.PrimaryKey || shown[col.Name] {
			b.rows = append(b.rows, boxRow{name: col.Name, typ: col.Type, primary: col.PrimaryKey, linked: shown[col.Name]})
		}
	}
	b.more = len(info.Columns) - len(b.rows)
}

func (b *box) mark(columns []string) {
	for i := range b.rows {
		if slices.Contains(columns, b.rows[i].name) {
			b.rows[i].linked = true
		}
	}
}
