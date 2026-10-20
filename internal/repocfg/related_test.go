package repocfg

import (
	"reflect"
	"slices"
	"strings"
	"testing"
)

// Related repositories declared in the repository config (v2.5 spec V-13b
// §3.2).

const declaredRepos = `[[related.repos]]
url = "https://git.example/team/web-ui"
branch = "main"
paths = ["src/api/", "lib"]

[[related.repos]]
url = "https://gitlab.example/grp/mobile/-/tree/release/2"
`

// TestRelatedReposOff [canary 2]: with related.from_repo_config false (the
// default) the declarations are not kept: related.repos is listed in
// repo_config.ignored with the one fixed note, and in no other note; the
// rest of the file applies as usual.
func TestRelatedReposOff(t *testing.T) {
	_, o := applyText(t, declaredRepos+"\n[review]\nrequire_tests = false\n")
	if o.Related != nil {
		t.Fatalf("declarations kept with the operator switch off: %+v", o.Related)
	}
	if !slices.Equal(o.Result.Ignored, []string{"related.repos"}) || !slices.Equal(o.Result.Applied, []string{"review.require_tests"}) ||
		o.Result.Status != StatusApplied {
		t.Fatalf("result %+v", o.Result)
	}
	want := "The repository config names related repositories, but reading them is turned off by the operator (related.from_repo_config)."
	if NoteRelatedOff != want {
		t.Fatalf("note %q", NoteRelatedOff)
	}
	n := 0
	for _, note := range o.Notes {
		switch {
		case note == want:
			n++
		case strings.Contains(note, "related.repos"):
			t.Errorf("another note names the key: %q", note)
		}
	}
	if n != 1 {
		t.Fatalf("notes %q", o.Notes)
	}
	// Only the declarations: nothing applied, the key ignored with the note.
	_, only := applyText(t, declaredRepos)
	if only.Result.Status != StatusNothingApplied || !slices.Equal(only.Result.Ignored, []string{"related.repos"}) ||
		!slices.Equal(only.Notes, []string{want}) {
		t.Fatalf("result %+v notes %q", only.Result, only.Notes)
	}
}

// TestRelatedReposCallRelated: a call that passes its own related argument
// drops the operator note (WP-13f): the switch changes nothing for it.
// related.repos stays in repo_config.ignored, and every other note stays.
func TestRelatedReposCallRelated(t *testing.T) {
	_, o := applyText(t, declaredRepos+"\n[review]\nrequire_tests = false\n")
	notes := slices.DeleteFunc(slices.Clone(o.Notes), func(n string) bool { return n == NoteRelatedOff })
	result := o.Result
	o.CallRelated()
	if slices.Contains(o.Notes, NoteRelatedOff) || !slices.Equal(o.Notes, notes) || len(notes) == 0 {
		t.Fatalf("notes %q, want %q", o.Notes, notes)
	}
	if !reflect.DeepEqual(o.Result, result) || !slices.Equal(o.Result.Ignored, []string{"related.repos"}) {
		t.Fatalf("result %+v, want %+v", o.Result, result)
	}
}

func applyOn(t *testing.T, content string) *Outcome {
	t.Helper()
	base := operatorConfig()
	base.Related.FromRepoConfig = true
	return Apply(base, readBase(t, content))
}

// TestRelatedReposOn: with related.from_repo_config the valid declarations
// are kept in file order with their branch and paths, the key is applied,
// and the operator's configuration is not changed by them.
func TestRelatedReposOn(t *testing.T) {
	o := applyOn(t, declaredRepos)
	want := []Declared{
		{URL: "https://git.example/team/web-ui", Branch: "main", Paths: []string{"src/api/", "lib"}},
		{URL: "https://gitlab.example/grp/mobile/-/tree/release/2"},
	}
	if !reflect.DeepEqual(o.Related, want) {
		t.Fatalf("declared %+v", o.Related)
	}
	if o.Result.Status != StatusApplied || !slices.Equal(o.Result.Applied, []string{"related.repos"}) || len(o.Result.Ignored) != 0 {
		t.Fatalf("result %+v", o.Result)
	}
	if !slices.Equal(o.Notes, []string{"Settings from `.review-mcp.toml` on the target branch were applied: related.repos."}) {
		t.Fatalf("notes %q", o.Notes)
	}
	inline := applyOn(t, "[related]\nrepos = [{ url = \"https://git.example/team/web-ui\" }]\n")
	if len(inline.Related) != 1 || inline.Related[0].URL != "https://git.example/team/web-ui" {
		t.Fatalf("inline form %+v %+v", inline.Related, inline.Result)
	}
}

// TestRelatedReposInvalidEntries: each invalid entry is reported like an
// invalid key (named by its 0-based index, with the invalid-key note) and
// the valid ones are kept; an entry after the fourth is invalid; a value
// that is not a list of tables makes the key invalid.
func TestRelatedReposInvalidEntries(t *testing.T) {
	entry := func(body string) string { return "[[related.repos]]\n" + body + "\n" }
	valid := entry(`url = "https://git.example/a/one"`)
	for name, bad := range map[string]string{ //nolint:gosec // synthetic fixtures, no real credentials
		"credentials":   `url = "https://user:pw@git.example/a/two"`,
		"not http":      `url = "ftp://git.example/a/two"`,
		"not absolute":  `url = "git.example/a/two"`,
		"bad branch":    "url = \"https://git.example/a/two\"\nbranch = \"a..b\"",
		"path outside":  "url = \"https://git.example/a/two\"\npaths = [\"../x\"]",
		"path .git":     "url = \"https://git.example/a/two\"\npaths = [\"src/.git\"]",
		"the root":      "url = \"https://git.example/a/two\"\npaths = [\"\"]",
		"no paths":      "url = \"https://git.example/a/two\"\npaths = []",
		"11 paths":      "url = \"https://git.example/a/two\"\npaths = [\"a\",\"b\",\"c\",\"d\",\"e\",\"f\",\"g\",\"h\",\"i\",\"j\",\"k\"]",
		"unknown key":   "url = \"https://git.example/a/two\"\nlabel = \"x\"",
		"no url":        `branch = "main"`,
		"url not text":  `url = 7`,
		"paths not str": "url = \"https://git.example/a/two\"\npaths = [1]",
	} {
		o := applyOn(t, valid+entry(bad)+entry(`url = "https://git.example/a/three"`))
		var urls []string
		for _, d := range o.Related {
			urls = append(urls, d.URL)
		}
		if !slices.Equal(urls, []string{"https://git.example/a/one", "https://git.example/a/three"}) ||
			!slices.Equal(o.Result.Ignored, []string{"related.repos[1]"}) || !slices.Equal(o.Result.Applied, []string{"related.repos"}) ||
			!slices.Contains(o.Notes, "The value of related.repos[1] in `.review-mcp.toml` is not valid and was ignored.") {
			t.Errorf("%s: kept %q, result %+v, notes %q", name, urls, o.Result, o.Notes)
		}
	}
	five := strings.Repeat(valid, 5)
	if o := applyOn(t, five); len(o.Related) != MaxRelatedRepos || !slices.Equal(o.Result.Ignored, []string{"related.repos[4]"}) {
		t.Errorf("five entries: %d kept, %+v", len(o.Related), o.Result)
	}
	allBad := applyOn(t, entry(`url = "ftp://git.example/a/two"`))
	if allBad.Related != nil || len(allBad.Result.Applied) != 0 || allBad.Result.Status != StatusNothingApplied ||
		!slices.Equal(allBad.Result.Ignored, []string{"related.repos[0]"}) {
		t.Errorf("only invalid entries: %+v", allBad.Result)
	}
	for _, bad := range []string{"[related]\nrepos = \"https://git.example/a/one\"\n", "[related]\nrepos = 3\n"} {
		if b := applyOn(t, bad); b.Related != nil || !slices.Equal(b.Result.Ignored, []string{"related.repos"}) {
			t.Errorf("%q: %+v", bad, b.Result)
		}
	}
	if b := applyOn(t, "[related]\nrepos = [\"https://git.example/a/one\"]\n"); b.Related != nil ||
		!slices.Equal(b.Result.Ignored, []string{"related.repos[0]"}) {
		t.Errorf("a list of strings: %+v", b.Result)
	}
}
