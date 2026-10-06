// Package prompt holds what the prompt layers of the LLM tools share
// (pr_review now, pr_ask in P5): the text/template engine settings, the
// clock that supplies the prompt date, and upstream's output-language
// instruction (spec P4 §4.2).
//
// Templates are rendered with text/template and missingkey=error, so a
// template that references a variable the caller did not set fails to
// render instead of printing "<no value>". Template data must therefore be
// a map[string]any: missingkey applies to map lookups only.
package prompt

import (
	"strings"
	"text/template"
	"time"
	"unicode"
)

// DateLayout is the format of the prompt date (upstream's
// datetime.now().strftime('%Y-%m-%d')).
const DateLayout = "2006-01-02"

// Clock supplies the current time. It is injected so that rendered prompts
// are reproducible in tests.
type Clock interface {
	Now() time.Time
}

// SystemClock is the wall clock.
type SystemClock struct{}

// Now returns time.Now().
func (SystemClock) Now() time.Time { return time.Now() }

// Date returns the prompt date of c, or of the wall clock when c is nil.
func Date(c Clock) string {
	if c == nil {
		c = SystemClock{}
	}
	return c.Now().Format(DateLayout)
}

// funcs are the template functions available to every prompt template.
var funcs = template.FuncMap{
	// trim is Jinja's trim filter (Python's str.strip()).
	"trim": PyStrip,
}

// New returns an empty template set with the shared engine settings:
// missingkey=error and the shared functions. Parse or ParseFS the prompt
// templates into it.
func New(name string) *template.Template {
	return template.New(name).Option("missingkey=error").Funcs(funcs)
}

// Execute renders the named template of set with vars. Like Jinja's default
// (keep_trailing_newline=False), one trailing newline of the output is
// dropped, so a template file may end with a newline.
func Execute(set *template.Template, name string, vars map[string]any) (string, error) {
	var b strings.Builder
	if err := set.ExecuteTemplate(&b, name, vars); err != nil {
		return "", err
	}
	return strings.TrimSuffix(b.String(), "\n"), nil
}

// PyStrip removes leading and trailing whitespace as Python's str.strip()
// does: Unicode whitespace plus the ASCII separators U+001C to U+001F,
// which Python counts as whitespace and Go's unicode.IsSpace does not.
func PyStrip(s string) string {
	return strings.TrimFunc(s, func(r rune) bool {
		return unicode.IsSpace(r) || (r >= 0x1c && r <= 0x1f)
	})
}
