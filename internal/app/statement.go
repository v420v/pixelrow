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

