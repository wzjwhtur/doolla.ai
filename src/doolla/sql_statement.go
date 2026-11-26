package doolla

import "strings"

// commandWithoutRows distinguishes DML without RETURNING/OUTPUT from queries.
// Ignore quoted strings, identifiers, comments and CTE bodies when reading
// top-level SQL keywords. Unknown statements take the result-set path.
func commandWithoutRows(query string) bool {
	words := topLevelWords(query)
	if len(words) == 0 || words[0] == "EXPLAIN" {
		return false
	}
	dml := false
	for _, w := range words {
		switch w {
		case "SELECT", "VALUES":
			if !dml {
				return false
			}
		case "RETURNING", "OUTPUT":
			return false
		case "INSERT", "UPDATE", "DELETE", "MERGE", "REPLACE":
			dml = true
		}
	}
	return dml
}
func topLevelWords(s string) []string {
	var words []string
	depth := 0
	for i := 0; i < len(s); {
		ch := s[i]
		i++
		switch {
		case ch == '-' && i < len(s) && s[i] == '-':
			if n := strings.IndexByte(s[i:], '\n'); n >= 0 {
				i += n + 1
			} else {
				return words
			}
		case ch == '/' && i < len(s) && s[i] == '*':
			if n := strings.Index(s[i+1:], "*/"); n >= 0 {
				i += n + 3
			} else {
				return words
			}
		case ch == '\'' || ch == '"' || ch == '`' || ch == '[':
			end := ch
			if ch == '[' {
				end = ']'
			}
			for i < len(s) {
				b := s[i]
				i++
				if b == end {
					if i < len(s) && s[i] == end {
						i++
						continue
					}
					break
				}
			}
		case ch == '(':
			depth++
		case ch == ')':
			if depth > 0 {
				depth--
			}
		case isSQLWord(ch):
			start := i - 1
			for i < len(s) && (isSQLWord(s[i]) || (s[i] >= '0' && s[i] <= '9')) {
				i++
			}
			if depth == 0 {
				words = append(words, strings.ToUpper(s[start:i]))
			}
		}
	}
	return words
}
func isSQLWord(c byte) bool { return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c == '_' }
