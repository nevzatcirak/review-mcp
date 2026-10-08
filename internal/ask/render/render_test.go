package render

import (
	"os/exec"
	"slices"
	"strings"
	"testing"

	"github.com/nevzatcirak/review-mcp/internal/ask"
	"github.com/nevzatcirak/review-mcp/internal/llmrun"
	"github.com/nevzatcirak/review-mcp/internal/provider"
)

var _ ask.ProviderRenderer = Provider

var (
	capsGitea = provider.Capabilities{GFM: true, MarkdownTables: true, Labels: true, InlineComments: true}
	capsBB    = provider.Capabilities{GFM: false, MarkdownTables: true, InlineComments: true}
)

// bannerAsk is the banner of sample(): 1 of 2 reviewable files used.
const bannerAsk = "**Partial answer: 1 of 2 changed files was used for this answer. 1 file was not reviewed (see Coverage); nothing is concluded about it.**"

// sample is a partial result (one file left out for the budget); the
// filtered file does not count.
func sample() *ask.Result {
	return &ask.Result{
		Question: "Which files change the request validation?",
		Answer:   "Only `src/app.go` changes it.\n\n- one\n- two",
		Coverage: llmrun.Coverage{
			Included: []string{"src/app.go"},
			Clipped:  []string{},
			Omitted:  llmrun.OmittedFiles{Added: []string{}, Modified: []string{"src/big.go"}, Deleted: []string{}},
			Skipped:  []llmrun.SkippedFile{},
			Filtered: []llmrun.SkippedFile{{Path: "go.sum", Reason: "lockfile_or_minified"}},
		},
		Notes: []string{ask.NoteTruncated},
	}
}

func TestClientLayout(t *testing.T) {
	got := Client(sample())
	want := bannerAsk + "\n\n## Question\n\n```\nWhich files change the request validation?\n```\n\n" +
		"## Answer\n\nOnly `src/app.go` changes it.\n\n- one\n- two\n\n" +
		"## Coverage\n\n- Included: 1 file\n- Omitted: 2 files\n\n" +
		"Left out to fit the context window (modified files) (1):\n\n- `src/big.go`\n\n" +
		"Filtered: `lockfile_or_minified` (1):\n\n- `go.sum`\n\n" +
		"## Notes\n\n- The answer was cut off by the model's output limit.\n"
	if got != want {
		t.Errorf("client markdown:\n%s\n--- want ---\n%s", got, want)
	}
}

func TestClientAdaptiveFenceAndAnswerAsIs(t *testing.T) {
	r := sample()
	r.Question = "How does ```` this ``` work?\n/close"
	r.Answer = "# Heading from the model\n<b>bold</b> and *emphasis*"
	got := Client(r)
	if !strings.Contains(got, "`````\nHow does ```` this ``` work?\n/close\n`````\n") {
		t.Errorf("the question is not in an adaptive fence:\n%s", got)
	}
	if !strings.Contains(got, "\n## Answer\n\n# Heading from the model\n<b>bold</b> and *emphasis*\n") {
		t.Errorf("the answer is not shown as is:\n%s", got)
	}
	// The client profile does not sanitize slashes: it is not published.
	if !strings.Contains(got, "\n/close\n") {
		t.Errorf("the client profile changed the question:\n%s", got)
	}
}

func TestClientWithoutAnswerAndWithoutNotes(t *testing.T) {
	r := sample()
	r.Answer, r.Notes = "", []string{}
	got := Client(r)
	if strings.Contains(got, "## Answer") || strings.Contains(got, "## Notes") {
		t.Errorf("empty sections are shown:\n%s", got)
	}
	if !strings.Contains(got, "## Coverage") {
		t.Errorf("the coverage section is always present:\n%s", got)
	}
	if Client(nil) != "" || Provider(nil, capsGitea) != "" {
		t.Error("a nil result renders as the empty string")
	}
}

func TestProviderLayouts(t *testing.T) {
	gitea := Provider(sample(), capsGitea)
	wantGitea := "> ⚠️ " + bannerAsk + "\n\n### **Ask** ❓\n```\nWhich files change the request validation?\n```\n\n" +
		"### **Answer:**\nOnly `src/app.go` changes it.\n\n- one\n- two\n\n" +
		"### 📂 Coverage\n"
	if !strings.HasPrefix(gitea, wantGitea) || !strings.Contains(gitea, "\n### 📝 Notes\n") {
		t.Errorf("gitea comment:\n%s", gitea)
	}
	bb := Provider(sample(), capsBB)
	wantBB := bannerAsk + "\n\n### Question\n```\nWhich files change the request validation?\n```\n\n" +
		"### Answer\nOnly `src/app.go` changes it.\n\n- one\n- two\n\n" +
		"### Coverage\n"
	if !strings.HasPrefix(bb, wantBB) || !strings.Contains(bb, "\n### Notes\n") {
		t.Errorf("bitbucket comment:\n%s", bb)
	}
	for _, s := range []string{gitea, bb} {
		if strings.Contains(s, "<") && strings.Contains(s, "<table") {
			t.Errorf("unexpected HTML:\n%s", s)
		}
		if !strings.Contains(s, "Included: 1 file") {
			t.Errorf("no coverage section:\n%s", s)
		}
	}
}

// noSlashLine reports the first line of body that starts with "/", treating
// "\n" and "\r" as line breaks.
func noSlashLine(body string) (string, bool) {
	for _, l := range strings.FieldsFunc(body, func(r rune) bool { return r == '\n' || r == '\r' }) {
		if strings.HasPrefix(l, "/") {
			return l, true
		}
	}
	return "", false
}

// TestProviderSanitizesQuickActions [canary]: an answer containing "\n/close"
// is published as "\n /close", also after "\r", at the very start, and in
// the question. Removing provider.SanitizeQuickActions fails this test.
func TestProviderSanitizesQuickActions(t *testing.T) {
	for _, caps := range []provider.Capabilities{capsGitea, capsBB} {
		r := sample()
		r.Answer = "Fine.\n/close\r/assign me\n//double"
		r.Question = "Why?\n/lgtm\r/approve"
		body := Provider(r, caps)
		for _, want := range []string{"Fine.\n /close\r /assign me\n //double", "Why?\n /lgtm\r /approve"} {
			if !strings.Contains(body, want) {
				t.Errorf("gfm=%v: body lacks %q:\n%q", caps.GFM, want, body)
			}
		}
		if l, bad := noSlashLine(body); bad {
			t.Errorf("gfm=%v: a line starts with /: %q", caps.GFM, l)
		}
	}
	// An answer that starts with "/".
	r := sample()
	r.Answer = "/close this"
	for _, caps := range []provider.Capabilities{capsGitea, capsBB} {
		body := Provider(r, caps)
		if !strings.Contains(body, "\n /close this\n") {
			t.Errorf("gfm=%v: a leading slash is not neutralized:\n%q", caps.GFM, body)
		}
		if l, bad := noSlashLine(body); bad {
			t.Errorf("gfm=%v: a line starts with /: %q", caps.GFM, l)
		}
	}
	// A question that starts with "/" (inside the fence).
	r = sample()
	r.Question = "/close"
	if body := Provider(r, capsBB); !strings.Contains(body, "```\n /close\n```") {
		t.Errorf("a question starting with / is not neutralized:\n%q", body)
	}
}

// TestNoYAMLInDependencyClosure: the ask renderers keep internal/ask's hard
// rule (spec P5 §3): no YAML or repair code in their dependency closure.
func TestNoYAMLInDependencyClosure(t *testing.T) {
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go tool not found")
	}
	out, err := exec.Command(goBin, "list", "-deps", "-test=false", ".").Output() //nolint:gosec // fixed arguments
	if err != nil {
		t.Skipf("go list failed: %v", err)
	}
	listed := strings.Fields(string(out))
	if !slices.Contains(listed, "github.com/nevzatcirak/review-mcp/internal/ask") {
		t.Fatalf("go list -deps did not reach internal/ask: %v", listed)
	}
	for _, f := range []string{
		"github.com/nevzatcirak/review-mcp/internal/yamlrepair",
		"github.com/nevzatcirak/review-mcp/internal/review",
		"gopkg.in/yaml.v3", "gopkg.in/yaml.v2",
	} {
		if slices.Contains(listed, f) {
			t.Errorf("internal/ask/render depends on %s", f)
		}
	}
}

// completeSample is sample() with nothing left out: the filtered file still
// does not count.
func completeSample() *ask.Result {
	r := sample()
	r.Coverage.Omitted.Modified = []string{}
	return r
}

// TestPartialBannerIsFirstLine: the pr_ask banner leads every rendering.
func TestPartialBannerIsFirstLine(t *testing.T) {
	r := sample()
	for name, got := range map[string]string{
		"client":    Client(r),
		"gitea":     Provider(r, capsGitea),
		"bitbucket": Provider(r, capsBB),
	} {
		first, rest, _ := strings.Cut(got, "\n")
		want := bannerAsk
		if name == "gitea" {
			want = "> ⚠️ " + bannerAsk
		}
		if first != want || !strings.HasPrefix(rest, "\n") {
			t.Errorf("%s: first line = %q", name, first)
		}
	}
}

// TestCompleteAnswerHasNoBanner: a complete run, with a filtered file only,
// is not partial.
func TestCompleteAnswerHasNoBanner(t *testing.T) {
	r := completeSample()
	for name, got := range map[string]string{
		"client":    Client(r),
		"gitea":     Provider(r, capsGitea),
		"bitbucket": Provider(r, capsBB),
	} {
		if strings.Contains(got, "Partial") || strings.Contains(got, "⚠️") {
			t.Errorf("%s: a complete answer has a banner:\n%s", name, got)
		}
	}
	// A clipped file alone makes it partial.
	r.Coverage.Clipped = []string{"src/big.go"}
	if got := Client(r); !strings.HasPrefix(got, "**Partial answer: 1 of 2 changed files was used for this answer.") {
		t.Errorf("clipped file: no banner:\n%s", got)
	}
}

// TestAnswerBannerWording pins the plural forms.
func TestAnswerBannerWording(t *testing.T) {
	cov := llmrun.Coverage{Included: []string{"a.go", "b.go", "c.go"}, Clipped: []string{}, Omitted: llmrun.OmittedFiles{
		Added: []string{"d.go", "e.go"}, Modified: []string{}, Deleted: []string{}}}
	want := "**Partial answer: 3 of 5 changed files were used for this answer. 2 files were not reviewed (see Coverage); nothing is concluded about them.**"
	if got := Client(&ask.Result{Question: "q", Coverage: cov}); !strings.HasPrefix(got, want+"\n\n") {
		t.Errorf("banner:\n%s", got)
	}
}
