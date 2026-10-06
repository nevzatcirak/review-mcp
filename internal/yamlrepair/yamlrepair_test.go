package yamlrepair

import (
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// reviewKeys are the v1 review keys (X-4) as test input; the real list is
// derived from the review field descriptors (WP-PR-4c).
var reviewKeys = Keys{
	Names: []string{
		"estimated_effort_to_review", "relevant_tests", "key_issues_to_review", "security_concerns",
		"relevant_file", "issue_header", "issue_content", "start_line", "end_line",
	},
	First: "review",
	Last:  "security_concerns",
}

func TestIsNo(t *testing.T) {
	// The first two groups are the inputs of upstream's TestIsValueNo
	// (tests/unittest/test_markdown_ticket_output_core.py) and the "no"
	// answers of test_review_security_concerns_shape.py.
	yes := []any{
		"No", "no", "NONE", " false ", "", nil, 0, []any{}, map[string]any{},
		"none", "false",
		false, "FALSE", "None", "\tNo\n", int64(0), uint64(0), 0.0,
	}
	not := []any{
		"yes", "Yes", "true", "maybe", "123",
		true, "   ", "Possible token leak", 1, 2.5, []any{"No"}, map[string]any{"a": "No"}, "No.", "Nope",
	}
	for _, v := range yes {
		if !IsNo(v) {
			t.Errorf("IsNo(%#v) = false, want true", v)
		}
	}
	for _, v := range not {
		if IsNo(v) {
			t.Errorf("IsNo(%#v) = true, want false", v)
		}
	}
}

// TestIsNoOnParsedSecurityConcerns runs the No-detector on what Load
// actually produces for the shapes a model writes.
func TestIsNoOnParsedSecurityConcerns(t *testing.T) {
	for _, tc := range []struct {
		answer string
		no     bool
	}{
		{"review:\n  security_concerns: No\n", true},
		{"review:\n  security_concerns: 'No'\n", true},
		{"review:\n  security_concerns: |\n    No\n", true},
		{"review:\n  security_concerns: false\n", true},
		{"review:\n  security_concerns: none\n", true},
		{"review:\n  security_concerns:\n", true},
		{"review:\n  security_concerns: |\n    SQL injection: the id reaches the query.\n", false},
	} {
		got, trace := Load(tc.answer, reviewKeys)
		review, _ := got["review"].(map[string]any)
		if review == nil {
			t.Fatalf("%q: no review (trace %s)", tc.answer, trace.Tactic)
		}
		if IsNo(review["security_concerns"]) != tc.no {
			t.Errorf("%q: IsNo(%#v) != %v", tc.answer, review["security_concerns"], tc.no)
		}
	}
}

// TestDuplicateKeysLastWins documents the dialect rule: the last duplicate
// key wins, as in PyYAML. yaml.v3's own decoder rejects duplicates, which
// is why parse converts the node tree itself.
func TestDuplicateKeysLastWins(t *testing.T) {
	var v any
	if err := yaml.Unmarshal([]byte("a: 1\na: 2\n"), &v); err == nil {
		t.Fatal("yaml.v3 now accepts duplicate keys; revisit the converter in parse.go")
	}
	answer := "review:\n" +
		"  estimated_effort_to_review: 2\n" +
		"  key_issues_to_review:\n" +
		"    - relevant_file: a.go\n" +
		"      relevant_file: b.go\n" +
		"  estimated_effort_to_review: 4\n" +
		"review_extra: x\n" +
		"review_extra: y\n"
	got, trace := Load(answer, reviewKeys)
	want := map[string]any{
		"review": map[string]any{
			"estimated_effort_to_review": 4,
			"key_issues_to_review":       []any{map[string]any{"relevant_file": "b.go"}},
		},
		"review_extra": "y",
	}
	if trace.Tactic != TacticDirect || !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v (trace %s), want %#v", got, trace.Tactic, want)
	}
}

func TestParseDialect(t *testing.T) {
	for _, tc := range []struct {
		name string
		text string
		want any
	}{
		{"empty", "", nil},
		{"comment only", "# nothing\n", nil},
		{"null", "~", nil},
		{"scalar", "hello", "hello"},
		{"yaml 1.1 words stay strings", "a: yes\nb: No\nc: on\nd: off", map[string]any{"a": "yes", "b": "No", "c": "on", "d": "off"}},
		{"timestamps stay strings", "d: 2024-01-01\nt: 2024-01-01T10:00:00Z", map[string]any{"d": "2024-01-01", "t": "2024-01-01T10:00:00Z"}},
		{"non-string keys", "1: a\ntrue: b\n~: c\n1.5: d", map[string]any{"1": "a", "true": "b", "null": "c", "1.5": "d"}},
		{"alias", "a: &x [1, 2]\nb: *x", map[string]any{"a": []any{1, 2}, "b": []any{1, 2}}},
		{"merge", "base: &b {x: 1, y: 2}\nm:\n  <<: *b\n  y: 3", map[string]any{"base": map[string]any{"x": 1, "y": 2}, "m": map[string]any{"x": 1, "y": 3}}},
		{"merge list precedence", "a: &a {x: 1}\nb: &b {x: 2, z: 2}\nm:\n  <<: [*a, *b]", map[string]any{"a": map[string]any{"x": 1}, "b": map[string]any{"x": 2, "z": 2}, "m": map[string]any{"x": 1, "z": 2}}},
		{"quoted merge key is a key", "\"<<\": 1", map[string]any{"<<": 1}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parse(tc.text)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %#v, want %#v", got, tc.want)
			}
		})
	}
}

func TestParseFailures(t *testing.T) {
	bomb := "a: &a [x, x, x, x, x, x, x, x, x, x]\n"
	prev := "a"
	for _, n := range []string{"b", "c", "d", "e", "f"} {
		bomb += n + ": &" + n + " [" + strings.Repeat("*"+prev+", ", 9) + "*" + prev + "]\n"
		prev = n
	}
	for _, tc := range []struct {
		name string
		text string
	}{
		{"syntax", "a: b: c"},
		{"two documents", "a: 1\n---\nb: 2"},
		{"empty second document", "a: 1\n---\n"},
		{"unknown tag", "a: !foo x"},
		{"unknown tag on a mapping", "a: !foo {x: 1}"},
		{"sequence key", "? [a, b]\n: 1"},
		{"bad merge", "m:\n  <<: 1"},
		{"invalid explicit int", "a: !!int abc"},
		{"alias expansion", bomb},
		{"tab indentation", "a:\n\tb: 1"},
		{"C1 control character", "a: x\u0085\u0090y"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if v, err := parse(tc.text); err == nil {
				t.Fatalf("parse succeeded: %#v", v)
			}
		})
	}
}

func TestLoadAlwaysReturnsAMap(t *testing.T) {
	for _, text := range []string{"", "   ", "just prose", "- a\n- b", "{}", "\x00\x01", "::: [", "|"} {
		got, trace := Load(text, reviewKeys)
		if got == nil {
			t.Errorf("%q: nil map", text)
		}
		if len(got) != 0 {
			t.Errorf("%q: got %#v, want empty (trace %s)", text, got, trace.Tactic)
		}
	}
}

func TestSanitizeControlChars(t *testing.T) {
	in := "a\x00b\x08c\td\ne\rf\x0bg\x0ch\x1fi\x7fj\u0085k\u009fl"
	want := "abc\td\ne\rfghij\u0085k\u009fl"
	if got := sanitizeControlChars(in); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestPreprocess(t *testing.T) {
	for _, tc := range []struct {
		name, in, want string
	}{
		{"surrounding newlines", "\n\na: 1\n\n", "a: 1"},
		{"yaml fence", "```yaml\na: 1\n```", "\na: 1\n"},
		{"labelled fence keeps other info strings", "```python\na: 1\n```", "```python\na: 1\n"},
		{"fence label must be complete", "```yamlx\na: 1", "```yamlx\na: 1"},
		{"fence at end without newline is kept", "```yaml", "```yaml"},
		{"crlf fence", "```yaml\r\na: 1\r\n```", "\r\na: 1\r\n"},
		{"bare yaml prefix", "yaml\na: 1", "\na: 1"},
		{"bare yaml prefix of a word (upstream quirk)", "yaml_key: 1", "_key: 1"},
		{"yml word is kept", "yml_config: 1", "yml_config: 1"},
		{"sign-off dropped", "a: 1\n```\n\nThanks!", "a: 1"},
		{"key-like tail kept", "a: 1\n```\nb: 2", "a: 1\n```\nb: 2"},
		{"list tail kept", "a: 1\n```\n- x", "a: 1\n```\n- x"},
		{"tail kept when the rest is no mapping", "just text\n```\n\nThanks!", "just text\n```\n\nThanks!"},
		{"fence with trailing spaces", "a: 1\n```  \n\nThanks!", "a: 1"},
		{"control characters removed last", "a: 1\x08\n```", "a: 1\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := preprocess(tc.in); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestStartsWithKey(t *testing.T) {
	for line, want := range map[string]bool{
		"key:":          true,
		"key: value":    true,
		"_k1:\tv":       true,
		"key:\u00a0v":   true,
		"key:value":     false,
		"1key: v":       false,
		"Note: thanks":  true,
		"two words: v":  false,
		"":              false,
		"key":           false,
		"key\u0131: v":  false,
		"k:\x1cseparat": true,
	} {
		if got := startsWithKey(line); got != want {
			t.Errorf("startsWithKey(%q) = %v, want %v", line, got, want)
		}
	}
}

func TestFindSnippet(t *testing.T) {
	for _, tc := range []struct {
		name, in, want string
		ok             bool
	}{
		{"plain", "x\n```\na: 1\n```", "a: 1\n", true},
		{"labelled", "x\n```YmL \na: 1\n```  \n", "a: 1\n", true},
		{"closing fence must end the text", "```\na: 1\n```\nmore", "", false},
		{"closing fence before a quote", "\"```yaml\na: 1\n```\"", "a: 1\n", true},
		{"shortest body", "```\na\n```\n```\nb\n```", "a\n```\n```\nb\n", true},
		{"other label", "```go\na\n```", "", false},
		{"later opening", "```go\nx\n```yaml\na: 1\n```", "a: 1\n", true},
		{"crlf", "```yaml\r\na: 1\r\n```", "a: 1\r\n", true},
		{"no opening", "a: 1\n```", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := findSnippet(tc.in)
			if ok != tc.ok || got != tc.want {
				t.Fatalf("got %q, %v; want %q, %v", got, ok, tc.want, tc.ok)
			}
		})
	}
}

func TestKeyWindow(t *testing.T) {
	keys := Keys{First: "review", Last: "security_concerns"}
	for _, tc := range []struct {
		name, text string
		want       []string
	}{
		{"window", "x\nreview:\n  a: 1\n  security_concerns: No\n\ntail", []string{"review:\n  a: 1\n  security_concerns: No"}},
		{"no blank line after the last key", "review:\n  security_concerns: No\ntail", []string{"review:\n  security_concerns: No\ntail"}},
		{"labelled closing fence", "review:\n  security_concerns: No\n```YAML", []string{"review:\n  security_concerns: No"}},
		{"backticks", "review:\n  security_concerns: No\n``", []string{"review:\n  security_concerns: No"}},
		// Upstream's find() returns -1, and slicing from -1 keeps the last
		// character.
		{"first key missing", "some text without the key", []string{"y"}},
		{"first key missing, last char a backtick", "text`", nil},
		{"first key missing, multibyte last char", "text é", []string{"é"}},
		{"empty", "", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := keyWindow(input{text: tc.text, keys: keys})
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
	if got := keyWindow(input{text: "review:\n  a: 1", keys: Keys{First: "review"}}); got != nil {
		t.Fatalf("without a last key: got %q", got)
	}
}

func TestDiffMarkers(t *testing.T) {
	in := input{text: "- item\n-  removed\n--x\n-+y\n- \n-\n+added\nkeep"}
	want := []string{"- item\n  removed\n x\n y\n \n\n added\nkeep"}
	if got := diffMarkers(in); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
	if got := diffMarkers(input{text: "- a\n- b"}); got != nil {
		t.Fatalf("unmodified text: got %q", got)
	}
}

func TestBlockScalarKeys(t *testing.T) {
	in := input{
		text: "  issue_content: a: b\n  relevant_file: |\n    x\n  other: c: d",
		keys: Keys{Names: []string{"", "issue_content", "relevant_file"}},
	}
	want := []string{"  issue_content: |\n         a: b\n  relevant_file: |\n    x\n  other: c: d"}
	if got := blockScalarKeys(in); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestReencode(t *testing.T) {
	mojibake := "a: \u00c3\u00a9" // "é" in UTF-8, read as Latin-1
	if got := reencode(input{text: mojibake}); !reflect.DeepEqual(got, []string{"a: é"}) {
		t.Fatalf("got %q", got)
	}
	if got := reencode(input{text: "a: \u0131"}); got != nil {
		t.Fatalf("outside Latin-1: got %q", got)
	}
	if got := reencode(input{text: "a: \u00e9"}); got != nil {
		t.Fatalf("invalid UTF-8 after encoding: got %q", got)
	}
}
