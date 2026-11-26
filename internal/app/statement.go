package app

import "strings"

// streamable reports whether sql is a single statement that only reads, so that its rows can be read a page at a time.
func streamable(sql string) bool {
	if hasDollarQuote(sql) {
		return false
	}
	var code strings.Builder
	ended := false
	for i := 0; i < len(sql); {
		c := sql[i]
		switch {
		case c == '-' && strings.HasPrefix(sql[i:], "--"):
			end := strings.IndexByte(sql[i:], '\n')
			if end < 0 {
				end = len(sql) - i
			}
			i += end
			continue
		// MySQL runs what /*! and /*+ comments hold, so they are read as code.
		case c == '/' && strings.HasPrefix(sql[i:], "/*") && !strings.HasPrefix(sql[i:], "/*!") && !strings.HasPrefix(sql[i:], "/*+"):
			end := strings.Index(sql[i+2:], "*/")
			if end < 0 {
				return false
			}
			i += 2 + end + 2
			code.WriteByte(' ')
			continue
		case c == '\'' || c == '"' || c == '`':
			// Backslash escapes are not followed: where they would matter, the scan ends up unsure rather than wrong.
			end := strings.IndexByte(sql[i+1:], c)
			if end < 0 || ended {
				return false
			}
			i += 1 + end + 1
			code.WriteByte(' ')
			continue
		case c == ';':
			ended = true
		case ended && !isSpace(c):
			return false
		default:
			code.WriteByte(lower(c))
		}
		i++
	}

	words := strings.FieldsFunc(code.String(), func(r rune) bool { return r < 'a' || r > 'z' })
	if len(words) == 0 {
		return false
	}
	switch words[0] {
	case "select", "table", "values", "show":
		return true
	case "with":
		// A WITH query can hold statements that write.
		for _, w := range words {
			switch w {
			case "insert", "update", "delete", "merge":
				return false
			}
		}
		return true
	}
	return false
}

// hasDollarQuote reports whether sql may hold a PostgreSQL dollar-quoted string, such as $$…$$ or $body$…$body$.
func hasDollarQuote(sql string) bool {
	for i := 0; i < len(sql); i++ {
		if sql[i] != '$' {
			continue
		}
		j := i + 1
		for j < len(sql) && (lower(sql[j]) >= 'a' && lower(sql[j]) <= 'z' || sql[j] == '_') {
			j++
		}
		if j < len(sql) && sql[j] == '$' {
			return true
		}
	}
	return false
}

func isSpace(c byte) bool { return c == ' ' || c == '\t' || c == '\n' || c == '\r' }

func lower(c byte) byte {
	if c >= 'A' && c <= 'Z' {
		return c + 'a' - 'A'
	}
	return c
}

// span is a range of bytes of a text.
type span struct{ start, end int }

// statements returns where each statement of sql is, without the semicolon that ends it or the space around it.
//
// A semicolon ends a statement only outside strings, quoted names, comments and PostgreSQL's dollar-quoted bodies.
// Backslash escapes are not followed, as the two databases disagree on them: where one matters, MySQL gets a statement cut short inside a string, which it refuses rather than runs.
func statements(sql string) []span {
	var out []span
	start, code := 0, false
	end := func(at int) {
		// What holds only space or comments is no statement.
		if code {
			out = append(out, trim(sql, span{start, at}))
		}
		start, code = at+1, false
	}
	for i := 0; i < len(sql); {
		c := sql[i]
		switch {
		case c == '-' && strings.HasPrefix(sql[i:], "--"):
			n := strings.IndexByte(sql[i:], '\n')
			if n < 0 {
				n = len(sql) - i
			}
			i += n
		case c == '/' && strings.HasPrefix(sql[i:], "/*"):
			n := strings.Index(sql[i+2:], "*/")
			if n < 0 {
				i = len(sql)
				break
			}
			i += n + 4
		case c == '\'' || c == '"' || c == '`':
			code = true
			n := strings.IndexByte(sql[i+1:], c)
			if n < 0 {
				i = len(sql)
				break
			}
			i += n + 2
		case c == '$' && (i == 0 || !isNameByte(sql[i-1])):
			code = true
			// $tag$ opens a body that runs to the same tag, semicolons and all.
			j := i + 1
			for j < len(sql) && isNameByte(sql[j]) && sql[j] != '$' {
				j++
			}
			if j >= len(sql) || sql[j] != '$' {
				i++
				break
			}
			tag := sql[i : j+1]
			n := strings.Index(sql[j+1:], tag)
			if n < 0 {
				i = len(sql)
				break
			}
			i = j + 1 + n + len(tag)
		case c == ';':
			end(i)
			i++
		default:
			code = code || !isSpace(c)
			i++
		}
	}
	end(len(sql))
	return out
}

func trim(sql string, s span) span {
	for s.start < s.end && isSpace(sql[s.start]) {
		s.start++
	}
	for s.end > s.start && isSpace(sql[s.end-1]) {
		s.end--
	}
	return s
}

// statementAt returns the statement the caret is in. A caret between two statements is in the one before it
// when it is on that statement's last line, as just after its semicolon, and in the one after it otherwise.
func statementAt(sql string, caret int) (span, bool) {
	all := statements(sql)
	for i, s := range all {
		if caret <= s.end || i == len(all)-1 {
			return s, true
		}
		if caret < all[i+1].start && !strings.Contains(sql[s.end:caret], "\n") {
			return s, true
		}
	}
	return span{}, false
}
