package config

import (
	"strings"

	"github.com/nevzatcirak/review-mcp/internal/logging"
)

// Origin says which layer supplied the effective value of a key.
type Origin string

// Origins, in increasing precedence.
const (
	OriginDefault Origin = "default"
	OriginFile    Origin = "file"
	OriginEnv     Origin = "env"
	// OriginFlag marks serve.listen when the serve command's --listen flag
	// set it.
	OriginFlag Origin = "flag"
)

// Report accompanies a loaded Config.
type Report struct {
	// Warnings are non-fatal findings (unknown REVIEW_MCP_* variables,
	// disabled TLS verification, tokens for disabled providers).
	Warnings []string
	// Sources maps every §5 key to the layer that supplied its effective
	// value. Secret keys appear only when set (always OriginEnv).
	Sources map[string]Origin
}

func newReport() *Report {
	r := &Report{Sources: make(map[string]Origin, len(table))}
	for _, e := range table {
		r.Sources[e.key] = OriginDefault
	}
	return r
}

// ValidationError aggregates every problem found while loading.
type ValidationError struct {
	Problems []string
}

// Error joins all problems; it never contains secret values.
func (e *ValidationError) Error() string {
	return "invalid configuration: " + strings.Join(e.Problems, "; ")
}

// sanitize is a last line of defence: problems are built without secret
// values, and this strips any URL userinfo or Authorization value that might
// still have slipped in.
func sanitize(s string) string { return logging.RedactText(s) }
