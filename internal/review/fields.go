// Package review implements the pr_review tool's core (spec P4 §4): the
// field-descriptor table (X-4, DQ-6), the adapted review prompts, and the
// review pipeline.
//
// One descriptor table (fields, keyIssueFields) drives four things: the
// schema text and the example YAML in the system prompt, the validation and
// conversion of the parsed answer (Convert), the MCP output schema
// (ReviewSchema) and the key list of the YAML repair chain (RepairKeys).
// The prompt text the table produces is adapted from PR-Agent
// (pr_agent/settings/pr_reviewer_prompts.toml @ 8e5a929, MIT; see NOTICE).
package review

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"

	"github.com/nevzatcirak/review-mcp/internal/config"
	"github.com/nevzatcirak/review-mcp/internal/llmrun"
	"github.com/nevzatcirak/review-mcp/internal/prompt"
	"github.com/nevzatcirak/review-mcp/internal/yamlrepair"
)

// RootKey is the root mapping of a review answer (upstream first_key).
const RootKey = "review"

// Field keys, in upstream order.
const (
	KeyEffort           = "estimated_effort_to_review"
	KeyRelevantTests    = "relevant_tests"
	KeyKeyIssues        = "key_issues_to_review"
	KeySecurityConcerns = "security_concerns"
)

// Key-issue element keys, in upstream order.
const (
	KeyRelevantFile = "relevant_file"
	KeyIssueHeader  = "issue_header"
	KeyIssueContent = "issue_content"
	KeyStartLine    = "start_line"
	KeyEndLine      = "end_line"
)

// SecurityNo is the value of Review.SecurityConcerns when the model reported
// no security concerns (the No-detector matched).
const SecurityNo = "No"

// Effort bounds (X-4).
const (
	MinEffort = 1
	MaxEffort = 5
)

// errNoReview is the validation gate's failure (§4.1).
var errNoReview = fmt.Errorf("review: the answer has no non-empty %q mapping: %w", RootKey, ErrFallbackEligible)

// Toggles selects the optional review fields (X-4).
type Toggles struct {
	EffortEstimate bool
	Tests          bool
	Security       bool
}

// TogglesFrom reads the toggles from the review configuration.
func TogglesFrom(r config.Review) Toggles {
	return Toggles{EffortEstimate: r.RequireEffortEstimate, Tests: r.RequireTests, Security: r.RequireSecurity}
}

// Review is the validated review. Optional fields are nil when disabled,
// missing or unusable.
type Review struct {
	EstimatedEffortToReview *int       `json:"estimated_effort_to_review,omitempty"`
	RelevantTests           *bool      `json:"relevant_tests,omitempty"`
	KeyIssuesToReview       []KeyIssue `json:"key_issues_to_review"`
	SecurityConcerns        *string    `json:"security_concerns,omitempty"`
}

// HasSecurityConcerns reports whether the security field is enabled, present
// and not "No".
func (r *Review) HasSecurityConcerns() bool {
	return r.SecurityConcerns != nil && *r.SecurityConcerns != SecurityNo
}

// KeyIssue is one finding. The first five fields come from the model; the
// others are filled by the pipeline (Run).
type KeyIssue struct {
	RelevantFile string `json:"relevant_file"`
	IssueHeader  string `json:"issue_header"`
	IssueContent string `json:"issue_content"`
	// StartLine and EndLine are new-side line numbers; 0 when the model gave
	// none that could be read.
	StartLine int `json:"start_line"`
	EndLine   int `json:"end_line"`

	// Snippet holds the verified lines StartLine to EndLine (at most
	// MaxSnippetLines), without line endings, joined by "\n". Empty when
	// the lines could not be verified (SnippetNote says why).
	Snippet string `json:"snippet,omitempty"`
	// SnippetNote explains a missing or shortened snippet.
	SnippetNote string `json:"snippet_note,omitempty"`
	// Link is the provider URL of the file at StartLine, when available.
	Link string `json:"link,omitempty"`
	// InlineURL is the URL of the finding's inline comment, when one was
	// posted and the server reported its URL.
	InlineURL string `json:"inline_url,omitempty"`
}

// field describes one review field (X-4). Its prompt text reproduces
// upstream's Pydantic-style schema and example.
type field struct {
	key string
	// enabled is nil for a field that is always on.
	enabled func(Toggles) bool
	// pyType is the type in the prompt's Pydantic-style schema.
	pyType string
	// description is the prompt description; maxFindings fills the key
	// issues' count.
	description func(maxFindings int) string
	// positional writes Field("…") instead of Field(description="…"), as
	// upstream's key_issues_to_review line does; kept so the prompt is
	// upstream's byte for byte.
	positional bool
	// example is the value part of the example YAML line ("key:" + example).
	example string
	// leafKeys are the keys of this field that hold scalar values, for the
	// repair chain's tactic 1 (see RepairKeys).
	leafKeys []string
	// schema is the JSON schema of the converted value.
	schema func() map[string]any
	// convert stores the converted value in r. It returns a fixed warning
	// (never containing the value) when the value is unusable.
	convert func(v any, r *Review, c *Conversion, maxFindings int) string
}

// keyIssueField describes one element key of key_issues_to_review.
type keyIssueField struct {
	key, pyType, description, example string
	schema                            map[string]any
}

// Upstream's prompt descriptions, verbatim except the effort key, which X-4
// renames from upstream's estimated_effort_to_review_[1-5] (the range stays
// in the description).
const (
	descEffort = "Estimate, on a scale of 1-5 (inclusive), the time and effort required to review this PR by an " +
		"experienced and knowledgeable developer. 1 means short and easy review, 5 means long and hard review. " +
		"Take into account the size, complexity, quality, and the needed changes of the PR code diff."
	descTests     = "Does this PR have relevant tests added or updated? Answer exactly Yes or No."
	descKeyIssues = "A concise list (0-%d issues) of bugs, security vulnerabilities, or significant performance " +
		"concerns introduced in this PR. Only include issues you are confident about. If confidence is limited but " +
		"the potential impact is high (e.g., data loss, security), you may include it only if you explicitly note " +
		"what remains uncertain. Each issue must identify a concrete problem with a realistic trigger scenario. " +
		"An empty list is acceptable if no clear issues are found."
	descSecurity = "Does this PR code introduce vulnerabilities such as exposure of sensitive information " +
		"(e.g., API keys, secrets, passwords), or security concerns like SQL injection, XSS, CSRF, and others? " +
		"Answer 'No' (without explaining why) if there are no possible issues. Answer with the exact English " +
		"literal 'No', and do not translate it into another language, even if extra instructions ask you to " +
		"write your response in another language. If there are security concerns or issues, start your answer " +
		"with a short header, such as: 'Sensitive information exposure: ...', 'SQL injection: ...', etc. " +
		"Explain your answer. Be specific and give examples if possible"
)

// keyIssueFields is upstream's KeyIssuesComponentLink, in upstream order.
var keyIssueFields = []keyIssueField{
	{KeyRelevantFile, "str", "The full file path of the relevant file", " |\n        directory/xxx.py",
		map[string]any{"type": "string", "description": "path of the file, as the model named it"}},
	{KeyIssueHeader, "str", "One or two word title for the issue. For example: 'Possible Bug', etc.", " |\n        Possible Bug",
		map[string]any{"type": "string", "description": "one or two word title of the issue"}},
	{KeyIssueContent, "str", "A short and concise description of the issue, why it matters, and the specific " +
		"scenario or input that triggers it. Do not mention line numbers in this field.", " |\n        ...",
		map[string]any{"type": "string", "description": "description of the issue"}},
	{KeyStartLine, "int", "The start line that corresponds to this issue in the relevant file", " 12",
		map[string]any{"type": "integer", "minimum": 0, "description": "first new-side line of the issue; 0 when unknown"}},
	{KeyEndLine, "int", "The end line that corresponds to this issue in the relevant file", " 14",
		map[string]any{"type": "integer", "minimum": 0, "description": "last new-side line of the issue; 0 when unknown"}},
}

// keyIssueExtraSchema are the KeyIssue properties the pipeline adds.
var keyIssueExtraSchema = map[string]map[string]any{
	"snippet":      {"type": "string", "description": "the verified code lines start_line to end_line (at most 30), joined by newlines"},
	"snippet_note": {"type": "string", "description": "why the snippet is missing or shortened"},
	"link":         {"type": "string", "description": "provider URL of the file at start_line"},
	"inline_url":   {"type": "string", "description": "URL of the inline comment posted for this finding"},
}

// fields is the review descriptor table, in upstream order (X-4).
var fields = []field{
	{
		key:         KeyEffort,
		enabled:     func(t Toggles) bool { return t.EffortEstimate },
		pyType:      "int",
		description: func(int) string { return descEffort },
		example:     " 3",
		leafKeys:    []string{KeyEffort},
		schema: func() map[string]any {
			return map[string]any{"type": "integer", "minimum": MinEffort, "maximum": MaxEffort,
				"description": "estimated review effort, 1 (short and easy) to 5 (long and hard)"}
		},
		convert: convertEffort,
	},
	{
		key:         KeyRelevantTests,
		enabled:     func(t Toggles) bool { return t.Tests },
		pyType:      `Literal["Yes", "No"]`,
		description: func(int) string { return descTests },
		example:     " |\n    No",
		leafKeys:    []string{KeyRelevantTests},
		schema: func() map[string]any {
			return map[string]any{"type": "boolean", "description": "whether the PR adds or updates relevant tests"}
		},
		convert: convertTests,
	},
	{
		key:         KeyKeyIssues,
		pyType:      "List[KeyIssuesComponentLink]",
		description: func(n int) string { return fmt.Sprintf(descKeyIssues, n) },
		positional:  true,
		example:     keyIssuesExample(),
		leafKeys:    keyIssueKeys(),
		schema:      keyIssuesSchema,
		convert:     convertKeyIssues,
	},
	{
		key:         KeySecurityConcerns,
		enabled:     func(t Toggles) bool { return t.Security },
		pyType:      "str",
		description: func(int) string { return descSecurity },
		example:     " |\n    No",
		leafKeys:    []string{KeySecurityConcerns},
		schema: func() map[string]any {
			return map[string]any{"type": "string",
				"description": "\"No\" when no security concerns were found; otherwise the concerns, starting with a short header"}
		},
		convert: convertSecurity,
	},
}

func (f *field) on(t Toggles) bool { return f.enabled == nil || f.enabled(t) }

func keyIssueKeys() []string {
	keys := make([]string, len(keyIssueFields))
	for i, e := range keyIssueFields {
		keys[i] = e.key
	}
	return keys
}

// keyIssuesExample is upstream's example list: one complete element, then
// "- ...".
func keyIssuesExample() string {
	var b strings.Builder
	for i, e := range keyIssueFields {
		if i == 0 {
			b.WriteString("\n    - ")
		} else {
			b.WriteString("\n      ")
		}
		b.WriteString(e.key + ":" + e.example)
	}
	b.WriteString("\n    - ...")
	return b.String()
}

// enabledFields returns the fields on under t, in table order.
func enabledFields(t Toggles) []*field {
	var out []*field
	for i := range fields {
		if fields[i].on(t) {
			out = append(out, &fields[i])
		}
	}
	return out
}

// SchemaText is the Pydantic-style schema of the system prompt for the
// enabled fields: upstream's KeyIssuesComponentLink, Review and PRReview
// classes, without the removed fields (X-4).
func SchemaText(t Toggles, maxFindings int) string {
	var b strings.Builder
	b.WriteString("class KeyIssuesComponentLink(BaseModel):")
	for _, e := range keyIssueFields {
		fmt.Fprintf(&b, "\n    %s: %s = Field(description=\"%s\")", e.key, e.pyType, e.description)
	}
	b.WriteString("\n\nclass Review(BaseModel):")
	for _, f := range enabledFields(t) {
		kw := "description="
		if f.positional {
			kw = ""
		}
		fmt.Fprintf(&b, "\n    %s: %s = Field(%s\"%s\")", f.key, f.pyType, kw, f.description(maxFindings))
	}
	b.WriteString("\n\nclass PRReview(BaseModel):\n    review: Review")
	return b.String()
}

// ExampleYAML is the example answer of the system prompt for the enabled
// fields.
func ExampleYAML(t Toggles) string {
	var b strings.Builder
	b.WriteString(RootKey + ":")
	for _, f := range enabledFields(t) {
		b.WriteString("\n  " + f.key + ":" + f.example)
	}
	return b.String()
}

// RepairKeys is the key configuration of the YAML repair chain for the
// enabled fields.
//
// Deviation from upstream (lead decision, WP-PR-4b DESIGN-QUESTION 4):
// Names holds the scalar leaf keys only: the enabled scalar fields and the
// key-issue element keys. Upstream's list also holds the list key
// key_issues_to_review, which tactic 1 then turns into an empty block
// scalar, so that tactic can never repair an answer that has key issues.
// The root key is not listed either: forcing it would turn the whole
// mapping into one string. First is the root key and Last the last enabled
// field.
func RepairKeys(t Toggles) yamlrepair.Keys {
	k := yamlrepair.Keys{First: RootKey}
	for _, f := range enabledFields(t) {
		k.Names = append(k.Names, f.leafKeys...)
		k.Last = f.key
	}
	return k
}

// ReviewSchema is the JSON schema (draft 2020-12) of Review for the MCP
// output schema (DQ-6). Every descriptor field is a property; only the
// always-on fields are required, because the toggles may switch the others
// off.
func ReviewSchema() map[string]any {
	props := map[string]any{}
	var required []any
	for i := range fields {
		f := &fields[i]
		props[f.key] = f.schema()
		if f.enabled == nil {
			required = append(required, f.key)
		}
	}
	return map[string]any{
		"type":                 "object",
		"properties":           props,
		"required":             required,
		"additionalProperties": false,
	}
}

func keyIssuesSchema() map[string]any {
	props := map[string]any{}
	required := []any{}
	for _, e := range keyIssueFields {
		s := map[string]any{}
		for k, v := range e.schema {
			s[k] = v
		}
		props[e.key] = s
		required = append(required, e.key)
	}
	for k, v := range keyIssueExtraSchema {
		props[k] = v
	}
	return map[string]any{
		"type":        "array",
		"description": "the findings, at most max_findings",
		"items": map[string]any{
			"type":                 "object",
			"properties":           props,
			"required":             required,
			"additionalProperties": false,
		},
	}
}

// Conversion reports what Convert did besides the result.
type Conversion struct {
	// Warnings are fixed phrases naming a field and a problem; they never
	// contain values. The caller logs them at debug level.
	Warnings []string
	// Notes are user-facing sentences (dropped findings).
	Notes []string
}

func (c *Conversion) warn(s string) { c.Warnings = append(c.Warnings, s) }

// Convert validates a parsed answer and converts the enabled fields (§4.1).
// The only gate is a non-empty "review" mapping: without one, Convert
// returns an error matching ErrFallbackEligible. Everything else is
// warn-only: a missing or unusable optional field is left nil with a
// warning, unusable findings and findings beyond maxFindings are dropped
// with a note. Fields the toggles disable are ignored.
func Convert(data map[string]any, t Toggles, maxFindings int) (*Review, *Conversion, error) {
	root, ok := data[RootKey].(map[string]any)
	if !ok || len(root) == 0 {
		return nil, nil, errNoReview
	}
	r := &Review{KeyIssuesToReview: []KeyIssue{}}
	c := &Conversion{}
	for _, f := range enabledFields(t) {
		v, present := root[f.key]
		if !present {
			c.warn(f.key + ": missing")
			continue
		}
		if w := f.convert(v, r, c, maxFindings); w != "" {
			c.warn(f.key + ": " + w)
		}
	}
	return r, c, nil
}

var leadingInt = regexp.MustCompile(`^\s*([+-]?\d+)`)

// toInt reads an integer: an int, an integral float, or a string that
// starts with an integer ("3", "3\n", "3, because ...").
func toInt(v any) (int, bool) {
	switch x := v.(type) {
	case int:
		return x, true
	case int64:
		if x < math.MinInt || x > math.MaxInt {
			return 0, false
		}
		return int(x), true
	case uint64:
		if x > math.MaxInt {
			return 0, false
		}
		return int(x), true
	case float64:
		if x != math.Trunc(x) || x < math.MinInt32 || x > math.MaxInt32 {
			return 0, false
		}
		return int(x), true
	case string:
		m := leadingInt.FindStringSubmatch(x)
		if m == nil {
			return 0, false
		}
		n, err := strconv.Atoi(m[1])
		if err != nil {
			return 0, false
		}
		return n, true
	}
	return 0, false
}

// toText reads a scalar as trimmed text; mappings and lists are not text.
func toText(v any) (string, bool) {
	switch x := v.(type) {
	case nil:
		return "", true
	case string:
		return prompt.PyStrip(x), true
	case bool, int, int64, uint64, float64:
		return fmt.Sprint(x), true
	}
	return "", false
}

func convertEffort(v any, r *Review, _ *Conversion, _ int) string {
	n, ok := toInt(v)
	if !ok {
		return "not an integer"
	}
	// DESIGN-QUESTION: an effort outside 1-5 — clamp it or drop it? — chose
	// to drop it (field absent, warning logged) because clamping would
	// invent a value the model did not give, and the field is warn-only.
	if n < MinEffort || n > MaxEffort {
		return "out of range"
	}
	r.EstimatedEffortToReview = &n
	return ""
}

// convertTests normalizes relevant_tests to a bool.
//
// DESIGN-QUESTION: which answers count as Yes? — chose a boolean, or a
// string the No-detector accepts (false), or one starting with "yes" or
// equal to "true" (true); anything else leaves the field absent with a
// warning, because upstream's renderer treats every non-No value as "PR
// contains tests", which would turn an unrelated answer into a claim.
func convertTests(v any, r *Review, _ *Conversion, _ int) string {
	var b bool
	switch x := v.(type) {
	case bool:
		b = x
	case string:
		s := strings.ToLower(prompt.PyStrip(x))
		switch {
		case yamlrepair.IsNo(x):
			b = false
		case s == "yes" || s == "true" || strings.HasPrefix(s, "yes"):
			b = true
		default:
			return "neither Yes nor No"
		}
	default:
		return "neither Yes nor No"
	}
	r.RelevantTests = &b
	return ""
}

func convertSecurity(v any, r *Review, _ *Conversion, _ int) string {
	if yamlrepair.IsNo(v) {
		s := SecurityNo
		r.SecurityConcerns = &s
		return ""
	}
	s, ok := toText(v)
	if !ok {
		return "not text"
	}
	if s == "" {
		// IsNo already accepts "" and nil; a whitespace-only string is not
		// a usable answer.
		return "empty"
	}
	r.SecurityConcerns = &s
	return ""
}

func convertKeyIssues(v any, r *Review, c *Conversion, maxFindings int) string {
	var warning string
	var items []any
	switch x := v.(type) {
	case nil:
	case []any:
		items = x
	default:
		if !yamlrepair.IsNo(x) {
			warning = "not a list"
		}
	}
	unusable := 0
	for _, it := range items {
		ki, ok := convertKeyIssue(it)
		if !ok {
			unusable++
			continue
		}
		r.KeyIssuesToReview = append(r.KeyIssuesToReview, ki)
	}
	if unusable > 0 {
		c.Notes = append(c.Notes, fmt.Sprintf("%s without a file or a description %s dropped.",
			countPhrase(unusable, "finding", "findings"), wasWere(unusable)))
	}
	if maxFindings > 0 && len(r.KeyIssuesToReview) > maxFindings {
		extra := len(r.KeyIssuesToReview) - maxFindings
		r.KeyIssuesToReview = r.KeyIssuesToReview[:maxFindings]
		c.Notes = append(c.Notes, fmt.Sprintf("%s beyond the limit of %d (max_findings) %s dropped.",
			countPhrase(extra, "finding", "findings"), maxFindings, wasWere(extra)))
	}
	return warning
}

// convertKeyIssue converts one element. It is unusable when it is not a
// mapping or has no file or no content.
func convertKeyIssue(v any) (KeyIssue, bool) {
	m, ok := v.(map[string]any)
	if !ok {
		return KeyIssue{}, false
	}
	var ki KeyIssue
	ki.RelevantFile, _ = toText(m[KeyRelevantFile])
	ki.IssueHeader, _ = toText(m[KeyIssueHeader])
	ki.IssueContent, _ = toText(m[KeyIssueContent])
	if ki.RelevantFile == "" || ki.IssueContent == "" {
		return KeyIssue{}, false
	}
	if n, ok := toInt(m[KeyStartLine]); ok && n > 0 {
		ki.StartLine = n
	}
	if n, ok := toInt(m[KeyEndLine]); ok && n > 0 {
		ki.EndLine = n
	}
	return ki, true
}

func wasWere(n int) string {
	if n == 1 {
		return "was"
	}
	return "were"
}

// countPhrase returns "1 <one>" or "n <many>".
func countPhrase(n int, one, many string) string { return llmrun.CountPhrase(n, one, many) }
