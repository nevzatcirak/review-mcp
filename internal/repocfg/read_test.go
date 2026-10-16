package repocfg

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/nevzatcirak/review-mcp/internal/config"
	"github.com/nevzatcirak/review-mcp/internal/provider"
)

const (
	baseSHA = "base0000000000000000000000000000000000000"
	headSHA = "head1111111111111111111111111111111111111"
)

// fakeFiles serves files by revision and records every request.
type fakeFiles struct {
	files map[string]string // rev + " " + path -> content
	err   error
	reqs  []string // rev + " " + path
}

func (f *fakeFiles) GetFileAt(_ context.Context, _ provider.PRRef, path, rev string) ([]byte, error) {
	f.reqs = append(f.reqs, rev+" "+path)
	if f.err != nil {
		return nil, f.err
	}
	c, ok := f.files[rev+" "+path]
	if !ok {
		return nil, provider.ErrNotFound
	}
	return []byte(c), nil
}

var testPR = &provider.PullRequest{BaseSHA: baseSHA, HeadSHA: headSHA}

func repoCfg() config.RepoConfig {
	return config.RepoConfig{Enabled: true, Path: ".review-mcp.toml"}
}

func readBase(t *testing.T, content string) *File {
	t.Helper()
	fg := &fakeFiles{files: map[string]string{baseSHA + " .review-mcp.toml": content}}
	return Read(context.Background(), nil, repoCfg(), fg, provider.PRRef{}, testPR)
}

func TestReadAbsent(t *testing.T) {
	fg := &fakeFiles{}
	f := Read(context.Background(), nil, repoCfg(), fg, provider.PRRef{}, testPR)
	if f.state != stateAbsent || len(f.Notes()) != 0 {
		t.Fatalf("state %v, notes %q: a missing file is the normal case, with no note", f.state, f.Notes())
	}
	if len(fg.reqs) != 1 {
		t.Errorf("requests %q, want exactly one read", fg.reqs)
	}
}

func TestReadOff(t *testing.T) {
	fg := &fakeFiles{files: map[string]string{baseSHA + " .review-mcp.toml": "[output]\nlanguage = \"tr\"\n"}}
	cfg := repoCfg()
	cfg.Enabled = false
	f := Read(context.Background(), nil, cfg, fg, provider.PRRef{}, testPR)
	if f.state != stateOff || len(fg.reqs) != 0 || len(f.Notes()) != 0 {
		t.Fatalf("state %v, requests %q, notes %q: repo_config.enabled=false reads nothing", f.state, fg.reqs, f.Notes())
	}
}

func TestReadEmptyAndValid(t *testing.T) {
	f := readBase(t, "")
	if !f.Parsed() || len(f.values) != 0 || len(f.Notes()) != 0 {
		t.Errorf("an empty file: parsed %v, values %v, notes %q", f.Parsed(), f.values, f.Notes())
	}
	f = readBase(t, "# comment\n[output]\nlanguage = \"tr\"\n\n[review]\nmax_findings = 2\nfocuses = [\"security\"]\n[ignore]\nglob = [\"**/*_gen.go\"]\n")
	if !f.Parsed() || len(f.ignored) != 0 || len(f.Notes()) != 0 {
		t.Fatalf("a valid file: parsed %v, ignored %q, notes %q", f.Parsed(), f.ignored, f.Notes())
	}
	for _, k := range []string{"output.language", "review.max_findings", "review.focuses", "ignore.glob"} {
		if _, ok := f.values[k]; !ok {
			t.Errorf("allowed key %s was not collected: %v", k, f.values)
		}
	}
}

func TestReadSyntaxError(t *testing.T) {
	const quoted = "SYNTAX-VALUE-ZQ7X"
	f := readBase(t, "[output]\nlanguage = \"tr\"\nbroken = "+quoted+" oops\n")
	if f.state != stateSyntax || f.line != 3 {
		t.Fatalf("state %v line %d, want a syntax error at line 3", f.state, f.line)
	}
	want := "`.review-mcp.toml` on the target branch could not be read as TOML (line 3) and was ignored."
	if got := f.Notes(); len(got) != 1 || got[0] != want {
		t.Errorf("notes %q, want %q", got, want)
	}
	if strings.Contains(strings.Join(f.Notes(), ""), quoted) {
		t.Error("a note quotes the file")
	}
	if f.Parsed() || len(f.values) != 0 {
		t.Error("a file with a syntax error contributes nothing")
	}
}

func TestReadOversized(t *testing.T) {
	big := "[review]\nextra_instructions = \"" + strings.Repeat("x", MaxFileBytes) + "\"\n"
	f := readBase(t, big)
	want := "`.review-mcp.toml` on the target branch is larger than 16 KiB and was ignored."
	if f.state != stateTooLarge || len(f.Notes()) != 1 || f.Notes()[0] != want {
		t.Fatalf("state %v, notes %q, want %q", f.state, f.Notes(), want)
	}
	exact := "# " + strings.Repeat("x", MaxFileBytes-3) + "\n"
	if len(exact) != MaxFileBytes || !readBase(t, exact).Parsed() {
		t.Error("a file of exactly 16 KiB is read")
	}
	// The provider's own cap (diff.max_file_bytes) is the same case.
	fg := &fakeFiles{err: provider.ErrTooLarge}
	if f := Read(context.Background(), nil, repoCfg(), fg, provider.PRRef{}, testPR); f.state != stateTooLarge {
		t.Errorf("a provider too_large: state %v", f.state)
	}
}

func TestReadUnreadable(t *testing.T) {
	want := "`.review-mcp.toml` on the target branch could not be read and was ignored."
	for name, err := range map[string]error{
		"auth":      &provider.Error{Class: provider.ClassAuth},
		"transport": &provider.Error{Class: provider.ClassTransport},
		"plain":     errors.New("boom"),
	} {
		fg := &fakeFiles{err: err}
		f := Read(context.Background(), nil, repoCfg(), fg, provider.PRRef{}, testPR)
		if f.state != stateUnreadable || len(f.Notes()) != 1 || f.Notes()[0] != want {
			t.Errorf("%s: state %v, notes %q", name, f.state, f.Notes())
		}
	}
	// No base revision: nothing can be read at the base, and the head is no substitute.
	fg := &fakeFiles{files: map[string]string{headSHA + " .review-mcp.toml": "[output]\nlanguage = \"tr\"\n"}}
	for name, pr := range map[string]*provider.PullRequest{"nil": nil, "no base": {HeadSHA: headSHA}} {
		f := Read(context.Background(), nil, repoCfg(), fg, provider.PRRef{}, pr)
		if f.state != stateUnreadable || len(fg.reqs) != 0 {
			t.Errorf("%s: state %v, requests %q: with no base revision nothing is read", name, f.state, fg.reqs)
		}
	}
}

// TestReadBaseOnly: the base revision's file is used and the head revision's
// file is never requested, however different it is. [canary]
func TestReadBaseOnly(t *testing.T) {
	fg := &fakeFiles{files: map[string]string{
		baseSHA + " .review-mcp.toml": "[output]\nlanguage = \"tr\"\n",
		headSHA + " .review-mcp.toml": "[output]\nlanguage = \"de\"\n[llm]\nmodel = \"x\"\n",
	}}
	f := Read(context.Background(), nil, repoCfg(), fg, provider.PRRef{}, testPR)
	if got := f.values["output.language"]; got != "tr" {
		t.Errorf("output.language = %v, want the base revision's value", got)
	}
	if len(f.ignored) != 0 {
		t.Errorf("ignored %q: the head revision's file was read", f.ignored)
	}
	if want := []string{baseSHA + " .review-mcp.toml"}; fmt.Sprint(fg.reqs) != fmt.Sprint(want) {
		t.Errorf("requests %q, want only %q", fg.reqs, want)
	}
}

func TestReadConfiguredPath(t *testing.T) {
	fg := &fakeFiles{files: map[string]string{baseSHA + " conf/review.toml": "[llm]\nmodel = \"x\"\n"}}
	cfg := repoCfg()
	cfg.Path = "conf/review.toml"
	f := Read(context.Background(), nil, cfg, fg, provider.PRRef{}, testPR)
	want := "Settings in `conf/review.toml` that a repository may not change were ignored: llm.model."
	if got := f.Notes(); len(got) != 1 || got[0] != want {
		t.Errorf("notes %q, want %q", got, want)
	}
}

// TestForbiddenKeys: a key outside the allow-list is ignored and named,
// whatever its family. [canary]
func TestForbiddenKeys(t *testing.T) {
	const quoted = "SECRET-VALUE-ZQ7X"
	f := readBase(t, strings.Join([]string{
		"[llm]", `model = "` + quoted + `"`, `base_url = "https://llm.example"`, "max_retries = 5",
		"[[llm.fallback]]", `model = "m"`,
		"[gitea]", `token = "` + quoted + `"`,
		"[serve]", `listen = ":1"`,
		"[context.repo]", `cache_dir = "/tmp"`,
		"[log]", `level = "debug"`,
		"[repo_config]", `path = "x"`, "enabled = false",
		"[diff]", "max_diff_bytes = 1", `ignore_generated_frameworks = ["protobuf"]`,
		"[review]", "max_guidelines_tokens = 5000", "max_findings = 2",
		"[nosuch]", "thing = 1",
		"",
	}, "\n"))
	if !f.Parsed() {
		t.Fatalf("not parsed: state %v", f.state)
	}
	for _, k := range []string{"llm.model", "llm.base_url", "llm.max_retries", "llm.fallback", "gitea.token", "serve.listen",
		"context.repo.cache_dir", "log.level", "repo_config.path", "repo_config.enabled", "diff.max_diff_bytes",
		"review.max_guidelines_tokens", "nosuch.thing"} {
		if !contains(f.ignored, k) {
			t.Errorf("%s was not ignored: %q", k, f.ignored)
		}
		if _, ok := f.values[k]; ok {
			t.Errorf("%s reached the allowed values", k)
		}
	}
	for _, k := range []string{"diff.ignore_generated_frameworks", "review.max_findings"} {
		if _, ok := f.values[k]; !ok || contains(f.ignored, k) {
			t.Errorf("allowed key %s: values %v, ignored %q", k, f.values, f.ignored)
		}
	}
	text := strings.Join(f.Notes(), "\n")
	if strings.Contains(text, quoted) || strings.Contains(text, "llm.example") {
		t.Errorf("a note quotes a value of the file: %s", text)
	}
}

func TestIgnoredNote(t *testing.T) {
	f := readBase(t, "[llm]\nmodel = \"x\"\n[serve]\nlisten = \":1\"\n")
	want := "Settings in `.review-mcp.toml` that a repository may not change were ignored: llm.model, serve.listen."
	if got := f.Notes(); len(got) != 1 || got[0] != want {
		t.Errorf("notes %q, want %q", got, want)
	}
}

func TestIgnoredNamesCapped(t *testing.T) {
	var b strings.Builder
	b.WriteString("[x]\n")
	for i := 0; i < 14; i++ {
		fmt.Fprintf(&b, "k%02d = 1\n", i)
	}
	f := readBase(t, b.String())
	if len(f.ignored) != 14 {
		t.Fatalf("ignored %d, want 14", len(f.ignored))
	}
	names := f.IgnoredNames()
	if len(names) != MaxIgnoredNames+1 || names[MaxIgnoredNames] != "and 4 more" || names[0] != "x.k00" {
		t.Errorf("names %q, want 10 names then \"and 4 more\"", names)
	}
	note := f.Notes()[0]
	if !strings.HasSuffix(note, "x.k09, and 4 more.") || strings.Contains(note, "k10") {
		t.Errorf("note %q", note)
	}
}

func TestKeyNamesAreSanitised(t *testing.T) {
	long := strings.Repeat("a", 100)
	f := readBase(t, "\"review.max_findings\" = 3\n[x]\n\"line\\nbreak `tick` \\u202e\" = 1\n"+long+" = 2\n")
	if len(f.ignored) != 3 {
		t.Fatalf("ignored %q", f.ignored)
	}
	for _, n := range f.ignored {
		if len(n) > MaxKeyBytes {
			t.Errorf("name %q is %d bytes, cap %d", n, len(n), MaxKeyBytes)
		}
		for _, r := range n {
			if !isKeyRune(r) {
				t.Errorf("name %q holds %q", n, r)
			}
		}
	}
	if _, ok := f.values["review.max_findings"]; ok {
		t.Error("a quoted dotted key passed for the section path review.max_findings")
	}
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func isKeyRune(r rune) bool {
	return r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-' || r == '.'
}
