package render

import (
	"strings"
	"testing"

	"github.com/nevzatcirak/review-mcp/internal/provider"
	"github.com/nevzatcirak/review-mcp/internal/review"
)

var _ review.InlineRenderer = Inline

func TestInline(t *testing.T) {
	ki := &review.KeyIssue{
		RelevantFile: "src/app.go", IssueHeader: "Possible Bug",
		IssueContent: "The loop never stops when `max` is 0 <b>bold</b> & more.\n# not a heading",
		StartLine:    40, EndLine: 52, Snippet: "for {}", Link: "https://your-gitea.example/x#L40",
	}
	cases := []struct {
		name string
		ki   *review.KeyIssue
		caps provider.Capabilities
		want string
	}{
		{"gitea range", ki, capsGitea,
			"**Possible Issue**\n\nThe loop never stops when \\`max\\` is 0 &lt;b&gt;bold&lt;/b&gt; &amp; more.\n\\# not a heading\n\nLines 40–52\n"},
		{"bitbucket range", ki, capsBB,
			"**Possible Issue**\n\nThe loop never stops when \\`max\\` is 0 \\<b\\>bold\\</b\\> \\& more.\n\\# not a heading\n\nLines 40–52\n"},
		{"single line", &review.KeyIssue{IssueHeader: "Style\n**x**", IssueContent: "Rename it.", StartLine: 7, EndLine: 7}, capsBB,
			"**Style \\*\\*x\\*\\***\n\nRename it.\n"},
		{"no end line", &review.KeyIssue{IssueHeader: "Style", IssueContent: "Rename it.", StartLine: 7}, capsGitea,
			"**Style**\n\nRename it.\n"},
		{"empty header and content", &review.KeyIssue{StartLine: 3, EndLine: 4}, capsGitea,
			"**Issue**\n\nLines 3–4\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Inline(c.ki, c.caps)
			if got != c.want {
				t.Errorf("got\n%q\nwant\n%q", got, c.want)
			}
			if strings.Contains(got, "for {}") || strings.Contains(got, "```") {
				t.Errorf("the inline body carries a snippet")
			}
			if strings.Contains(strings.ReplaceAll(got, `\<`, ""), "<") {
				t.Errorf("the inline body carries an unescaped HTML tag")
			}
		})
	}
	if Inline(nil, capsGitea) != "" {
		t.Errorf("nil finding rendered")
	}
}

// TestOverviewLinksInlineComments: a finding with an inline comment links to
// it in every profile; the others keep their file link.
func TestOverviewLinksInlineComments(t *testing.T) {
	res := &review.Result{
		PR: basePR(), EnabledFields: allKeys, Coverage: baseCoverage(), Notes: []string{},
		Review: &review.Review{KeyIssuesToReview: []review.KeyIssue{
			{RelevantFile: "a.go", IssueHeader: "Inline", IssueContent: "x", StartLine: 1, EndLine: 1,
				Link: "https://your-gitea.example/src/a.go#L1", InlineURL: "https://your-gitea.example/pulls/12/files#issuecomment-9"},
			{RelevantFile: "b.go", IssueHeader: "Overview only", IssueContent: "y", StartLine: 2, EndLine: 2,
				Link: "https://your-gitea.example/src/b.go#L2"},
			{RelevantFile: "c.go", IssueHeader: "Unsafe inline URL", IssueContent: "z", StartLine: 3, EndLine: 3,
				Link: "https://your-gitea.example/src/c.go#L3", InlineURL: "javascript:alert(1)"},
		}},
	}
	for name, out := range map[string]string{
		"gitea":     Provider(res, capsGitea),
		"bitbucket": Provider(res, capsBB),
		"client":    Client(res),
	} {
		if !strings.Contains(out, "issuecomment-9") || strings.Contains(out, "src/a.go#L1") {
			t.Errorf("%s: the inline finding does not link its comment:\n%s", name, out)
		}
		if !strings.Contains(out, "src/b.go#L2") || !strings.Contains(out, "src/c.go#L3") || strings.Contains(out, "javascript:") {
			t.Errorf("%s: file links wrong:\n%s", name, out)
		}
	}
}
