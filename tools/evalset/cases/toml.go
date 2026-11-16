package cases

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"
)

// The case files use a small subset of TOML, parsed here so that the
// self-check needs the standard library only:
//
//	# comment
//	key = "basic string"        (escapes \" \\ \n \t \r \uXXXX)
//	key = 'literal string'
//	key = 42
//	key = true
//	key = ["a", "b"]            (one line; strings or integers)
//	[[issue]]                   (the only table header)
//
// Anything else is an error, so a typo cannot slip through as an unknown
// construct.

// value is a parsed TOML value: string, int, bool, []string or []int.
type value any

// table is one TOML table, with the line number of each key for messages.
type table struct {
	values map[string]value
	lines  map[string]int
}

func newTable() *table {
	return &table{values: map[string]value{}, lines: map[string]int{}}
}

// document is a parsed case file: the top-level table and the [[issue]]
// tables in order.
type document struct {
	top    *table
	issues []*table
}

func parseTOML(src string) (*document, error) {
	doc := &document{top: newTable()}
	cur := doc.top
	for i, raw := range strings.Split(strings.ReplaceAll(src, "\r\n", "\n"), "\n") {
		n := i + 1
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "[") && !strings.Contains(line, "=") {
			header := strings.TrimSpace(stripComment(line))
			if header != "[[issue]]" {
				return nil, fmt.Errorf("line %d: unknown table header %s (only [[issue]] is allowed)", n, header)
			}
			cur = newTable()
			doc.issues = append(doc.issues, cur)
			continue
		}
		key, rest, ok := strings.Cut(line, "=")
		if !ok {
			return nil, fmt.Errorf("line %d: want key = value", n)
		}
		key = strings.TrimSpace(key)
		if !isBareKey(key) {
			return nil, fmt.Errorf("line %d: key %q is not a bare key", n, key)
		}
		if _, dup := cur.values[key]; dup {
			return nil, fmt.Errorf("line %d: key %s is set twice", n, key)
		}
		v, tail, err := parseValue(strings.TrimSpace(rest))
		if err != nil {
			return nil, fmt.Errorf("line %d: %s: %w", n, key, err)
		}
		if t := strings.TrimSpace(tail); t != "" && !strings.HasPrefix(t, "#") {
			return nil, fmt.Errorf("line %d: %s: unexpected %q after the value", n, key, t)
		}
		cur.values[key] = v
		cur.lines[key] = n
	}
	return doc, nil
}

func isBareKey(k string) bool {
	if k == "" {
		return false
	}
	for _, r := range k {
		ok := r == '_' || r == '-' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9'
		if !ok {
			return false
		}
	}
	return true
}

// stripComment removes a trailing comment from a line without strings.
func stripComment(s string) string {
	if i := strings.IndexByte(s, '#'); i >= 0 {
		return s[:i]
	}
	return s
}

// parseValue parses one value at the start of s and returns the rest.
func parseValue(s string) (value, string, error) {
	switch {
	case s == "":
		return nil, "", errors.New("missing value")
	case s[0] == '"':
		return parseBasic(s)
	case s[0] == '\'':
		end := strings.IndexByte(s[1:], '\'')
		if end < 0 {
			return nil, "", errors.New("unterminated literal string")
		}
		return s[1 : 1+end], s[2+end:], nil
	case s[0] == '[':
		return parseArray(s)
	case strings.HasPrefix(s, "true"):
		return true, s[4:], nil
	case strings.HasPrefix(s, "false"):
		return false, s[5:], nil
	default:
		end := 0
		for end < len(s) && (s[end] == '-' || s[end] == '+' || s[end] >= '0' && s[end] <= '9') {
			end++
		}
		n, err := strconv.Atoi(s[:end])
		if err != nil || end == 0 {
			return nil, "", fmt.Errorf("unsupported value %q", s)
		}
		return n, s[end:], nil
	}
}

func parseBasic(s string) (value, string, error) {
	var b strings.Builder
	for i := 1; i < len(s); {
		c := s[i]
		switch c {
		case '"':
			return b.String(), s[i+1:], nil
		case '\\':
			if i+1 >= len(s) {
				return nil, "", errors.New("unterminated escape")
			}
			switch e := s[i+1]; e {
			case '"', '\\':
				b.WriteByte(e)
				i += 2
			case 'n':
				b.WriteByte('\n')
				i += 2
			case 't':
				b.WriteByte('\t')
				i += 2
			case 'r':
				b.WriteByte('\r')
				i += 2
			case 'u':
				if i+6 > len(s) {
					return nil, "", errors.New("short \\u escape")
				}
				r, err := strconv.ParseUint(s[i+2:i+6], 16, 16)
				if err != nil {
					return nil, "", fmt.Errorf("bad \\u escape: %w", err)
				}
				b.WriteRune(rune(uint16(r)))
				i += 6
			default:
				return nil, "", fmt.Errorf("unsupported escape \\%c", e)
			}
		default:
			r, size := utf8.DecodeRuneInString(s[i:])
			if r == utf8.RuneError && size == 1 {
				return nil, "", errors.New("invalid UTF-8")
			}
			b.WriteString(s[i : i+size])
			i += size
		}
	}
	return nil, "", errors.New("unterminated string")
}

func parseArray(s string) (value, string, error) {
	rest := strings.TrimSpace(s[1:])
	var strs []string
	var ints []int
	for !strings.HasPrefix(rest, "]") {
		v, tail, err := parseValue(rest)
		if err != nil {
			return nil, "", err
		}
		switch x := v.(type) {
		case string:
			strs = append(strs, x)
		case int:
			ints = append(ints, x)
		default:
			return nil, "", errors.New("arrays hold strings or integers only")
		}
		if strs != nil && ints != nil {
			return nil, "", errors.New("an array mixes strings and integers")
		}
		rest = strings.TrimSpace(tail)
		if strings.HasPrefix(rest, ",") {
			rest = strings.TrimSpace(rest[1:])
			continue
		}
		if !strings.HasPrefix(rest, "]") {
			return nil, "", errors.New("want , or ] in the array")
		}
	}
	tail := rest[1:]
	if ints != nil {
		return ints, tail, nil
	}
	if strs == nil {
		strs = []string{}
	}
	return strs, tail, nil
}
