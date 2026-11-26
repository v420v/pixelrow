package app

import (
	"context"
	"slices"
	"strings"
	"time"

	"github.com/egoist/mygo/ui/editor"

	"db/internal/driver"
)

// completion is what the text around the caret asks for.
type completion struct {
	// start is where the word being typed begins.
	start  int
	prefix string
	// qualifier holds the names before the word, joined to it by dots, as u in u.name.
	qualifier []string
	// tables says that a table is expected, as after FROM, rather than a column or a keyword.
	tables bool
	// refs are the tables the statement names, with their aliases.
	refs []sqlRef
}

type sqlRef struct {
	schema, table, alias string
}

type sqlToken struct {
	text       string
	start, end int
	// word marks a name or a keyword written bare, and quoted a name in quotes, which is never a keyword.
	word, quoted bool
}

// tokenize splits sql into names and punctuation. Comments and string literals are passed over, as nothing in them is a name.
func tokenize(sql string) []sqlToken {
	var tokens []sqlToken
	for i := 0; i < len(sql); {
		c := sql[i]
		switch {
		case isSpace(c):
			i++
		case c == '-' && strings.HasPrefix(sql[i:], "--"):
			end := strings.IndexByte(sql[i:], '\n')
			if end < 0 {
				return tokens
			}
			i += end
		case c == '/' && strings.HasPrefix(sql[i:], "/*"):
			end := strings.Index(sql[i+2:], "*/")
			if end < 0 {
				return tokens
			}
			i += end + 4
		case c == '\'':
			end := strings.IndexByte(sql[i+1:], '\'')
			if end < 0 {
				return tokens
			}
			i += end + 2
		case c == '"' || c == '`':
			end := strings.IndexByte(sql[i+1:], c)
			if end < 0 {
				return tokens
			}
			tokens = append(tokens, sqlToken{text: sql[i+1 : i+1+end], start: i, end: i + end + 2, quoted: true})
			i += end + 2
		case isNameByte(c):
			j := i
			for j < len(sql) && isNameByte(sql[j]) {
				j++
			}
			tokens = append(tokens, sqlToken{text: sql[i:j], start: i, end: j, word: true})
			i = j
		default:
			tokens = append(tokens, sqlToken{text: sql[i : i+1], start: i, end: i + 1})
			i++
		}
	}
	return tokens
}

// isNameByte reports whether c can be part of a name written without quotes. Bytes of other scripts count, as names in them need no quotes.
func isNameByte(c byte) bool {
	return c == '_' || c == '$' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= 0x80
}

func (t sqlToken) is(keyword string) bool {
	return t.word && strings.EqualFold(t.text, keyword)
}

// name reports whether the token is a name: quoted, or a bare word that is not a keyword.
func (t sqlToken) name() bool {
	return t.quoted || t.word && !sqlKeywords[strings.ToLower(t.text)]
}

func analyze(sql string, offset int) completion {
	c := completion{start: offset}
	for c.start > 0 && isNameByte(sql[c.start-1]) {
		c.start--
	}
	c.prefix = sql[c.start:offset]

	// Only the statement the caret is in counts: its neighbors name other tables.
	all := tokenize(sql)
	first, last := 0, len(all)
	for i, t := range all {
		if t.text != ";" || t.word || t.quoted {
			continue
		}
		if t.end <= c.start {
			first = i + 1
		} else if last == len(all) {
			last = i
		}
	}
	statement := all[first:last]

	before := statement
	if i := slices.IndexFunc(statement, func(t sqlToken) bool { return t.end > c.start }); i >= 0 {
		before = statement[:i]
	}
	for len(before) >= 2 && before[len(before)-1].text == "." && before[len(before)-2].name() {
		c.qualifier = append([]string{before[len(before)-2].text}, c.qualifier...)
		before = before[:len(before)-2]
	}

	if n := len(before); n > 0 && len(c.qualifier) == 0 {
		prev := before[n-1]
		switch {
		case prev.is("from"), prev.is("join"), prev.is("into"), prev.is("update"), prev.is("table"), prev.is("truncate"), prev.is("describe"), prev.is("desc"):
			c.tables = true
		case prev.text == "," && !prev.word:
			// A comma continues a list of tables only in FROM: the nearest of the words that start a part of the statement tells.
			for i := n - 2; i >= 0; i-- {
				if t := before[i]; t.word && clauseStarts[strings.ToLower(t.text)] {
					c.tables = t.is("from")
					break
				}
			}
		}
	}

	for i, t := range statement {
		if !(t.is("from") || t.is("join") || t.is("update") || t.is("into")) {
			continue
		}
		list := t.is("from")
		for j := i + 1; j < len(statement); {
			if !statement[j].name() {
				break
			}
			ref := sqlRef{table: statement[j].text}
			j++
			if j < len(statement) && statement[j].text == "." {
				// A dot with nothing after it is a schema whose table is still being typed.
				if j+1 >= len(statement) || !statement[j+1].name() {
					break
				}
				ref.schema, ref.table = ref.table, statement[j+1].text
				j += 2
			}
			if j < len(statement) && statement[j].is("as") {
				j++
			}
			if j < len(statement) && statement[j].name() {
				ref.alias = statement[j].text
				j++
			}
			// The word being typed is not yet a table of the statement.
			if statement[j-1].end != offset || ref.alias != "" && ref.table != c.prefix {
				c.refs = append(c.refs, ref)
			}
			if !list || j >= len(statement) || statement[j].text != "," {
				break
			}
			j++
		}
	}
	return c
}

// clauseStarts are the words that begin a part of a statement, which a list after them belongs to.
var clauseStarts = map[string]bool{
	"select": true, "from": true, "where": true, "set": true, "values": true, "by": true, "having": true,
	"on": true, "using": true, "returning": true, "into": true, "update": true, "limit": true,
}

var keywordList = strings.Fields(`
	select from where and or not in is null like ilike between exists case when then else end as distinct
	join inner left right full outer cross on using group by having order asc desc limit offset union all intersect except
	insert into values update set delete returning create table view index alter drop truncate add column primary key foreign references
	unique default constraint if with recursive explain analyze begin commit rollback true false
	count sum avg min max coalesce nullif cast now current_date current_timestamp describe show`)

var sqlKeywords = func() map[string]bool {
	m := map[string]bool{}
	for _, k := range keywordList {
		m[k] = true
	}
	return m
}()

// catalog holds the tables and columns of one connection for completing them, read once per schema.
type catalog struct {
	dial   func(ctx context.Context) (driver.Conn, error)
	update func(fn func())
	drv    driver.Driver
	// defaultSchema is where a table named without a schema is looked up.
	defaultSchema string

	schemas []string
	listed  bool
	tables  map[string][]driver.CatalogTable
	// waiting holds what to run once a schema has been read, by schema. A schema with an entry is being read.
	waiting map[string][]func()
}

// ready reports whether every schema named has been read, and reads those that have not, running then once they have.
// A schema that cannot be read counts as read and empty, so a failure does not ask again on every key.
func (c *catalog) ready(schemas []string, then func()) bool {
	missing := 0
	for _, schema := range schemas {
		if _, ok := c.tables[schema]; ok || schema == "" {
			continue
		}
		missing++
		reading := c.waiting[schema] != nil
		if c.waiting == nil {
			c.waiting = map[string][]func(){}
		}
		c.waiting[schema] = append(c.waiting[schema], then)
		if reading {
			continue
		}
		go func() {
			names, tables := c.read(schema)
			c.update(func() {
				if c.tables == nil {
					c.tables = map[string][]driver.CatalogTable{}
				}
				c.tables[schema] = tables
				if names != nil {
					c.schemas, c.listed = names, true
				}
				run := c.waiting[schema]
				delete(c.waiting, schema)
				// Several schemas can be awaited by one request, which runs when the last of them is in.
				if len(run) > 0 {
					run[len(run)-1]()
				}
			})
		}()
	}
	return missing == 0
}

// read runs off the UI thread.
func (c *catalog) read(schema string) (names []string, tables []driver.CatalogTable) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	conn, err := c.dial(ctx)
	if err != nil {
		return nil, nil
	}
	defer conn.Close()
	if !c.listed {
		names, _ = conn.Schemas(ctx)
	}
	tables, _ = conn.Catalog(ctx, schema)
	return names, tables
}

func (c *catalog) table(schema, name string) *driver.CatalogTable {
	tables := c.tables[schema]
	if i := slices.IndexFunc(tables, func(t driver.CatalogTable) bool { return strings.EqualFold(t.Name, name) }); i >= 0 {
		return &tables[i]
	}
	return nil
}

// ident writes name as SQL takes it: bare when it is a plain lower-case name, and quoted when anything else could change its meaning.
func (c *catalog) ident(name string) string {
	plain := name != "" && !sqlKeywords[name] && !(name[0] >= '0' && name[0] <= '9')
	for i := 0; plain && i < len(name); i++ {
		b := name[i]
		plain = b == '_' || b >= '0' && b <= '9' || b >= 'a' && b <= 'z'
	}
	if plain {
		return name
	}
	return c.drv.QuoteIdent(name)
}

// maxCompletions caps what is handed to the editor, which ranks every item it is given on each key.
const maxCompletions = 400

// complete offers what fits the caret at offset of text, through show, at once or when the schemas it needs have been read.
func (c *catalog) complete(text string, offset int, show func(start int, items []editor.Completion)) {
	at := analyze(text, offset)
	schemaOf := func(ref sqlRef) string {
		if ref.schema != "" {
			return ref.schema
		}
		return c.defaultSchema
	}

	// A qualifier is an alias, a table, or a schema, in that order of likelihood, and only a schema needs reading.
	need := []string{c.defaultSchema}
	for _, ref := range at.refs {
		need = append(need, schemaOf(ref))
	}
	var qualified *sqlRef
	if n := len(at.qualifier); n > 0 {
		name := at.qualifier[n-1]
		for i := range at.refs {
			if ref := &at.refs[i]; strings.EqualFold(ref.alias, name) || ref.alias == "" && strings.EqualFold(ref.table, name) {
				qualified = ref
			}
		}
		if qualified == nil && n == 1 {
			need = append(need, name)
		}
		if n == 2 {
			need = append(need, at.qualifier[0])
		}
	}
	if !c.ready(need, func() { c.complete(text, offset, show) }) {
		return
	}

	var items []editor.Completion
	add := func(item editor.Completion) {
		if len(items) < maxCompletions && (at.prefix == "" || strings.Contains(strings.ToLower(item.Label), strings.ToLower(at.prefix[:1]))) {
			items = append(items, item)
		}
	}
	columns := func(schema, table, from string) {
		t := c.table(schema, table)
		if t == nil {
			return
		}
		for _, col := range t.Columns {
			add(editor.Completion{Label: col.Name, Kind: editor.CompletionField, Detail: col.Type + " · " + from, Insert: c.ident(col.Name)})
		}
	}
	tables := func(schema string) {
		for _, t := range c.tables[schema] {
			detail := "table"
			if t.Kind != driver.KindTable {
				detail = "view"
			}
			if schema != c.defaultSchema {
				detail += " · " + schema
			}
			add(editor.Completion{Label: t.Name, Kind: editor.CompletionType, Detail: detail, Insert: c.ident(t.Name)})
		}
	}
	schemas := func() {
		for _, schema := range c.schemas {
			add(editor.Completion{Label: schema, Kind: editor.CompletionModule, Detail: "schema", Insert: c.ident(schema)})
		}
	}

	switch n := len(at.qualifier); {
	case qualified != nil:
		columns(schemaOf(*qualified), qualified.table, qualified.table)
	case n == 2:
		columns(at.qualifier[0], at.qualifier[1], at.qualifier[1])
	case n == 1 && c.table(c.defaultSchema, at.qualifier[0]) != nil:
		columns(c.defaultSchema, at.qualifier[0], at.qualifier[0])
	case n == 1:
		tables(at.qualifier[0])
	case at.tables:
		tables(c.defaultSchema)
		schemas()
	default:
		// The columns of the tables the statement names come first, then what leads to more of them.
		for _, ref := range at.refs {
			from := ref.table
			if ref.alias != "" {
				from = ref.alias
			}
			columns(schemaOf(ref), ref.table, from)
		}
		for _, ref := range at.refs {
			if ref.alias != "" {
				add(editor.Completion{Label: ref.alias, Kind: editor.CompletionVariable, Detail: "alias of " + ref.table, Insert: c.ident(ref.alias)})
			}
		}
		// Keywords follow the case the word is being typed in.
		upper := at.prefix == "" || at.prefix != strings.ToLower(at.prefix)
		for _, k := range keywordList {
			if upper {
				k = strings.ToUpper(k)
			}
			add(editor.Completion{Label: k, Kind: editor.CompletionKeyword})
		}
		tables(c.defaultSchema)
	}
	show(at.start, items)
}
