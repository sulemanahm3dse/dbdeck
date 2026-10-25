package driver

import "strings"

// SplitStatements splits SQL text on semicolons, respecting quotes
// ('...', "...", `...`), -- and /* */ comments, and $tag$...$tag$ bodies.
// Empty statements are dropped.
func SplitStatements(src string) []string {
	var out []string
	var cur strings.Builder
	flush := func() {
		if s := strings.TrimSpace(cur.String()); s != "" && !onlyComments(s) {
			out = append(out, s)
		}
		cur.Reset()
	}
	rs := []rune(src)
	for i := 0; i < len(rs); i++ {
		c := rs[i]
		switch {
		case c == '\'' || c == '"' || c == '`':
			j := i + 1
			for j < len(rs) {
				if rs[j] == '\\' && c != '`' && j+1 < len(rs) {
					j += 2
					continue
				}
				if rs[j] == c {
					if j+1 < len(rs) && rs[j+1] == c { // doubled quote
						j += 2
						continue
					}
					break
				}
				j++
			}
			end := j
			if end >= len(rs) {
				end = len(rs) - 1
			}
			cur.WriteString(string(rs[i : end+1]))
			i = end
		case c == '-' && i+1 < len(rs) && rs[i+1] == '-':
			j := i
			for j < len(rs) && rs[j] != '\n' {
				j++
			}
			cur.WriteString(string(rs[i:j]))
			i = j - 1
		case c == '/' && i+1 < len(rs) && rs[i+1] == '*':
			j := i + 2
			for j+1 < len(rs) && !(rs[j] == '*' && rs[j+1] == '/') {
				j++
			}
			end := j + 1
			if end >= len(rs) {
				end = len(rs) - 1
			}
			cur.WriteString(string(rs[i : end+1]))
			i = end
		case c == '$':
			// dollar-quoted body: $tag$ ... $tag$
			j := i + 1
			for j < len(rs) && (rs[j] == '_' || rs[j] >= 'a' && rs[j] <= 'z' || rs[j] >= 'A' && rs[j] <= 'Z' || rs[j] >= '0' && rs[j] <= '9') {
				j++
			}
			if j < len(rs) && rs[j] == '$' && (j == i+1 || !(rs[i+1] >= '0' && rs[i+1] <= '9')) {
				tag := string(rs[i : j+1])
				rest := string(rs[j+1:])
				if k := strings.Index(rest, tag); k >= 0 {
					n := len([]rune(rest[:k])) + len([]rune(tag))
					cur.WriteString(tag + rest[:k+len(tag)])
					i = j + n
					continue
				}
			}
			cur.WriteRune(c)
		case c == ';':
			flush()
		default:
			cur.WriteRune(c)
		}
	}
	flush()
	return out
}

func onlyComments(s string) bool {
	for _, line := range strings.Split(s, "\n") {
		t := strings.TrimSpace(line)
		if t != "" && !strings.HasPrefix(t, "--") {
			return false
		}
	}
	return true
}
