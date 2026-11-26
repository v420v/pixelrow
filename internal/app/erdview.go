package app

import (
	"context"
	"slices"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/v420v/pixelrow/internal/driver"
)

// erDiagram shows the tables of a schema and their foreign keys on a plane the user pans and zooms.
//
// The plane is painted, not built from elements: each frame draws only the boxes and lines in view,
// and drops detail as they get smaller, so the cost follows what is on screen rather than the size of the schema.
type erDiagram struct {
	// dial opens a connection used for one load.
	dial   func(ctx context.Context) (driver.Conn, error)
	update func(fn func())
	schema string
	// open shows a table of the diagram in a tab of its own.
	open func(table string)

	started bool
	loading bool
	err     error
	// source is what was read, kept to lay it out again when the columns shown change.
	source     *driver.SchemaDiagram
	model      *erModel
	allColumns bool

	// zoom is how many DIPs a unit of the plane takes, and x, y the point of the plane at the canvas's top left corner.
	zoom, x, y float32
	// w and h are the size of the canvas as last painted, and fit asks the next paint to show the whole diagram.
	w, h float32
	fit  bool

	// press follows a press on the canvas: on a table, which it moves, or on the background, which it pans.
	press struct {
		down   bool
		table  int
		x, y   float32
		moved  float32
		header bool
	}
	// inView says which tables the last paint found in view, kept between paints to save allocating it each time.
	inView []bool
	find   string
	// found is the index of the table the search stopped on, or -1.
	found int
}

// Below these zooms a box loses its rows, then its name, and lines lose their curves, then go: what could not be read is not drawn.
// Far out, thousands of boxes are in view at once, and each must cost one rectangle.
const (
	erRowsZoom  = 0.55
	erNamesZoom = 0.3
	erCurveZoom = 0.35
	erLinesZoom = 0.2
	erMinZoom   = 0.02
	erMaxZoom   = 2
	// erOpenZoom is the zoom a diagram too large to show whole opens at, where its keys can be read.
	erOpenZoom = 0.7
)

func (e *erDiagram) view(c *ui.Context) {
	t := c.Theme()
	if !e.started {
		e.started, e.found = true, -1
		e.load()
	}

	ui.Column(c).Fill().Children(func() {
		ui.Row(c).FillWidth().Height(barHeight).Shrink(0).Padding(0, t.Space(2), 0, t.Space(3)).Gap(t.Space(2)).AlignItems(ui.Center).Children(func() {
			ui.Text(c, e.schema).FontSize(smallText).FontWeight(600).Font(monospace).SingleLine()
			iconButton(c, iconReload, "Reload Diagram").Disabled(e.loading).OnClick(e.load)
			if e.loading {
				ui.Spinner(c)
			}
			if m := e.model; m != nil {
				ui.Text(c, m.summary()).FontSize(smallText).TextColor(t.TextMuted).SingleLine().Shrink(1).MinWidth(0)
			}
			ui.Spacer(c)
			if ui.TextInput(c, &e.find).Placeholder("Find table").Label("Find table").Width(170).Height(24).FontSize(smallText).Changed() {
				e.search()
			}
			toggleButton(c, iconFields, "All Columns", e.allColumns).OnClick(e.toggleColumns)
			iconButton(c, iconMinus, "Zoom Out").OnClick(func() { e.zoomAt(e.w/2, e.h/2, 1/1.3) })
			iconButton(c, iconPlus, "Zoom In").OnClick(func() { e.zoomAt(e.w/2, e.h/2, 1.3) })
			iconButton(c, iconFit, "Fit Diagram").OnClick(func() { e.fit = true })
		})
		hairline(c)
		switch {
		case e.err != nil:
			ui.Text(c, errorText(e.err)).TextColor(t.Danger).Padding(t.Space(4), t.Space(3)).Selectable()
		case e.model != nil && len(e.model.tables) == 0:
			ui.Text(c, "This schema has no tables.").FontSize(smallText).TextColor(t.TextMuted).Padding(t.Space(4), t.Space(3))
		case e.model != nil:
			ui.Box(c).Grow(1).FillWidth().Label("Diagram of " + e.schema).Draw(e.paint).HandleInput(e.input)
		}
	})
}

func (m *erModel) summary() string {
	parts := []string{count(len(m.tables), "table"), count(len(m.edges), "relation")}
	if m.external > 0 {
		parts = append(parts, group(m.external)+" to other schemas")
	}
	return strings.Join(parts, " · ")
}

func (e *erDiagram) load() {
	if e.loading {
		return
	}
	e.loading = true
	go func() {
		source, err := e.read()
		var model *erModel
		if err == nil {
			model = newERModel(source, e.schema, e.allColumns)
		}
		e.update(func() {
			e.loading, e.err = false, err
			if err == nil {
				e.source, e.model, e.fit, e.found = source, model, true, -1
			}
		})
	}()
}

func (e *erDiagram) read() (*driver.SchemaDiagram, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	conn, err := e.dial(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	return conn.Diagram(ctx, e.schema)
}

func (e *erDiagram) toggleColumns() {
	e.allColumns = !e.allColumns
	if e.source != nil {
		e.model, e.fit, e.found = newERModel(e.source, e.schema, e.allColumns), true, -1
	}
}

func (e *erDiagram) search() {
	e.found = -1
	text := strings.ToLower(strings.TrimSpace(e.find))
	if text == "" || e.model == nil {
		return
	}
	for i, t := range e.model.tables {
		if strings.Contains(strings.ToLower(t.name), text) {
			e.found = i
			e.zoom = max(e.zoom, 0.9)
			e.x = t.x + erWidth/2 - e.w/2/e.zoom
			e.y = t.y + t.h/2 - e.h/2/e.zoom
			return
		}
	}
}

// zoomAt scales the plane by factor, keeping the point under (px, py) of the canvas where it is.
func (e *erDiagram) zoomAt(px, py, factor float32) {
	zoom := min(max(e.zoom*factor, erMinZoom), erMaxZoom)
	e.x += px/e.zoom - px/zoom
	e.y += py/e.zoom - py/zoom
	e.zoom = zoom
}

func (e *erDiagram) input(ev ui.InputEvent) bool {
	m := e.model
	if m == nil || e.zoom == 0 {
		return false
	}
	switch ev.Kind {
	case ui.InputZoom:
		e.zoomAt(ev.X, ev.Y, ev.Scale)
	case ui.InputScroll:
		switch {
		// A wheel's notch has no direction to pan in that a touchpad lacks, so it zooms, as in maps.
		case !ev.Precise && ev.DY < 0:
			e.zoomAt(ev.X, ev.Y, 1.15)
		case !ev.Precise && ev.DY > 0:
			e.zoomAt(ev.X, ev.Y, 1/1.15)
		default:
			e.x += ev.DX / e.zoom
			e.y += ev.DY / e.zoom
		}
	case ui.InputPointerDown:
		if ev.Button != 0 {
			return false
		}
		table, header := e.hit(ev.X, ev.Y)
		e.press.down, e.press.table, e.press.header = true, table, header
		e.press.x, e.press.y, e.press.moved = ev.X, ev.Y, 0
	case ui.InputPointerMove:
		if !e.press.down {
			return false
		}
		dx, dy := ev.X-e.press.x, ev.Y-e.press.y
		e.press.x, e.press.y = ev.X, ev.Y
		e.press.moved += abs(dx) + abs(dy)
		if e.press.table >= 0 {
			t := m.tables[e.press.table]
			t.x += dx / e.zoom
			t.y += dy / e.zoom
		} else {
			e.x -= dx / e.zoom
			e.y -= dy / e.zoom
		}
	case ui.InputPointerUp:
		if !e.press.down {
			return false
		}
		e.press.down = false
		// A press let go where it began is a click: on a table's name, it opens the table.
		if !ev.Canceled && e.press.moved < 4 && e.press.table >= 0 && e.press.header {
			e.open(m.tables[e.press.table].name)
		}
	default:
		return false
	}
	return true
}

func abs(v float32) float32 {
	if v < 0 {
		return -v
	}
	return v
}

// hit returns the table under (px, py) of the canvas, or -1, and whether the point is on its name.
// The tables drawn last are on top, so they are tried first.
func (e *erDiagram) hit(px, py float32) (table int, header bool) {
	wx, wy := e.x+px/e.zoom, e.y+py/e.zoom
	for i, t := range slices.Backward(e.model.tables) {
		if wx >= t.x && wx < t.x+erWidth && wy >= t.y && wy < t.y+t.h {
			return i, wy < t.y+erHeader
		}
	}
	return -1, false
}

func (e *erDiagram) paint(p *ui.Painter, r ui.Rect) {
	m := e.model
	if m == nil {
		return
	}
	e.w, e.h = r.W, r.H
	if e.fit || e.zoom == 0 {
		e.fit = false
		// The whole diagram, unless that makes even its names unreadable: then it starts at its top left corner.
		e.zoom = min(r.W/m.width, r.H/m.height, 1)
		e.x, e.y = (m.width-r.W/e.zoom)/2, (m.height-r.H/e.zoom)/2
		if e.zoom < erNamesZoom {
			e.zoom, e.x, e.y = erOpenZoom, 0, 0
		}
	}
	t := p.Theme()
	z := e.zoom
	// The part of the plane in view, which is all that is drawn.
	x0, y0, x1, y1 := e.x, e.y, e.x+r.W/z, e.y+r.H/z
	sx := func(wx float32) float32 { return r.X + (wx-e.x)*z }
	sy := func(wy float32) float32 { return r.Y + (wy-e.y)*z }

	// A line is drawn when a table at one of its ends is in view. One that only crosses the view between two tables
	// elsewhere says nothing that can be followed, and a table thousands refer to would otherwise put them all in every frame.
	if len(e.inView) != len(m.tables) {
		e.inView = make([]bool, len(m.tables))
	}
	for i, table := range m.tables {
		e.inView[i] = table.x+erWidth >= x0 && table.x <= x1 && table.y+table.h >= y0 && table.y <= y1
	}

	p.Clip(r, 0, func() {
		// The lines are opaque, mixed by hand with what is behind them: the GPU draws opaque straight lines without a mask each.
		line := t.Background.Mix(t.TextMuted, 0.6)
		for _, edge := range m.edges {
			if z < erLinesZoom {
				break
			}
			near, far := e.inView[edge.from] && e.inView[edge.to], e.inView[edge.from] || e.inView[edge.to]
			// Zoomed out, hundreds of tables are in view, and lines to all those beyond it would be a gray mass that costs the most to draw.
			if !far || z < erCurveZoom && !near {
				continue
			}
			from, to := m.tables[edge.from], m.tables[edge.to]
			ax, ay := from.x+erWidth, from.anchor(edge.fromCol)
			bx, by := to.x, to.anchor(edge.toCol)
			// The table referred to can have been moved to the left of the other, where the line leaves from the other sides.
			if bx < ax {
				ax, bx = from.x, to.x+erWidth
			}
			// A curve is for following a line between two tables in view. To a table out of view a straight line points the way, and costs far less.
			if z < erCurveZoom || !near {
				p.StrokePolyline([][2]float32{{sx(ax), sy(ay)}, {sx(bx), sy(by)}}, 1, line)
				continue
			}
			bend := (bx - ax) / 2
			var path ui.Path
			path.MoveTo(sx(ax), sy(ay)).CubeTo(sx(ax+bend), sy(ay), sx(bx-bend), sy(by), sx(bx), sy(by))
			p.StrokePath(&path, max(1, 1.25*z), line)
			var dot ui.Path
			dot.Circle(sx(bx), sy(by), 3*z)
			p.FillPath(&dot, line)
		}

		for i, table := range m.tables {
			if e.inView[i] {
				e.paintTable(p, t, table, sx(table.x), sy(table.y), i == e.found)
			}
		}
	})
}

// anchor returns where a line meets the box for the column at col: the middle of its row, or of the name when the row is not drawn.
func (t *erTable) anchor(col int) float32 {
	if row := t.rowOf(col); row >= 0 {
		return t.y + erHeader + (float32(row)+0.5)*erRow
	}
	return t.y + erHeader/2
}

// lineSpan is the height of a line of text as a multiple of its size, which centers text in a row without measuring it.
const lineSpan = 1.5

func (e *erDiagram) paintTable(p *ui.Painter, t *ui.Theme, table *erTable, x, y float32, found bool) {
	z := e.zoom
	box := ui.Rect{X: x, Y: y, W: erWidth * z, H: table.h * z}
	radius := 6 * z
	border, width := t.Border, float32(1)
	if found {
		border, width = t.Accent, 2
	}
	if z < erNamesZoom {
		// A box this small is a mark of where a table is, and a plain rectangle is the cheapest thing to draw.
		fill := t.Background.Mix(t.TextMuted, 0.35)
		if found {
			fill = t.Accent
		}
		p.Fill(box, fill, 0)
		return
	}
	p.Fill(box, t.Background, radius)
	p.Fill(ui.Rect{X: x, Y: y, W: box.W, H: erHeader * z}, t.Surface, radius)
	p.Stroke(box, border, radius, width)

	pad := 10 * z
	// Names are cut to what fits by counting characters, which costs nothing where measuring each would.
	text := func(s string, tx, ty, size float32, weight int, color ui.Color, room float32) {
		if n := int(room / (size * 0.62)); n < len(s) {
			if n < 2 {
				return
			}
			s = s[:n-1] + "…"
		}
		p.RichText(tx, ty, 0, ui.Span{Text: s, Font: monospace, Size: size, Weight: weight, Color: color})
	}
	// A name stays large enough to read while the box shrinks, until it no longer fits.
	nameSize := max(12*z, 9)
	text(table.name, x+pad, y+(erHeader*z-nameSize*lineSpan)/2, nameSize, 600, t.Text, box.W-2*pad)
	if z < erRowsZoom {
		return
	}

	size := 11.5 * z
	for row, col := range table.shown {
		column := table.cols[col]
		ty := y + (erHeader+float32(row)*erRow)*z + (erRow*z-size*lineSpan)/2
		color, weight := t.Text, 400
		switch {
		case column.primary:
			weight = 600
		case !column.linked:
			color = t.TextMuted
		}
		typeRoom := box.W * 0.42
		text(column.name, x+pad, ty, size, weight, color, box.W-2*pad-typeRoom)
		typ := column.typ
		if n := int(typeRoom / (size * 0.62)); n < len(typ) && n >= 2 {
			typ = typ[:n-1] + "…"
		}
		p.RichText(x+box.W-pad-float32(len(typ))*size*0.62, ty, 0, ui.Span{Text: typ, Font: monospace, Size: size, Color: t.TextMuted})
	}
	if hidden := table.hidden(); hidden > 0 {
		ty := y + (erHeader+float32(len(table.shown))*erRow)*z + (erRow*z-size*lineSpan)/2
		text(count(hidden, "more column"), x+pad, ty, size, 400, t.TextMuted, box.W-2*pad)
	}
}
