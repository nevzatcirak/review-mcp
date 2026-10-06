package review

import (
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/nevzatcirak/review-mcp/internal/config"
	"github.com/nevzatcirak/review-mcp/internal/yamlrepair"
)

var allOn = Toggles{EffortEstimate: true, Tests: true, Security: true, Performance: true}

func TestFieldOrderIsUpstreams(t *testing.T) {
	var keys []string
	for _, f := range fields {
		keys = append(keys, f.key)
	}
	want := []string{KeyEffort, KeyRelevantTests, KeyKeyIssues, KeySecurityConcerns, KeyPerformanceConcerns}
	if !slices.Equal(keys, want) {
		t.Errorf("fields = %v, want %v", keys, want)
	}
	if got := keyIssueKeys(); !slices.Equal(got, []string{KeyRelevantFile, KeyIssueHeader, KeyIssueContent, KeyStartLine, KeyEndLine}) {
		t.Errorf("key issue fields = %v", got)
	}
}

func TestTogglesFrom(t *testing.T) {
	got := TogglesFrom(config.Review{RequireEffortEstimate: true, RequireTests: false, RequireSecurity: true})
	if got != (Toggles{EffortEstimate: true, Security: true}) {
		t.Errorf("TogglesFrom = %+v", got)
	}
	if got := TogglesFrom(config.Review{RequirePerformance: true}); got != (Toggles{Performance: true}) {
		t.Errorf("TogglesFrom(performance) = %+v", got)
	}
	if got := TogglesFrom(config.Defaults().Review); got != allOn {
		t.Errorf("TogglesFrom(defaults) = %+v, want every field on", got)
	}
}

func TestSchemaAndExampleFollowToggles(t *testing.T) {
	for _, tc := range []struct {
		t       Toggles
		present []string
		absent  []string
	}{
		{allOn, []string{KeyEffort, KeyRelevantTests, KeyKeyIssues, KeySecurityConcerns, KeyPerformanceConcerns}, nil},
		{Toggles{}, []string{KeyKeyIssues}, []string{KeyEffort, KeyRelevantTests, KeySecurityConcerns, KeyPerformanceConcerns}},
		{Toggles{Security: true}, []string{KeyKeyIssues, KeySecurityConcerns}, []string{KeyEffort, KeyRelevantTests, KeyPerformanceConcerns}},
		{Toggles{Performance: true}, []string{KeyKeyIssues, KeyPerformanceConcerns}, []string{KeyEffort, KeyRelevantTests, KeySecurityConcerns}},
	} {
		s, e := SchemaText(tc.t, 4), ExampleYAML(tc.t)
		for _, k := range tc.present {
			if !strings.Contains(s, "\n    "+k+": ") || !strings.Contains(e, "\n  "+k+":") {
				t.Errorf("%+v: %s missing from schema or example", tc.t, k)
			}
		}
		for _, k := range tc.absent {
			if strings.Contains(s, k) || strings.Contains(e, k) {
				t.Errorf("%+v: disabled %s present", tc.t, k)
			}
		}
		if !strings.Contains(s, "(0-4 issues)") {
			t.Errorf("max findings not in the key issues description")
		}
		if strings.Contains(s, "[1-5]") || strings.Contains(e, "[1-5]") {
			t.Errorf("upstream's bracketed effort key leaked")
		}
	}
}

// TestExampleIsValidYAML: the example the prompt shows must itself parse
// to a review mapping that converts without warnings for its fields.
func TestExampleIsValidYAML(t *testing.T) {
	data, trace := yamlrepair.Load(ExampleYAML(allOn), RepairKeys(allOn))
	if trace.Tactic != yamlrepair.TacticDirect {
		t.Fatalf("example needed repair: %s", trace.Tactic)
	}
	r, c, err := Convert(data, allOn, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Warnings) != 0 {
		t.Errorf("warnings: %v", c.Warnings)
	}
	if r.EstimatedEffortToReview == nil || *r.EstimatedEffortToReview != 3 || r.RelevantTests == nil || *r.RelevantTests ||
		r.SecurityConcerns == nil || *r.SecurityConcerns != SecurityNo ||
		r.PerformanceConcerns == nil || *r.PerformanceConcerns != PerformanceNo || len(r.KeyIssuesToReview) != 1 {
		t.Errorf("example converted to %+v", r)
	}
}

func TestRepairKeys(t *testing.T) {
	k := RepairKeys(allOn)
	want := []string{KeyEffort, KeyRelevantTests, KeyRelevantFile, KeyIssueHeader, KeyIssueContent, KeyStartLine, KeyEndLine,
		KeySecurityConcerns, KeyPerformanceConcerns}
	if !slices.Equal(k.Names, want) {
		t.Errorf("Names = %v, want %v", k.Names, want)
	}
	if slices.Contains(k.Names, KeyKeyIssues) || slices.Contains(k.Names, RootKey) {
		t.Errorf("Names must hold scalar leaf keys only: %v", k.Names)
	}
	if k.First != RootKey || k.Last != KeyPerformanceConcerns {
		t.Errorf("First/Last = %q/%q", k.First, k.Last)
	}
	if k := RepairKeys(Toggles{Security: true}); k.Last != KeySecurityConcerns || slices.Contains(k.Names, KeyPerformanceConcerns) {
		t.Errorf("without performance: %+v", k)
	}
	if k := RepairKeys(Toggles{EffortEstimate: true, Tests: true}); k.Last != KeyKeyIssues || slices.Contains(k.Names, KeySecurityConcerns) {
		t.Errorf("without security: %+v", k)
	}
}

// answerWithColons is a review answer with key issues whose plain scalars
// contain ": " — invalid YAML that tactic 1 (block scalars for known keys)
// repairs.
const answerWithColons = `review:
  estimated_effort_to_review: 2
  relevant_tests: No
  key_issues_to_review:
    - relevant_file: src/app.py
      issue_header: Possible Bug
      issue_content: The loop uses: an off-by-one bound: it skips the last item
      start_line: 12
      end_line: 14
  security_concerns: No`

// TestTacticOneRepairsKeyIssues proves the lead decision on the repair key
// list: with the descriptor-derived scalar leaf keys, tactic 1 repairs a
// review that has key issues; with upstream's list key added, it cannot.
func TestTacticOneRepairsKeyIssues(t *testing.T) {
	keys := RepairKeys(allOn)
	data, trace := yamlrepair.Load(strings.TrimSpace(answerWithColons), keys)
	if trace.Tactic != "block_scalar_keys" {
		t.Fatalf("tactic = %q, want block_scalar_keys", trace.Tactic)
	}
	r, _, err := Convert(data, allOn, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.KeyIssuesToReview) != 1 {
		t.Fatalf("key issues = %+v", r.KeyIssuesToReview)
	}
	ki := r.KeyIssuesToReview[0]
	if ki.RelevantFile != "src/app.py" || ki.StartLine != 12 || ki.EndLine != 14 ||
		ki.IssueContent != "The loop uses: an off-by-one bound: it skips the last item" {
		t.Errorf("key issue = %+v", ki)
	}

	withList := keys
	withList.Names = append([]string{KeyKeyIssues}, keys.Names...)
	if _, trace := yamlrepair.Load(strings.TrimSpace(answerWithColons), withList); trace.Tactic == "block_scalar_keys" {
		t.Errorf("with the list key in Names tactic 1 still won; the deviation would be unnecessary")
	}
}

func TestConvertGate(t *testing.T) {
	for _, data := range []map[string]any{
		{},
		{"review": nil},
		{"review": "text"},
		{"review": map[string]any{}},
		{"other": map[string]any{"a": 1}},
	} {
		if _, _, err := Convert(data, allOn, 3); !errors.Is(err, ErrFallbackEligible) {
			t.Errorf("Convert(%v) err = %v, want ErrFallbackEligible", data, err)
		}
	}
}

func review(fields map[string]any) map[string]any { return map[string]any{"review": fields} }

func TestConvertEffort(t *testing.T) {
	for _, tc := range []struct {
		v    any
		want int // 0: absent
	}{
		{3, 3}, {"3", 3}, {"4\n", 4}, {"2, because it is small", 2}, {5.0, 5},
		{0, 0}, {6, 0}, {"three", 0}, {2.5, 0}, {nil, 0}, {[]any{1}, 0}, {true, 0},
	} {
		r, c, err := Convert(review(map[string]any{KeyEffort: tc.v}), Toggles{EffortEstimate: true}, 3)
		if err != nil {
			t.Fatal(err)
		}
		got := 0
		if r.EstimatedEffortToReview != nil {
			got = *r.EstimatedEffortToReview
		}
		if got != tc.want {
			t.Errorf("effort %#v -> %d, want %d", tc.v, got, tc.want)
		}
		if (tc.want == 0) != slices.ContainsFunc(c.Warnings, func(w string) bool { return strings.HasPrefix(w, KeyEffort+":") }) {
			t.Errorf("effort %#v: warnings %v", tc.v, c.Warnings)
		}
	}
}

func TestConvertTests(t *testing.T) {
	yes, no := true, false
	for _, tc := range []struct {
		v    any
		want *bool
	}{
		{"Yes", &yes}, {"yes\n", &yes}, {"Yes, unit tests were added", &yes}, {true, &yes},
		{"No", &no}, {"no\n", &no}, {false, &no}, {"none", &no}, {"", &no},
		{"maybe", nil}, {3, nil}, {map[string]any{"a": 1}, nil},
	} {
		r, _, err := Convert(review(map[string]any{KeyRelevantTests: tc.v}), Toggles{Tests: true}, 3)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(r.RelevantTests, tc.want) {
			t.Errorf("tests %#v -> %v, want %v", tc.v, r.RelevantTests, tc.want)
		}
	}
}

func TestConvertSecurity(t *testing.T) {
	for _, tc := range []struct {
		v       any
		want    string // "" means absent
		concern bool
	}{
		{"No", SecurityNo, false}, {"no\n", SecurityNo, false}, {false, SecurityNo, false}, {"None", SecurityNo, false},
		{nil, SecurityNo, false}, {"FALSE", SecurityNo, false},
		{"SQL injection: the query is built from input.\n", "SQL injection: the query is built from input.", true},
		{"  ", "", false}, {[]any{"x"}, "", false},
	} {
		r, _, err := Convert(review(map[string]any{KeySecurityConcerns: tc.v}), Toggles{Security: true}, 3)
		if err != nil {
			t.Fatal(err)
		}
		got := ""
		if r.SecurityConcerns != nil {
			got = *r.SecurityConcerns
		}
		if got != tc.want || r.HasSecurityConcerns() != tc.concern {
			t.Errorf("security %#v -> %q (concern %v), want %q (%v)", tc.v, got, r.HasSecurityConcerns(), tc.want, tc.concern)
		}
	}
}

// TestPerformanceDescriptionKeepsEnglishNo pins the lead decision on the
// performance description: the spec wording, then security's no-translation
// sentence verbatim, so a non-English review still answers the literal "No"
// that the No-detector reads.
func TestPerformanceDescriptionKeepsEnglishNo(t *testing.T) {
	const spec = "Answer 'No' if there are none, otherwise describe each one briefly with its file."
	const englishNo = "Answer with the exact English literal 'No', and do not translate it into another language, " +
		"even if extra instructions ask you to write your response in another language."
	if !strings.HasSuffix(descPerformance, spec+" "+englishNo) {
		t.Errorf("performance description does not end with the spec sentence and the no-translation sentence:\n%s", descPerformance)
	}
	if !strings.Contains(descSecurity, "if there are no possible issues. "+englishNo+" If there are security concerns") {
		t.Errorf("security description lost its no-translation sentence:\n%s", descSecurity)
	}
	s := SchemaText(Toggles{Performance: true, Security: true}, 3)
	if strings.Count(s, englishNo) != 2 {
		t.Errorf("schema has %d no-translation sentences, want 2 (security and performance)", strings.Count(s, englishNo))
	}
}

// TestConvertPerformance: performance_concerns has the No-or-text semantics
// of security_concerns (X-12), normalised through yamlrepair.IsNo.
func TestConvertPerformance(t *testing.T) {
	for _, tc := range []struct {
		v       any
		want    string // "" means absent
		concern bool
	}{
		{"No", PerformanceNo, false}, {"no\n", PerformanceNo, false}, {false, PerformanceNo, false}, {"None", PerformanceNo, false},
		{nil, PerformanceNo, false}, {"FALSE", PerformanceNo, false}, {"", PerformanceNo, false},
		{"internal/store/list.go: one query per item (N+1).\n", "internal/store/list.go: one query per item (N+1).", true},
		{"  ", "", false}, {[]any{"x"}, "", false}, {map[string]any{"a": "b"}, "", false},
	} {
		r, c, err := Convert(review(map[string]any{KeyPerformanceConcerns: tc.v}), Toggles{Performance: true}, 3)
		if err != nil {
			t.Fatal(err)
		}
		got := ""
		if r.PerformanceConcerns != nil {
			got = *r.PerformanceConcerns
		}
		if got != tc.want || r.HasPerformanceConcerns() != tc.concern {
			t.Errorf("performance %#v -> %q (concern %v), want %q (%v)", tc.v, got, r.HasPerformanceConcerns(), tc.want, tc.concern)
		}
		if r.SecurityConcerns != nil {
			t.Errorf("performance %#v set the security field", tc.v)
		}
		for _, w := range c.Warnings {
			if !strings.HasPrefix(w, KeyPerformanceConcerns+": ") && !strings.HasPrefix(w, KeyKeyIssues+": ") {
				t.Errorf("unexpected warning %q", w)
			}
		}
	}
	// Disabled: the value is ignored.
	r, _, err := Convert(review(map[string]any{KeyPerformanceConcerns: "slow"}), Toggles{Security: true}, 3)
	if err != nil {
		t.Fatal(err)
	}
	if r.PerformanceConcerns != nil {
		t.Errorf("disabled performance converted: %q", *r.PerformanceConcerns)
	}
}

func issue(file, content string, start, end any) map[string]any {
	return map[string]any{KeyRelevantFile: file, KeyIssueHeader: "Possible Bug", KeyIssueContent: content,
		KeyStartLine: start, KeyEndLine: end}
}

func TestConvertKeyIssues(t *testing.T) {
	items := []any{
		issue("a.go\n", "first\n", 10, 12),
		issue("", "no file", 1, 1),
		"not a mapping",
		issue("b.go", "second", "7\n", "x"),
		issue("c.go", "  ", 1, 1),
		issue("d.go", "third", -1, 2.0),
		issue("e.go", "fourth", 1, 1),
	}
	r, c, err := Convert(review(map[string]any{KeyKeyIssues: items}), Toggles{}, 2)
	if err != nil {
		t.Fatal(err)
	}
	want := []KeyIssue{
		{RelevantFile: "a.go", IssueHeader: "Possible Bug", IssueContent: "first", StartLine: 10, EndLine: 12},
		{RelevantFile: "b.go", IssueHeader: "Possible Bug", IssueContent: "second", StartLine: 7},
	}
	if !reflect.DeepEqual(r.KeyIssuesToReview, want) {
		t.Errorf("key issues = %+v", r.KeyIssuesToReview)
	}
	if len(c.Notes) != 2 || c.Notes[0] != "3 findings without a file or a description were dropped." ||
		c.Notes[1] != "2 findings beyond the limit of 2 (max_findings) were dropped." {
		t.Errorf("notes = %q", c.Notes)
	}

	// One of each, singular.
	r, c, _ = Convert(review(map[string]any{KeyKeyIssues: []any{issue("a", "x", 1, 1), issue("b", "y", 1, 1), 5}}), Toggles{}, 1)
	if len(r.KeyIssuesToReview) != 1 || !slices.Equal(c.Notes, []string{
		"1 finding without a file or a description was dropped.",
		"1 finding beyond the limit of 1 (max_findings) was dropped.",
	}) {
		t.Errorf("singular notes = %q", c.Notes)
	}

	// Empty, null and "No" key issues are an empty list; other scalars warn.
	for _, v := range []any{[]any{}, nil, "No"} {
		r, c, _ := Convert(review(map[string]any{KeyKeyIssues: v}), Toggles{}, 3)
		if r.KeyIssuesToReview == nil || len(r.KeyIssuesToReview) != 0 || len(c.Warnings) != 0 {
			t.Errorf("%#v: %+v %v", v, r.KeyIssuesToReview, c.Warnings)
		}
	}
	if _, c, _ := Convert(review(map[string]any{KeyKeyIssues: "some text"}), Toggles{}, 3); len(c.Warnings) != 1 {
		t.Errorf("scalar key issues: warnings %v", c.Warnings)
	}
}

func TestConvertWarnsMissingAndIgnoresDisabled(t *testing.T) {
	data := review(map[string]any{KeyEffort: 9, KeyRelevantTests: "Yes", "unknown": 1})
	r, c, err := Convert(data, Toggles{Tests: true, Security: true}, 3)
	if err != nil {
		t.Fatal(err)
	}
	if r.EstimatedEffortToReview != nil {
		t.Errorf("disabled effort converted")
	}
	sort.Strings(c.Warnings)
	if !slices.Equal(c.Warnings, []string{KeyKeyIssues + ": missing", KeySecurityConcerns + ": missing"}) {
		t.Errorf("warnings = %v", c.Warnings)
	}
	for _, w := range c.Warnings {
		if strings.Contains(w, "Yes") || strings.Contains(w, "9") {
			t.Errorf("warning contains a value: %q", w)
		}
	}
	if r.KeyIssuesToReview == nil {
		t.Errorf("key issues must be an empty list, not nil")
	}
}

// TestReviewSchemaMatchesStruct: the MCP output schema and the Go structs
// come from the same descriptors and must name the same keys.
func TestReviewSchemaMatchesStruct(t *testing.T) {
	s := ReviewSchema()
	props := s["properties"].(map[string]any)
	if got, want := sortedKeys(props), jsonKeys(t, reflect.TypeFor[Review]()); !slices.Equal(got, want) {
		t.Errorf("schema properties %v, struct fields %v", got, want)
	}
	if !reflect.DeepEqual(s["required"], []any{KeyKeyIssues}) {
		t.Errorf("required = %v", s["required"])
	}
	items := props[KeyKeyIssues].(map[string]any)["items"].(map[string]any)
	if got, want := sortedKeys(items["properties"].(map[string]any)), jsonKeys(t, reflect.TypeFor[KeyIssue]()); !slices.Equal(got, want) {
		t.Errorf("item properties %v, struct fields %v", got, want)
	}
	if _, err := json.Marshal(s); err != nil {
		t.Fatal(err)
	}
}

func sortedKeys(m map[string]any) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func jsonKeys(t *testing.T, typ reflect.Type) []string {
	t.Helper()
	var out []string
	for i := range typ.NumField() {
		name, _, _ := strings.Cut(typ.Field(i).Tag.Get("json"), ",")
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}
