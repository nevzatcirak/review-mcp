package ask

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/nevzatcirak/review-mcp/internal/gitctx"
)

// Paths the run never saw (v2.5 spec V-13b §6 item 1, WP-13f): a path in a
// code span of the answer that names no file the prompt showed the model is
// marked with one last line; nothing else in the answer changes.

// TestAskUnseenPathsQ1 [canary 1]: a consumer's file the related
// repository's hits showed (R2) is not marked, with or without its label;
// a made-up neighbour is, and so is a citation of a file never seen there,
// which also keeps its "(location not verified)". A file of the related
// pull request (R1) and of the diff count as seen. The check reads the
// model's own text, before the citations become links.
func TestAskUnseenPathsQ1(t *testing.T) {
	answer := "The client reads it at `R2:src/client/profile.ts:2` (`src/client/profile.ts`), not `src/client/profle.ts`; " +
		"see `R2:src/other.ts:9`, `R1:proto/user.proto` and `api/openapi.yaml`."
	_, res := consumerHarness(t, answer)
	want := []string{"src/client/profle.ts", "src/other.ts"}
	if !slices.Equal(res.AnswerUnverifiedPaths, want) {
		t.Errorf("answer_unverified_paths %q, want %q", res.AnswerUnverifiedPaths, want)
	}
	if !strings.HasSuffix(res.Answer, "`src/other.ts:9` (location not verified), `R1:proto/user.proto` and `api/openapi.yaml`.\n\n"+
		"Paths not seen in this review: `src/client/profle.ts`, `src/other.ts`") {
		t.Errorf("answer\n%s", res.Answer)
	}
	if !strings.Contains(res.Answer, "[`grp/web-ui src/client/profile.ts:2`](") {
		t.Errorf("the seen citation is not linked:\n%s", res.Answer)
	}
}

// TestAskUnseenPathsSources: a file of the repository-context hits and one
// a repository tool read at the head commit count as seen, as does a file
// of the diff; a path none of them names is marked.
func TestAskUnseenPathsSources(t *testing.T) {
	h, tr, _ := toolHarness(3, callTools(readCall("c1", "pkg/store.go")),
		answerWith("Compare `pkg/store.go`, `pkg/use.go:12`, `pkg/a.go` and `pkg/zzz.go`."))
	tr.hits = map[string][]gitctx.Hit{"Alpha": {{Symbol: "Alpha", Path: "pkg/use.go", Line: 12, StartLine: 11, Snippet: "x\nAlpha()\ny"}}}
	res := runTools(t, h)
	if res.Coverage.RepoContext.References != 1 || res.Coverage.RepoContext.Tools.Status != "used" {
		t.Fatalf("repository context %+v, tools %+v", res.Coverage.RepoContext, res.Coverage.RepoContext.Tools)
	}
	if !slices.Equal(res.AnswerUnverifiedPaths, []string{"pkg/zzz.go"}) {
		t.Errorf("answer_unverified_paths %q", res.AnswerUnverifiedPaths)
	}
	if want := "Compare `pkg/store.go`, `pkg/use.go:12`, `pkg/a.go` and `pkg/zzz.go`.\n\nPath not seen in this review: `pkg/zzz.go`"; res.Answer != want {
		t.Errorf("answer %q\nwant   %q", res.Answer, want)
	}
}

// TestAskUnseenPathsNone: an answer without such a span, or with one only
// in a fenced code block, is the model's answer byte for byte, and the
// result has no answer_unverified_paths.
func TestAskUnseenPathsNone(t *testing.T) {
	for _, answer := range []string{
		"It is bounded by `maxRetries` in src/app.go. " + answerMarker,
		"Like this:\n\n```go\n// `x/never.go`\n```",
	} {
		h := newHarness(answer)
		res, err := Run(context.Background(), h.deps, h.args())
		if err != nil {
			t.Fatal(err)
		}
		if res.Answer != answer || res.AnswerUnverifiedPaths != nil {
			t.Errorf("answer %q (paths %q), want %q", res.Answer, res.AnswerUnverifiedPaths, answer)
		}
		out, _ := json.Marshal(res)
		if strings.Contains(string(out), "answer_unverified_paths") {
			t.Errorf("the result carries answer_unverified_paths: %s", out)
		}
	}
}
