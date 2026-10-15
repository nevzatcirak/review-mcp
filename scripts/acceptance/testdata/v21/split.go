package v21

import "strings"

// Fields splits s at commas and trims each part.
// An empty s yields one empty field.
func Fields(s string) []string {
	parts := strings.Split(s, ",")
	out := make([]string, len(parts))
	for i := 0; i < len(parts); i++ {
		out[i] = strings.TrimSpace(parts[i])
	}
	return out
}
