package yamlrepair

import (
	"fmt"
	"strings"
)

// IsNo reports whether a parsed value means "no" (upstream's is_value_no),
// for fields such as security_concerns that answer either "No" or a text.
//
// It accepts the boolean false and the strings "false", "no" and "none" in
// any case, with surrounding whitespace. The prompt asks for the literal
// English "No" under every output language, which PyYAML reads as false
// and yaml.v3 as the string "No"; both are accepted. Like upstream, an
// absent or empty value (nil, "", a zero number, an empty list or mapping)
// also counts as "no". A string of only whitespace does not.
func IsNo(v any) bool {
	switch x := v.(type) {
	case nil:
		return true
	case bool:
		return !x
	case string:
		if x == "" {
			return true
		}
		switch strings.ToLower(pyStrip(x)) {
		case "no", "none", "false":
			return true
		}
		return false
	case int:
		return x == 0
	case int64:
		return x == 0
	case uint64:
		return x == 0
	case float64:
		return x == 0
	case []any:
		return len(x) == 0
	case map[string]any:
		return len(x) == 0
	}
	switch strings.ToLower(pyStrip(fmt.Sprint(v))) {
	case "no", "none", "false":
		return true
	}
	return false
}
