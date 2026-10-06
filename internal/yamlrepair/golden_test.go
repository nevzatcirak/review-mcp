package yamlrepair

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"testing"
)

// casesDir holds one directory per fixture (see testdata/README.md):
// input.txt, case.json, golden.json (written by the upstream oracle) and,
// for an intentional deviation, deviation.json (hand-derived).
const casesDir = "testdata/cases"

type caseMeta struct {
	Names  []string `json:"names"`
	First  string   `json:"first"`
	Last   string   `json:"last"`
	Source string   `json:"source"`
	Canary string   `json:"canary"`
}

// oracleGolden is golden.json, written by testdata/oracle/gen_goldens.py.
type oracleGolden struct {
	UpstreamType   string `json:"upstream_type"`
	Upstream       any    `json:"upstream"`
	UpstreamTactic string `json:"upstream_tactic"`
}

// deviation is deviation.json: a hand-derived expectation for a case where
// review-mcp deliberately differs from upstream. Reason names the decision.
type deviation struct {
	Reason string `json:"reason"`
	Want   any    `json:"want"`
	Tactic string `json:"tactic"`
}

type fixture struct {
	name      string
	input     string
	meta      caseMeta
	golden    oracleGolden
	deviation *deviation
}

func (f fixture) keys() Keys {
	return Keys{Names: f.meta.Names, First: f.meta.First, Last: f.meta.Last}
}

// upstreamTactics maps the oracle's tactic numbers to the chain's names.
// Tactics 3 and 10 are not ported (DQ-8): cases they win upstream are
// deviations.
var upstreamTactics = map[string]string{
	"direct": TacticDirect,
	"none":   TacticNone,
	"1":      tacticBlockScalarKeys,
	"2":      tacticExplicitIndent,
	"4":      tacticFencedSnippet,
	"5":      tacticStripBraces,
	"6":      tacticKeyWindow,
	"7":      tacticStripPlus,
	"8":      tacticDiffMarkers,
	"9":      tacticTabsToSpaces,
	"11":     tacticRootPipe,
	"12":     tacticReencode,
}

func readJSON(t *testing.T, path string, v any) bool {
	t.Helper()
	b, err := os.ReadFile(path) //nolint:gosec // test fixture path
	if errors.Is(err, fs.ErrNotExist) {
		return false
	}
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, v); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	return true
}

func loadFixtures(t *testing.T) []fixture {
	t.Helper()
	entries, err := os.ReadDir(casesDir)
	if err != nil {
		t.Fatal(err)
	}
	var out []fixture
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(casesDir, e.Name())
		input, err := os.ReadFile(filepath.Join(dir, "input.txt")) //nolint:gosec // test fixture path
		if err != nil {
			t.Fatal(err)
		}
		f := fixture{name: e.Name(), input: string(input)}
		if !readJSON(t, filepath.Join(dir, "case.json"), &f.meta) {
			t.Fatalf("%s: case.json missing", dir)
		}
		if !readJSON(t, filepath.Join(dir, "golden.json"), &f.golden) {
			t.Fatalf("%s: golden.json missing (run the oracle)", dir)
		}
		var d deviation
		if readJSON(t, filepath.Join(dir, "deviation.json"), &d) {
			f.deviation = &d
		}
		out = append(out, f)
	}
	if len(out) == 0 {
		t.Fatal("no fixtures")
	}
	return out
}

// expectation returns what Load must produce for a fixture.
//
// An oracle case expects upstream's result. A result that is not a mapping
// (a list, a scalar, None) is expected as an empty map: Load returns only
// mappings, and both are the same "no review" to the caller. The trace must
// name upstream's winning tactic.
func expectation(t *testing.T, f fixture) (want any, tactic string) {
	t.Helper()
	if f.deviation != nil {
		if f.deviation.Reason == "" {
			t.Fatalf("%s: deviation.json without a reason", f.name)
		}
		return f.deviation.Want, f.deviation.Tactic
	}
	tactic, ok := upstreamTactics[f.golden.UpstreamTactic]
	if !ok {
		t.Fatalf("%s: upstream won with tactic %s, which is not ported; add a deviation.json",
			f.name, f.golden.UpstreamTactic)
	}
	if f.golden.UpstreamType != "dict" {
		if tactic != TacticDirect && tactic != TacticNone {
			t.Fatalf("%s: upstream returned a %s from tactic %s, which Load does not accept; add a deviation.json",
				f.name, f.golden.UpstreamType, f.golden.UpstreamTactic)
		}
		return map[string]any{}, tactic
	}
	return f.golden.Upstream, tactic
}

// jsonNormalize turns a Load result into the JSON shape of the goldens.
func jsonNormalize(t *testing.T, v any) any {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var out any
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// yaml11Bools are the YAML 1.1 boolean words PyYAML resolves and yaml.v3
// (YAML 1.2) keeps as strings: drift rule R1 in testdata/README.md.
var yaml11Bools = map[string]bool{
	"yes": true, "Yes": true, "YES": true, "on": true, "On": true, "ON": true,
	"no": false, "No": false, "NO": false, "off": false, "Off": false, "OFF": false,
}

// exponentWithoutDot matches floats such as 1e3 that YAML 1.2 resolves as
// numbers and PyYAML (YAML 1.1 needs a dot) keeps as strings: drift rule R2.
var exponentWithoutDot = regexp.MustCompile(`^[-+]?[0-9]+[eE][-+]?[0-9]+$`)

// sameParse compares an oracle value with a JSON-normalized Load value,
// accepting only the documented PyYAML/yaml.v3 drift rules. It returns the
// rules it used.
func sameParse(want, got any, rules map[string]bool) bool {
	switch w := want.(type) {
	case map[string]any:
		g, ok := got.(map[string]any)
		if !ok || len(g) != len(w) {
			return false
		}
		for k, wv := range w {
			gv, ok := g[k]
			if !ok || !sameParse(wv, gv, rules) {
				return false
			}
		}
		return true
	case []any:
		g, ok := got.([]any)
		if !ok || len(g) != len(w) {
			return false
		}
		for i := range w {
			if !sameParse(w[i], g[i], rules) {
				return false
			}
		}
		return true
	case bool:
		if s, ok := got.(string); ok {
			if b, known := yaml11Bools[s]; known && b == w {
				rules["R1"] = true
				return true
			}
		}
	case string:
		if f, ok := got.(float64); ok && exponentWithoutDot.MatchString(w) {
			if v, err := strconv.ParseFloat(w, 64); err == nil && v == f {
				rules["R2"] = true
				return true
			}
		}
	}
	return reflect.DeepEqual(want, got)
}

// TestUpstreamGoldens compares Load with upstream's load_yaml on every
// fixture, as parsed structures (spec P4 §3, "Goldens").
func TestUpstreamGoldens(t *testing.T) {
	for _, f := range loadFixtures(t) {
		t.Run(f.name, func(t *testing.T) {
			want, wantTactic := expectation(t, f)
			got, trace := Load(f.input, f.keys())
			if got == nil {
				t.Fatal("Load returned a nil map")
			}
			if g := jsonNormalize(t, got); !sameParse(want, g, map[string]bool{}) {
				t.Errorf("result mismatch\n got: %#v\nwant: %#v", g, want)
			}
			if trace.Tactic != wantTactic {
				t.Errorf("trace = %q, want %q", trace.Tactic, wantTactic)
			}
		})
	}
}

// TestDriftRules documents, on fixture rv_yaml11_words, each PyYAML (YAML
// 1.1) / yaml.v3 (YAML 1.2) difference the golden comparison tolerates, and
// checks that each tolerated rule is actually needed there, so a rule
// cannot hide a regression unseen. See testdata/README.md, "Dialect drift".
func TestDriftRules(t *testing.T) {
	var f fixture
	for _, c := range loadFixtures(t) {
		if c.name == "rv_yaml11_words" {
			f = c
		}
	}
	if f.name == "" {
		t.Fatal("fixture rv_yaml11_words missing")
	}
	got, _ := Load(f.input, f.keys())
	review, _ := got["review"].(map[string]any)
	// field: upstream (PyYAML) value, Load (yaml.v3) value.
	for field, pair := range map[string][2]any{
		"relevant_tests":    {true, "Yes"},                // R1: YAML 1.1 boolean word
		"security_concerns": {false, "No"},                // R1 (IsNo accepts both)
		"issue_header":      {true, "on"},                 // R1
		"end_line":          {"1e3", 1000.0},              // R2: exponent without a dot
		"start_line":        {493, 493},                   // 0755 is octal in both
		"issue_content":     {"2024-01-01", "2024-01-01"}, // date: str() upstream, kept string here
	} {
		upstream := f.golden.Upstream.(map[string]any)["review"].(map[string]any)[field]
		if !reflect.DeepEqual(jsonNormalize(t, upstream), jsonNormalize(t, pair[0])) {
			t.Errorf("%s: upstream %#v, documented %#v", field, upstream, pair[0])
		}
		if !reflect.DeepEqual(review[field], pair[1]) {
			t.Errorf("%s: Load %#v, documented %#v", field, review[field], pair[1])
		}
	}
	rules := map[string]bool{}
	if !sameParse(f.golden.Upstream, jsonNormalize(t, got), rules) {
		t.Fatal("rv_yaml11_words does not match under the drift rules")
	}
	for _, r := range []string{"R1", "R2"} {
		if !rules[r] {
			t.Errorf("drift rule %s is not exercised", r)
		}
	}
}

// TestCanaryTrace is the [canary] table (spec P4 §3): for each kept tactic,
// the trace of its canary fixture names exactly that tactic, and running the
// chain up to (not including) the tactic repairs nothing, so no earlier
// tactic and not the direct parse can rescue the fixture.
func TestCanaryTrace(t *testing.T) {
	tests := []struct {
		fixture string
		tactic  string
	}{
		{"c01_block_scalar_keys", tacticBlockScalarKeys},
		{"c02_explicit_indent", tacticExplicitIndent},
		{"c04_fenced_snippet", tacticFencedSnippet},
		{"c05_strip_braces", tacticStripBraces},
		{"c06_key_window", tacticKeyWindow},
		{"c07_strip_plus", tacticStripPlus},
		{"c08_diff_markers", tacticDiffMarkers},
		{"c09_tabs_to_spaces", tacticTabsToSpaces},
		{"c11_root_pipe", tacticRootPipe},
		{"c12_reencode", tacticReencode},
	}
	byName := map[string]fixture{}
	for _, f := range loadFixtures(t) {
		byName[f.name] = f
	}
	// Every tactic of the chain has a row (checked on the table, so the
	// check does not depend on which subtests run).
	covered := map[string]bool{}
	for _, tt := range tests {
		covered[tt.tactic] = true
	}
	for _, tc := range chain {
		if !covered[tc.name] {
			t.Errorf("tactic %s has no canary fixture", tc.name)
		}
	}
	for _, tt := range tests {
		t.Run(tt.fixture, func(t *testing.T) {
			f, ok := byName[tt.fixture]
			if !ok {
				t.Fatalf("fixture %s missing", tt.fixture)
			}
			if f.meta.Canary != tt.tactic {
				t.Fatalf("case.json canary = %q, want %q", f.meta.Canary, tt.tactic)
			}
			got, trace := Load(f.input, f.keys())
			if trace.Tactic != tt.tactic || len(got) == 0 {
				t.Fatalf("trace = %q (result empty: %v), want %q", trace.Tactic, len(got) == 0, tt.tactic)
			}
			idx := chainIndex(t, tt.tactic)
			if before, trace := load(f.input, f.keys(), chain[:idx]); len(before) != 0 {
				t.Fatalf("an earlier step repaired the canary: trace %q", trace.Tactic)
			}
		})
	}
}

func chainIndex(t *testing.T, name string) int {
	t.Helper()
	for i, tc := range chain {
		if tc.name == name {
			return i
		}
	}
	t.Fatalf("tactic %s is not in the chain", name)
	return -1
}

// TestChainOrder pins the chain to upstream tactics 1, 2, 4, 5, 6, 7, 8, 9,
// 11 and 12 in upstream order (DQ-8).
func TestChainOrder(t *testing.T) {
	want := []string{
		tacticBlockScalarKeys, tacticExplicitIndent, tacticFencedSnippet, tacticStripBraces,
		tacticKeyWindow, tacticStripPlus, tacticDiffMarkers, tacticTabsToSpaces,
		tacticRootPipe, tacticReencode,
	}
	var got []string
	for _, tc := range chain {
		got = append(got, tc.name)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("chain = %v, want %v", got, want)
	}
}

// TestTraceCarriesOnlyNames checks that a trace is always one of the fixed
// names, whatever the input: it never carries content.
func TestTraceCarriesOnlyNames(t *testing.T) {
	allowed := map[string]bool{TacticDirect: true, TacticNone: true}
	for _, tc := range chain {
		allowed[tc.name] = true
	}
	seen := map[string]bool{}
	for _, f := range loadFixtures(t) {
		_, trace := Load(f.input, f.keys())
		if !allowed[trace.Tactic] {
			t.Errorf("%s: trace %q is not a tactic name", f.name, trace.Tactic)
		}
		seen[trace.Tactic] = true
	}
	var names []string
	for n := range seen {
		names = append(names, n)
	}
	sort.Strings(names)
	t.Logf("trace values seen: %v", names)
}
