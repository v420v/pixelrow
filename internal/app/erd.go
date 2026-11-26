package app

import (
	"cmp"
	"math"
	"slices"
	"strings"

	"db/internal/driver"
)

// erModel is the tables of a schema and the foreign keys between them, placed on a plane.
// It is plain data with no elements, so a schema of thousands of tables costs what is in view, not what exists.
type erModel struct {
	tables []*erTable
	edges  []erEdge
	// external counts the foreign keys that point out of the schema, and selfRefs those that point at their own table. Neither gets a line.
	external, selfRefs int
	width, height      float32
}

type erTable struct {
	name string
	cols []erColumn
	// shown are the indexes of the columns drawn: the keys, or all of them.
	shown []int
	x, y  float32
	h     float32

	// out and in are the tables this one refers to and those that refer to it, by index.
	out, in []int
	// layer is the column of its group the table is placed in, counted from the left, and order its place in that column.
	layer, order int
}

type erColumn struct {
	name, typ string
	primary   bool
	// linked marks a column that is part of a foreign key, on either end.
	linked bool
}

// erEdge is a foreign key between two tables: from the column that holds it to the column it points at.
type erEdge struct {
	from, to       int
	fromCol, toCol int
}

// The plane's own units: what a box measures at a zoom of 1.
const (
	erWidth  = 230
	erHeader = 28
	erRow    = 20
	// erFoot is the room under the last row, which keeps its text off the box's edge.
	erFoot   = 6
	erGapX   = 96
	erGapY   = 20
	erMargin = 48
)

func newERModel(d *driver.SchemaDiagram, schema string, allColumns bool) *erModel {
	m := &erModel{}
	index := map[string]int{}
	for i, t := range d.Tables {
		table := &erTable{name: t.Name}
		for _, col := range t.Columns {
			table.cols = append(table.cols, erColumn{name: col.Name, typ: col.Type, primary: col.PrimaryKey})
		}
		m.tables = append(m.tables, table)
		index[t.Name] = i
	}
	seen := map[[2]int]bool{}
	for _, fk := range d.ForeignKeys {
		from, ok := index[fk.Table]
		if !ok {
			continue
		}
		cols := strings.Split(fk.Columns, ", ")
		m.tables[from].link(cols)
		to, ok := index[fk.RefTable]
		switch {
		case fk.RefSchema != schema || !ok:
			m.external++
			continue
		case to == from:
			m.selfRefs++
			m.tables[to].link(strings.Split(fk.RefColumns, ", "))
			continue
		}
		refCols := strings.Split(fk.RefColumns, ", ")
		m.tables[to].link(refCols)
		m.edges = append(m.edges, erEdge{from: from, to: to, fromCol: m.tables[from].column(cols[0]), toCol: m.tables[to].column(refCols[0])})
		// Two foreign keys between the same tables are two lines, and one neighbor to the layout.
		if !seen[[2]int{from, to}] {
			seen[[2]int{from, to}] = true
			m.tables[from].out = append(m.tables[from].out, to)
			m.tables[to].in = append(m.tables[to].in, from)
		}
	}
	m.layout(allColumns)
	return m
}

func (t *erTable) column(name string) int {
	return slices.IndexFunc(t.cols, func(c erColumn) bool { return c.name == name })
}

func (t *erTable) link(names []string) {
	for i := range t.cols {
		if slices.Contains(names, t.cols[i].name) {
			t.cols[i].linked = true
		}
	}
}

func (t *erTable) hidden() int { return len(t.cols) - len(t.shown) }

// rowOf returns the place among the rows drawn of the column at col, or -1 when it is not drawn.
func (t *erTable) rowOf(col int) int { return slices.Index(t.shown, col) }

// layout chooses the columns each box shows and places every box.
//
// Tables joined by foreign keys form a group, laid out in columns so that a table sits to the left of those it refers to.
// The groups are packed in rows, and the tables with no foreign key at all go in a grid below them.
// Every step is linear in the tables and the keys, apart from sorting.
func (m *erModel) layout(allColumns bool) {
	for _, t := range m.tables {
		t.shown = t.shown[:0]
		for i, col := range t.cols {
			if allColumns || col.primary || col.linked {
				t.shown = append(t.shown, i)
			}
		}
		rows := len(t.shown)
		if t.hidden() > 0 {
			rows++
		}
		t.h = erHeader + float32(rows)*erRow + erFoot
	}

	groups, singles := m.groups()
	type placed struct {
		tables []int
		w, h   float32
	}
	var blocks []placed
	var area float64
	for _, g := range groups {
		w, h := m.layoutGroup(g)
		blocks = append(blocks, placed{g, w, h})
		area += float64(w+erGapX) * float64(h+erGapY)
	}
	// The largest groups come first, where the eye starts.
	slices.SortStableFunc(blocks, func(a, b placed) int { return cmp.Compare(float64(b.w)*float64(b.h), float64(a.w)*float64(a.h)) })

	var singlesArea float64
	for _, i := range singles {
		singlesArea += float64(erWidth+erGapY) * float64(m.tables[i].h+erGapY)
	}
	// A row is as wide as a square holding everything would be, stretched to suit a wide window.
	limit := float32(math.Sqrt(area+singlesArea) * 1.5)
	for _, b := range blocks {
		limit = max(limit, b.w)
	}
	limit = max(limit, erWidth)

	x, y, rowHeight := float32(erMargin), float32(erMargin), float32(0)
	m.width = 0
	for _, b := range blocks {
		if x > erMargin && x+b.w > erMargin+limit {
			x, y, rowHeight = erMargin, y+rowHeight+erGapY*3, 0
		}
		for _, i := range b.tables {
			m.tables[i].x += x
			m.tables[i].y += y
		}
		x += b.w + erGapX
		rowHeight = max(rowHeight, b.h)
		m.width = max(m.width, x-erGapX)
	}
	if len(blocks) > 0 {
		y += rowHeight + erGapY*3
	}

	x, rowHeight = erMargin, 0
	for _, i := range singles {
		t := m.tables[i]
		if x > erMargin && x+erWidth > erMargin+limit {
			x, y, rowHeight = erMargin, y+rowHeight+erGapY, 0
		}
		t.x, t.y = x, y
		x += erWidth + erGapY
		rowHeight = max(rowHeight, t.h)
		m.width = max(m.width, x-erGapY)
	}
	m.width += erMargin
	m.height = y + rowHeight + erMargin
}

func (m *erModel) groups() (groups [][]int, singles []int) {
	parent := make([]int, len(m.tables))
	for i := range parent {
		parent[i] = i
	}
	var find func(i int) int
	find = func(i int) int {
		for parent[i] != i {
			parent[i] = parent[parent[i]]
			i = parent[i]
		}
		return i
	}
	for _, e := range m.edges {
		parent[find(e.from)] = find(e.to)
	}
	byRoot := map[int]int{}
	for i, t := range m.tables {
		if len(t.out) == 0 && len(t.in) == 0 {
			singles = append(singles, i)
			continue
		}
		root := find(i)
		g, ok := byRoot[root]
		if !ok {
			g = len(groups)
			byRoot[root] = g
			groups = append(groups, nil)
		}
		groups[g] = append(groups[g], i)
	}
	return groups, singles
}

// layoutGroup places the tables of one group from the origin, and returns the size they take.
func (m *erModel) layoutGroup(group []int) (w, h float32) {
	// depth is how many steps a table is from one that refers to nothing, which is what puts it that many columns to the left.
	// A cycle has no such end: the key that closes it is passed over, so its tables still get columns.
	const unseen, open = -1, -2
	depth := map[int]int{}
	for _, i := range group {
		depth[i] = unseen
	}
	var visit func(i int) int
	visit = func(i int) int {
		if d := depth[i]; d != unseen {
			return d
		}
		depth[i] = open
		d := 0
		for _, to := range m.tables[i].out {
			if next := visit(to); next != open {
				d = max(d, next+1)
			}
		}
		depth[i] = d
		return d
	}
	deepest := 0
	for _, i := range group {
		deepest = max(deepest, visit(i))
	}
	layers := make([][]int, deepest+1)
	for _, i := range group {
		t := m.tables[i]
		t.layer = deepest - depth[i]
		layers[t.layer] = append(layers[t.layer], i)
	}
	for _, layer := range layers {
		slices.SortFunc(layer, func(a, b int) int { return cmp.Compare(m.tables[a].name, m.tables[b].name) })
		for order, i := range layer {
			m.tables[i].order = order
		}
	}

	// Each table moves towards the middle of its neighbors in the columns already settled, which untangles most lines.
	// A few sweeps each way get close to what many more would.
	settle := func(layer []int, neighbors func(t *erTable) []int) {
		at := map[int]float64{}
		for _, i := range layer {
			t := m.tables[i]
			near := neighbors(t)
			if len(near) == 0 {
				at[i] = float64(t.order)
				continue
			}
			var sum float64
			for _, n := range near {
				sum += float64(m.tables[n].order)
			}
			at[i] = sum / float64(len(near))
		}
		slices.SortStableFunc(layer, func(a, b int) int { return cmp.Compare(at[a], at[b]) })
		for order, i := range layer {
			m.tables[i].order = order
		}
	}
	for range 3 {
		for l := 1; l < len(layers); l++ {
			settle(layers[l], func(t *erTable) []int { return t.in })
		}
		for l := len(layers) - 2; l >= 0; l-- {
			settle(layers[l], func(t *erTable) []int { return t.out })
		}
	}

	// A column of hundreds of tables would make the group far taller than wide, so a long one continues in another beside it.
	// The tables of a column still sit to the left of every table they refer to.
	perColumn := max(12, int(math.Sqrt(float64(len(group)))*1.5))
	x := float32(0)
	for _, layer := range layers {
		for start := 0; start < len(layer); start += perColumn {
			if start > 0 {
				x += erWidth + erGapX/3
			}
			y := float32(0)
			for _, i := range layer[start:min(start+perColumn, len(layer))] {
				t := m.tables[i]
				t.x, t.y = x, y
				y += t.h + erGapY
			}
			h = max(h, y-erGapY)
		}
		x += erWidth + erGapX
	}
	return x - erGapX, h
}
