package review

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/nevzatcirak/review-mcp/internal/gitctx"
)

// Paths the review never saw (v2.5 spec V-13b §6 item 1, WP-13f): a path in
// a code span of a finding's title or body that names no file the prompts
// showed the model is marked; nothing else in the text changes.

// spanFinding is one finding of spanAnswer.
type spanFinding struct{ file, header, content string }

// spanAnswer is a review answer with the findings fs, each at lines 10 to
// 12, header and content as block scalars so that they may hold code spans.
func spanAnswer(fs ...spanFinding) string {
	var b strings.Builder
	b.WriteString("```yaml\nreview:\n  key_issues_to_review:\n")
	for _, f := range fs {
		fmt.Fprintf(&b, "    - relevant_file: %s\n      issue_header: |\n        %s\n      issue_content: |\n        %s\n"+
			"      start_line: 10\n      end_line: 12\n", f.file, f.header, f.content)
	}
	b.WriteString("```\n")
	return b.String()
}

// checkUnseen checks the findings of res against want (the unverified paths
// of each, nil for none): a marked finding has them as UnverifiedPaths and
// its content ends with the mark line after a blank line; an unmarked one
// has neither, and its content is the answer's.
func checkUnseen(t *testing.T, res *Result, contents []string, want [][]string) {
	t.Helper()
	f := res.Review.KeyIssuesToReview
	if len(f) != len(want) {
		t.Fatalf("%d findings, want %d", len(f), len(want))
	}
	for i, ki := range f {
		if !slices.Equal(ki.UnverifiedPaths, want[i]) {
			t.Errorf("finding %d: unverified paths %q, want %q", i+1, ki.UnverifiedPaths, want[i])
		}
		wantContent := contents[i]
		if len(want[i]) > 0 {
			spans := make([]string, len(want[i]))
			for k, p := range want[i] {
				spans[k] = "`" + p + "`"
			}
			head := "Paths not seen in this review: "
			if len(want[i]) == 1 {
				head = "Path not seen in this review: "
			}
			wantContent += "\n\n" + head + strings.Join(spans, ", ")
		}
		if ki.IssueContent != wantContent {
			t.Errorf("finding %d: content\n%q\nwant\n%q", i+1, ki.IssueContent, wantContent)
		}
	}
}

// TestUnseenPathsQ1 [canary 1]: the acceptance case Q1 of 2.5.0-rc.1. The
// consumer's file is web/src/ui/header.ts, a hit of the related repository;
// the model wrote web/src/api/header.ts. The made-up path is marked, the
// right one is not, with or without the label and a line suffix; the result
// validates against the declared schema.
func TestUnseenPathsQ1(t *testing.T) {
	const right, wrong = "contract-v25/web/src/ui/header.ts", "contract-v25/web/src/api/header.ts"
	contents := []string{
		"The consumer reads userName in `" + wrong + ":12`; the client is `" + right + ":12`.",
		"The header component (`R1:" + right + ":12`) breaks.",
	}
	h, b := consumerHarness(true, spanAnswer(
		spanFinding{"api/openapi.yaml", "Renamed field", contents[0]},
		spanFinding{"api/openapi.yaml", "Consumer `" + right + "`", contents[1]}))
	h.prov.files = append(h.prov.files, contractFile())
	b.hits = map[string][]gitctx.Hit{"web-ui@feature/x": {{Symbol: "userName", Path: right, Line: 12, StartLine: 11,
		Snippet: "const h = props.user\nreturn h.userName\n}"}}}
	h.deps.Config.Review.MaxFindings = 5
	res := runRelated(t, h, Args{Related: []string{consumerBranchURL}})
	if e := res.Coverage.Related[0]; e.Status != "full" {
		t.Fatalf("the repository item %+v", e)
	}
	checkUnseen(t, res, contents, [][]string{{wrong}, nil})
	if !strings.HasSuffix(res.Review.KeyIssuesToReview[0].IssueContent, "\n\nPath not seen in this review: `"+wrong+"`") {
		t.Errorf("the mark line: %q", res.Review.KeyIssuesToReview[0].IssueContent)
	}
}

// TestUnseenPathsEachSource: each source of "seen" counts on its own: a
// file of the primary diff, of the repository-context hits, the guidelines
// file and a file of the file tree; a file shown or listed in a related
// pull request's section (and a label selects that item only); a file a
// repository tool read. A path none of them names is marked in the same run.
func TestUnseenPathsEachSource(t *testing.T) {
	t.Run("the primary diff alone", func(t *testing.T) {
		contents := []string{"Called from `src/util.go:2`.", "Unlike `src/errors.go`."}
		h := newHarness(spanAnswer(spanFinding{"src/app.go", "Call", contents[0]}, spanFinding{"src/app.go", "Errors", contents[1]}))
		h.deps.Config.Review.GuidelinesPaths, h.deps.Config.Review.FileTree = nil, false
		res := runRelated(t, h, Args{})
		checkUnseen(t, res, contents, [][]string{nil, {"src/errors.go"}})
	})
	t.Run("diff, repository context, guidelines and file tree", func(t *testing.T) {
		contents := []string{
			"Compare `pkg/b.go`, the caller `cmd/main.go:7`, the rule in `.review-mcp/guidelines.md` and `pkg/c.go`.",
			"See `pkg/d.go`.",
		}
		h, _ := repoHarness(defFiles(), map[string][]gitctx.Hit{"Alpha": {useHit("Alpha", "cmd/main.go", 7)}})
		h.llm.answers = []string{spanAnswer(spanFinding{"pkg/a.go", "Alpha", contents[0]},
			spanFinding{"pkg/a.go", "In `pkg/a.go`", contents[1]})}
		h.prov.revs["base0"] = map[string]string{guidelinesPath: baseRule, "pkg/a.go": "x", "pkg/b.go": "x", "pkg/c.go": "x"}
		res := runRelated(t, h, Args{})
		checkUnseen(t, res, contents, [][]string{nil, {"pkg/d.go"}})
	})
	t.Run("a related pull request", func(t *testing.T) {
		contents := []string{
			"It changes `src/second.go` and `R1:proto/user.proto:3` together with `src/app.go`.",
			"The label selects R1 only: `R1:src/app.go`.",
		}
		h, _ := relatedHarness(spanAnswer(spanFinding{"src/app.go", "Mismatch", contents[0]},
			spanFinding{"src/app.go", "Label", contents[1]}))
		res := runRelated(t, h, Args{Related: []string{relatedURL}})
		checkUnseen(t, res, contents, [][]string{nil, {"src/app.go"}})
	})
	t.Run("a repository tool", func(t *testing.T) {
		contents := []string{"Find in `pkg/store.go` returns nil.", "And `pkg/other.go`."}
		h, _, _ := toolHarness(3, callTools(readCall("c1", "pkg/store.go")),
			toolAnswer(spanAnswer(spanFinding{"pkg/a.go", "Nil", contents[0]}, spanFinding{"pkg/a.go", "Other", contents[1]})))
		res := runTools(t, h, Args{})
		validateResult(t, res)
		checkUnseen(t, res, contents, [][]string{nil, {"pkg/other.go"}})
	})
}

// TestUnseenPathsMany: the grammar of the mark for many paths: at most five
// named, then "and N more"; every one is in unverified_paths.
func TestUnseenPathsMany(t *testing.T) {
	content := "`x/1.go` `x/2.go` `x/3.go` `x/4.go` `x/5.go` `x/6.go` `x/2.go:9`"
	h := newHarness(spanAnswer(spanFinding{"src/app.go", "Many", content}))
	res := runRelated(t, h, Args{})
	ki := res.Review.KeyIssuesToReview[0]
	if want := []string{"x/1.go", "x/2.go", "x/3.go", "x/4.go", "x/5.go", "x/6.go"}; !slices.Equal(ki.UnverifiedPaths, want) {
		t.Errorf("unverified paths %q", ki.UnverifiedPaths)
	}
	if want := content + "\n\nPaths not seen in this review: `x/1.go`, `x/2.go`, `x/3.go`, `x/4.go`, `x/5.go` and 1 more"; ki.IssueContent != want {
		t.Errorf("content\n%q\nwant\n%q", ki.IssueContent, want)
	}
}

// TestUnseenPathsNone: findings without such a span, or with one only in a
// fenced code block, are left as the model wrote them, and the result has
// no unverified_paths.
func TestUnseenPathsNone(t *testing.T) {
	h := newHarness(goodAnswer)
	base := runRelated(t, h, Args{})
	out, err := json.Marshal(base)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "unverified_paths") {
		t.Errorf("a result without spans carries unverified_paths: %s", out)
	}
	for _, ki := range base.Review.KeyIssuesToReview {
		if strings.Contains(ki.IssueContent, "not seen in this review") {
			t.Errorf("finding marked: %q", ki.IssueContent)
		}
	}
	fenced := "Use this:\n        ```go\n        // see `x/never.go`\n        ```"
	h = newHarness(spanAnswer(spanFinding{"src/app.go", "Fenced", fenced}))
	res := runRelated(t, h, Args{})
	if ki := res.Review.KeyIssuesToReview[0]; ki.UnverifiedPaths != nil || strings.Contains(ki.IssueContent, "not seen") {
		t.Errorf("a path in a fenced block was checked: %+v", ki)
	}
}
