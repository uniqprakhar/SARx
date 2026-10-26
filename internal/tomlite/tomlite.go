// Package tomlite parses the small subset of TOML that sarx configuration files use, so the
// program needs no third-party dependency.
//
// Supported: comments, [table] and [a.b] headers, key = value with dotted keys, basic ("...") and
// literal ('...') strings, integers, booleans, and arrays of scalars (which may span lines).
// Not supported: inline tables, arrays of tables, dates, multi-line strings. Unsupported syntax is
// reported as an error with a line number rather than silently ignored.
package tomlite

import (
	"fmt"
	"strconv"
	"strings"
)

// Parse reads src and returns nested maps. Values are string, int64, bool or []any.
func Parse(src string) (map[string]any, error) {
	root := map[string]any{}
	cur := root
	lines := strings.Split(strings.ReplaceAll(src, "\r\n", "\n"), "\n")
	for i := 0; i < len(lines); i++ {
		lineNo := i + 1
		line := strings.TrimSpace(stripComment(lines[i]))
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "[[") {
			return nil, fmt.Errorf("line %d: arrays of tables ([[...]]) are not supported", lineNo)
		}
		if strings.HasPrefix(line, "[") {
			if !strings.HasSuffix(line, "]") {
				return nil, fmt.Errorf("line %d: malformed table header", lineNo)
			}
			name := strings.TrimSpace(line[1 : len(line)-1])
			t, err := descend(root, splitKey(name))
			if err != nil {
				return nil, fmt.Errorf("line %d: %v", lineNo, err)
			}
			cur = t
			continue
		}
		eq := indexOutsideQuotes(line, '=')
		if eq < 0 {
			return nil, fmt.Errorf("line %d: expected key = value", lineNo)
		}
		key := strings.TrimSpace(line[:eq])
		raw := strings.TrimSpace(line[eq+1:])
		for strings.HasPrefix(raw, "[") && !balanced(raw) {
			i++
			if i >= len(lines) {
				return nil, fmt.Errorf("line %d: unterminated array", lineNo)
			}
			raw += " " + strings.TrimSpace(stripComment(lines[i]))
		}
		val, err := parseValue(raw)
		if err != nil {
			return nil, fmt.Errorf("line %d: %v", lineNo, err)
		}
		parts := splitKey(key)
		t := cur
		if len(parts) > 1 {
			t, err = descend(cur, parts[:len(parts)-1])
			if err != nil {
				return nil, fmt.Errorf("line %d: %v", lineNo, err)
			}
		}
		t[parts[len(parts)-1]] = val
	}
	return root, nil
}

func descend(m map[string]any, parts []string) (map[string]any, error) {
	for _, p := range parts {
		next, ok := m[p]
		if !ok {
			n := map[string]any{}
			m[p] = n
			m = n
			continue
		}
		nm, ok := next.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("%q is already a value, not a table", p)
		}
		m = nm
	}
	return m, nil
}

func splitKey(k string) []string {
	var parts []string
	for _, p := range strings.Split(k, ".") {
		p = strings.TrimSpace(p)
		p = strings.Trim(p, `"'`)
		parts = append(parts, p)
	}
	return parts
}

func stripComment(s string) string {
	var q rune
	esc := false
	for i, r := range s {
		switch {
		case esc:
			esc = false
		case q == '"' && r == '\\':
			esc = true
		case q != 0 && r == q:
			q = 0
		case q == 0 && (r == '"' || r == '\''):
			q = r
		case q == 0 && r == '#':
			return s[:i]
		}
	}
	return s
}

func indexOutsideQuotes(s string, c byte) int {
	var q byte
	esc := false
	for i := 0; i < len(s); i++ {
		ch := s[i]
		switch {
		case esc:
			esc = false
		case q == '"' && ch == '\\':
			esc = true
		case q != 0 && ch == q:
			q = 0
		case q == 0 && (ch == '"' || ch == '\''):
			q = ch
		case q == 0 && ch == c:
			return i
		}
	}
	return -1
}

func balanced(s string) bool {
	depth := 0
	var q byte
	esc := false
	for i := 0; i < len(s); i++ {
		ch := s[i]
		switch {
		case esc:
			esc = false
		case q == '"' && ch == '\\':
			esc = true
		case q != 0 && ch == q:
			q = 0
		case q == 0 && (ch == '"' || ch == '\''):
			q = ch
		case q == 0 && ch == '[':
			depth++
		case q == 0 && ch == ']':
			depth--
		}
	}
	return depth <= 0
}

func parseValue(raw string) (any, error) {
	raw = strings.TrimSpace(raw)
	switch {
	case raw == "":
		return nil, fmt.Errorf("missing value")
	case raw[0] == '"':
		return parseBasicString(raw)
	case raw[0] == '\'':
		if len(raw) < 2 || raw[len(raw)-1] != '\'' {
			return nil, fmt.Errorf("unterminated string")
		}
		return raw[1 : len(raw)-1], nil
	case raw[0] == '[':
		return parseArray(raw)
	case raw[0] == '{':
		return nil, fmt.Errorf("inline tables are not supported; use a [table] section")
	case raw == "true":
		return true, nil
	case raw == "false":
		return false, nil
	}
	n, err := strconv.ParseInt(strings.ReplaceAll(raw, "_", ""), 10, 64)
	if err != nil {
		return nil, fmt.Errorf("unrecognised value %q (strings must be quoted)", raw)
	}
	return n, nil
}

func parseBasicString(raw string) (string, error) {
	var b strings.Builder
	for i := 1; i < len(raw); i++ {
		c := raw[i]
		if c == '"' {
			if strings.TrimSpace(raw[i+1:]) != "" {
				return "", fmt.Errorf("unexpected text after string")
			}
			return b.String(), nil
		}
		if c != '\\' {
			b.WriteByte(c)
			continue
		}
		i++
		if i >= len(raw) {
			break
		}
		switch raw[i] {
		case 'n':
			b.WriteByte('\n')
		case 't':
			b.WriteByte('\t')
		case 'r':
			b.WriteByte('\r')
		case '"':
			b.WriteByte('"')
		case '\\':
			b.WriteByte('\\')
		default:
			return "", fmt.Errorf("unsupported escape \\%c", raw[i])
		}
	}
	return "", fmt.Errorf("unterminated string")
}

func parseArray(raw string) ([]any, error) {
	if !strings.HasSuffix(raw, "]") {
		return nil, fmt.Errorf("unterminated array")
	}
	inner := strings.TrimSpace(raw[1 : len(raw)-1])
	out := []any{}
	for inner != "" {
		inner = strings.TrimSpace(inner)
		var tok string
		switch inner[0] {
		case '"':
			end := -1
			esc := false
			for i := 1; i < len(inner); i++ {
				if esc {
					esc = false
					continue
				}
				if inner[i] == '\\' {
					esc = true
					continue
				}
				if inner[i] == '"' {
					end = i
					break
				}
			}
			if end < 0 {
				return nil, fmt.Errorf("unterminated string in array")
			}
			tok = inner[:end+1]
		case '\'':
			end := strings.IndexByte(inner[1:], '\'')
			if end < 0 {
				return nil, fmt.Errorf("unterminated string in array")
			}
			tok = inner[:end+2]
		case '[', '{':
			return nil, fmt.Errorf("nested arrays and inline tables are not supported")
		default:
			end := strings.IndexByte(inner, ',')
			if end < 0 {
				end = len(inner)
			}
			tok = strings.TrimSpace(inner[:end])
		}
		v, err := parseValue(tok)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
		inner = strings.TrimSpace(inner[len(tok):])
		if strings.HasPrefix(inner, ",") {
			inner = strings.TrimSpace(inner[1:])
		} else if inner != "" {
			return nil, fmt.Errorf("expected , between array items")
		}
	}
	return out, nil
}

// Typed accessors. Each returns the zero value and false when the key is missing or the wrong type.

func String(m map[string]any, k string) (string, bool) { s, ok := m[k].(string); return s, ok }
func Int(m map[string]any, k string) (int, bool) {
	n, ok := m[k].(int64)
	return int(n), ok
}
func Bool(m map[string]any, k string) (bool, bool) { b, ok := m[k].(bool); return b, ok }
func Table(m map[string]any, k string) (map[string]any, bool) {
	t, ok := m[k].(map[string]any)
	return t, ok
}
func Strings(m map[string]any, k string) ([]string, bool) {
	a, ok := m[k].([]any)
	if !ok {
		return nil, false
	}
	out := make([]string, 0, len(a))
	for _, v := range a {
		s, ok := v.(string)
		if !ok {
			return nil, false
		}
		out = append(out, s)
	}
	return out, true
}
