package gitctx

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
)

// consumerFiles is a consumer repository: uses of a renamed field in code,
// in a test (tests are consumers), in a longer word (not a use), in an
// ignored directory and in a minified file (not searched), and a route
// prefix inside a string.
func consumerFiles() map[string]string {
	many := ""
	for i := 1; i <= 8; i++ {
		many += fmt.Sprintf("total += row%d.userName.length\n", i)
	}
	return map[string]string{
		"web/__tests__/users.test.ts": "expect(u.userName).toBe('a')\n",
		"web/src/api/users.ts":        numbered(12, 5, "const n = user.userName;") + "fetch('/users/' + id)\n",
		"web/src/legacy.ts":           "const a = userNameLegacy;\nconst b = x.userName;\nconst c = 1;\nload('/usersettings')\n",
		"web/src/many.ts":             many,
		"docs/notes.md":               "userName is documented here\n",
		"web/vendor/lib.min.js":       "a.userName;\n",
	}
}

type termHit struct {
	term, path string
	line       int
}

func termHits(hits []Hit) []termHit {
	out := make([]termHit, len(hits))
	for i, h := range hits {
		out[i] = termHit{h.Symbol, h.Path, h.Line}
	}
	return out
}

// TestSearchTermsRealRepository: a consumer search on a real repository
// (spec §2.2, §2.6): identifiers are word-bounded and routes are not; the
// filter's files are not searched and test files are; each term keeps its
// first hits by path, then line, up to the cap, and Found counts them all;
// the total cap cuts across terms; a term without hits adds nothing; the
// context lines come from the blob; Paths confines the search.
func TestSearchTermsRealRepository(t *testing.T) {
	r, co := localCheckout(t, consumerFiles(), "")
	flt := testFilter(t, []string{"docs/**"})
	ctx := context.Background()
	terms := []Term{{Text: "userName", Word: true}, {Text: "/users", Word: false}, {Text: "missingName", Word: true}}

	res, err := r.SearchTerms(ctx, co, TermQuery{Terms: terms, Filter: flt, MaxPerTerm: 5, MaxTotal: 30, ContextLines: 2})
	if err != nil {
		t.Fatal(err)
	}
	want := []termHit{
		{"userName", "web/__tests__/users.test.ts", 1}, {"userName", "web/src/api/users.ts", 5},
		{"userName", "web/src/legacy.ts", 2}, {"userName", "web/src/many.ts", 1}, {"userName", "web/src/many.ts", 2},
		{"/users", "web/src/api/users.ts", 13}, {"/users", "web/src/legacy.ts", 4},
	}
	if got := termHits(res.Hits); !slices.Equal(got, want) {
		t.Errorf("hits %v\nwant %v", got, want)
	}
	if res.Found != 11+2 || res.Searched != 3 || res.Skipped != 0 {
		t.Errorf("found %d, searched %d, skipped %d; want 13, 3, 0", res.Found, res.Searched, res.Skipped)
	}
	h := res.Hits[1]
	if h.StartLine != 3 || h.Snippet != "line 3\nline 4\nconst n = user.userName;\nline 6\nline 7" {
		t.Errorf("context of %s:%d: start %d, %q", h.Path, h.Line, h.StartLine, h.Snippet)
	}

	res, err = r.SearchTerms(ctx, co, TermQuery{Terms: terms, Filter: flt, MaxPerTerm: 5, MaxTotal: 3})
	if err != nil || len(res.Hits) != 3 || res.Found != 13 || res.Hits[2].Path != "web/src/legacy.ts" {
		t.Errorf("total cap: %v, found %d, %v", termHits(res.Hits), res.Found, err)
	}

	res, err = r.SearchTerms(ctx, co, TermQuery{Terms: terms, Filter: flt, Paths: []string{"web/src/api/"}, MaxPerTerm: 5, MaxTotal: 30})
	want = []termHit{{"userName", "web/src/api/users.ts", 5}, {"/users", "web/src/api/users.ts", 13}}
	if got := termHits(res.Hits); err != nil || !slices.Equal(got, want) || res.Found != 2 {
		t.Errorf("confined: %v, found %d, %v; want %v", got, res.Found, err, want)
	}

	res, err = r.SearchTerms(ctx, co, TermQuery{Terms: terms[2:], Filter: flt, MaxPerTerm: 5, MaxTotal: 30})
	if err != nil || len(res.Hits) != 0 || res.Found != 0 || res.Searched != 1 {
		t.Errorf("no hits: %+v, %v", res, err)
	}

	for _, bad := range []string{"/abs", "a//b", "a\nb"} {
		if _, err := r.SearchTerms(ctx, co, TermQuery{Terms: terms, Paths: []string{bad}}); ReasonOf(err) != ReasonUnsupported {
			t.Errorf("path %q: %v, want unsupported", bad, err)
		}
	}
}

// TestSearchTermsRunsOffline: every git process of a consumer search runs
// offline (no credential, protocol.allow=never, GIT_NO_LAZY_FETCH=1, the
// allowlisted environment); an identifier is searched with -w and a route
// without; the term is the value of -e, after -F and -I; a confining prefix
// is a literal pathspec.
func TestSearchTermsRunsOffline(t *testing.T) {
	resetSchemes()
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "http.extraHeader")
	t.Setenv("GIT_CONFIG_VALUE_0", "Authorization: token "+testToken)
	r, f := fakeRunner(t, fakeBehavior{SHA: fakeSHA, GrepOut: fakeSHA + ":src/use.ts\x003\x00needle\n"}, Options{})
	co := fakeCheckout(t, r)
	res, err := r.SearchTerms(context.Background(), co, TermQuery{
		Terms: []Term{{Text: "-needle", Word: true}, {Text: "/route", Word: false}}, Paths: []string{"src"},
		MaxPerTerm: 5, MaxTotal: 30,
	})
	if err != nil || len(res.Hits) != 2 {
		t.Fatalf("SearchTerms = %+v, %v", res, err)
	}
	var greps [][]string
	for _, c := range f.calls(t) {
		sub := c.subcommand()
		if sub == "--version" {
			continue
		}
		args := " " + strings.Join(c.Args, " ") + " "
		if !strings.Contains(args, " -c protocol.allow=never ") || strings.Contains(args, "protocol.https.allow") ||
			strings.Contains(args, "protocol.http.allow") || strings.Contains(args, testToken) {
			t.Errorf("git %s is not offline: %s", sub, args)
		}
		if v, ok := envValue(c.Env, "GIT_NO_LAZY_FETCH"); !ok || v != "1" {
			t.Errorf("git %s: GIT_NO_LAZY_FETCH = %q (set %v), want 1", sub, v, ok)
		}
		for _, kv := range c.Env {
			name, _, _ := strings.Cut(kv, "=")
			if strings.HasPrefix(name, "GIT_CONFIG_COUNT") || strings.HasPrefix(name, "GIT_CONFIG_KEY") ||
				strings.HasPrefix(name, "GIT_CONFIG_VALUE") || strings.Contains(kv, testToken) {
				t.Errorf("git %s: environment carries %s", sub, name)
			}
			if !offlineEnv[name] && !allowedEnv.MatchString(name) {
				t.Errorf("git %s got %s, which is not on the allowlist", sub, name)
			}
		}
		if sub == "grep" {
			greps = append(greps, c.Args)
		}
	}
	if len(greps) != 2 {
		t.Fatalf("%d git grep calls, want 2", len(greps))
	}
	for i, want := range []struct {
		term string
		word bool
	}{{"-needle", true}, {"/route", false}} {
		args := " " + strings.Join(greps[i], " ") + " "
		if strings.Contains(args, " -w ") != want.word {
			t.Errorf("grep %d: -w is %v, want %v", i, !want.word, want.word)
		}
		for _, flag := range []string{" -n ", " -z ", " -F ", " -I "} {
			if !strings.Contains(args, flag) {
				t.Errorf("grep %d lacks %s", i, strings.TrimSpace(flag))
			}
		}
		j := slices.Index(greps[i], "-e")
		if j < 0 || greps[i][j+1] != want.term || greps[i][j+2] != fakeSHA || greps[i][j+3] != "--" {
			t.Errorf("grep %d arguments after -e: %v", i, greps[i][max(j, 0):])
		}
		if !strings.Contains(args, " :(top,literal)src ") {
			t.Errorf("grep %d is not confined to src", i)
		}
	}
}

// TestSearchTermsFailures: an unusable commit is unsupported before any git
// call; a canceled context is its own error.
func TestSearchTermsFailures(t *testing.T) {
	r, f := fakeRunner(t, fakeBehavior{SHA: fakeSHA}, Options{})
	co := fakeCheckout(t, r)
	bad := co
	bad.HeadSHA = "main"
	if _, err := r.SearchTerms(context.Background(), bad, TermQuery{Terms: []Term{{Text: "abc"}}}); ReasonOf(err) != ReasonUnsupported {
		t.Errorf("unusable commit: %v", err)
	}
	if n := len(callsOf(f.calls(t), "grep")); n != 0 {
		t.Errorf("%d git grep calls", n)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := r.SearchTerms(ctx, co, TermQuery{Terms: []Term{{Text: "abc"}}}); !errors.Is(err, context.Canceled) {
		t.Errorf("canceled: %v", err)
	}
}
