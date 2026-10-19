package review

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/nevzatcirak/review-mcp/internal/gitctx"
	"github.com/nevzatcirak/review-mcp/internal/llm"
	"github.com/nevzatcirak/review-mcp/internal/provider"
	"github.com/nevzatcirak/review-mcp/internal/repotools"
)

// Repository tools in related repositories (v2.5 spec V-13b §3.1): the
// tools of a review may also read the related repositories of the placed
// block, by label, at their pinned commits.

// consumerTools is the consumer backend with the reads of the repository
// tools: the primary's head commit (toolRepo's files) and the consumer's
// checkout at consumerCommit (files), told apart by the checkout's commit.
type consumerTools struct {
	*consumerRepo
	head     map[string]string
	files    map[string]string
	reads    []string // "<head|consumer>:<path>"
	searches []gitctx.TextQuery
}

func (c *consumerTools) FileAt(_ context.Context, co gitctx.Checkout, rev, path string, _ int) ([]byte, error) {
	files, side := c.head, "head"
	if co.HeadSHA == consumerCommit {
		files, side = c.files, "consumer"
	}
	c.reads = append(c.reads, side+":"+path)
	if rev != co.HeadSHA {
		return nil, &provider.Error{Class: provider.ClassNotFound}
	}
	s, ok := files[path]
	if !ok {
		return nil, &provider.Error{Class: provider.ClassNotFound}
	}
	return []byte(s), nil
}

func (c *consumerTools) DirEntries(context.Context, gitctx.Checkout, string, string) ([]provider.DirEntry, error) {
	return []provider.DirEntry{{Name: "client", Dir: true}}, nil
}

func (c *consumerTools) SearchText(_ context.Context, _ gitctx.Checkout, q gitctx.TextQuery) (gitctx.TextResult, error) {
	c.searches = append(c.searches, q)
	return gitctx.TextResult{}, nil
}

// consumerToolsHarness is consumerHarness with the tools on (rounds), a
// primary diff that defines symbols and renames userName, a consumer with a
// hit in src/client/profile.ts and a second file, and the model's script.
func consumerToolsHarness(rounds int, steps ...func(llm.Conversation) (llm.Turn, error)) (*harness, *consumerTools, *toolLLM) {
	h, b := consumerHarness(true)
	h.prov.files = append(append(h.prov.files, defFiles()...), contractFile())
	b.hits = map[string][]gitctx.Hit{"web-ui@feature/x": {consumerHit}}
	ct := &consumerTools{consumerRepo: b, head: map[string]string{"pkg/store.go": "package pkg\n"},
		files: map[string]string{"src/client/settings.ts": "export const label = user.userName\nCONSUMER-SETTINGS-MARKER\n"}}
	h.deps.RepoContext = ct
	h.deps.Config.Context.Repo.MaxToolRounds = rounds
	h.deps.Config.Review.MaxFindings = 5
	tl := &toolLLM{fakeLLM: h.llm, steps: steps}
	h.deps.LLM = tl
	return h, ct, tl
}

func repoCall(id, name, args string) llm.ToolCall {
	return llm.ToolCall{ID: id, Name: name, Arguments: args, Args: json.RawMessage(args)}
}

// settingsAnswer has a finding in R2 on the file the tool read there (never
// a hit file), and one in R2 on a file the run never saw.
const settingsAnswer = "```yaml\nreview:\n  key_issues_to_review:\n" +
	"    - relevant_file: src/client/settings.ts\n      issue_header: Settings read the old name\n" +
	"      issue_content: It reads userName, which the pull request renames.\n      start_line: 1\n      end_line: 1\n      related: R2\n" +
	"    - relevant_file: src/client/never-seen.ts\n      issue_header: Maybe here too\n" +
	"      issue_content: Needs verification.\n      start_line: 7\n      end_line: 7\n      related: R2\n```\n"

// TestToolsReadRelatedRepositoryInReview: in a review whose related block
// holds a pull request (R1) and a repository (R2), the tools take the
// argument repo with R2 only and the instruction names it; repo "R2" reads
// the consumer's checkout at its pinned commit; repo "R1" (the related pull
// request) is invalid_arguments, a path outside is not_allowed, and neither
// reads anything; the calls are accounted with their labels; a finding in
// the file the tool read there is linked at the commit (not "location not
// verified"), one in a file the run never saw is not; the result validates.
func TestToolsReadRelatedRepositoryInReview(t *testing.T) {
	h, ct, tl := consumerToolsHarness(3,
		callTools(
			repoCall("c1", repotools.ReadFile, `{"path":"src/client/settings.ts","repo":"R2"}`),
			repoCall("c2", repotools.ReadFile, `{"path":"src/client/settings.ts","repo":"R1"}`),
			repoCall("c3", repotools.ReadFile, `{"path":"../demo/pkg/store.go","repo":"R2"}`),
			readCall("c4", "pkg/store.go")),
		toolAnswer(settingsAnswer))
	res := runRelated(t, h, Args{Related: []string{relatedURL, consumerBranchURL}, Publish: true})
	if len(tl.convs) != 2 {
		t.Fatalf("%d requests", len(tl.convs))
	}
	conv := tl.convs[0]
	if !strings.Contains(conv.System, "instead of the head commit: R2 for grp/web-ui.") {
		t.Errorf("the instruction does not name the repository: %q", conv.System)
	}
	if got, want := mustJSON(t, conv.Tools), mustJSON(t, repotools.DefinitionsFor([]repotools.Repo{{Label: "R2"}})); string(got) != string(want) {
		t.Errorf("tools %s\nwant  %s", got, want)
	}
	if !slices.Equal(ct.reads, []string{"consumer:src/client/settings.ts", "head:pkg/store.go"}) {
		t.Errorf("reads %q", ct.reads)
	}
	if !strings.Contains(tl.convs[1].Messages[2].Content, "CONSUMER-SETTINGS-MARKER") ||
		!strings.HasPrefix(tl.convs[1].Messages[2].Content, repotools.RepoHeader("R2")) {
		t.Errorf("the consumer's file did not reach the model: %q", tl.convs[1].Messages[2].Content)
	}
	u := toolsOf(res)
	want := []struct{ status, repo string }{{"ok", "R2"}, {"invalid_arguments", "R1"}, {"not_allowed", "R2"}, {"ok", ""}}
	if u.Status != repotools.StatusUsed || len(u.Calls) != len(want) {
		t.Fatalf("tool use %+v", u)
	}
	for i, w := range want {
		if c := u.Calls[i]; c.Status != w.status || c.Repo != w.repo {
			t.Errorf("call %d %+v, want %s %q", i+1, c, w.status, w.repo)
		}
	}
	f := res.Review.KeyIssuesToReview
	if len(f) != 2 {
		t.Fatalf("%d findings", len(f))
	}
	if read := f[0]; read.Link != "https://gitlab.example/grp/svc/-/blob/"+consumerCommit+"/src/client/settings.ts#L1" ||
		read.LocationUnverified || !read.RelatedRepository || read.InlineStatus != "" {
		t.Errorf("the finding in the file the tool read %+v", read)
	}
	if unseen := f[1]; unseen.Link != "" || !unseen.LocationUnverified {
		t.Errorf("the finding in a file never seen %+v", unseen)
	}
	for _, leak := range []string{"settings.ts", consumerCommit[:12], "feature/x"} {
		if strings.Contains(h.logs.String(), leak) {
			t.Errorf("the logs carry %q", leak)
		}
	}
}

// TestToolSchemasWithoutRepositoryItems: a review whose related block holds
// pull requests only, or that has no related argument, sends the tools of
// 2.4 byte for byte and the instruction of 2.4; its calls carry no repo.
func TestToolSchemasWithoutRepositoryItems(t *testing.T) {
	for name, args := range map[string]Args{"no related": {}, "pull requests only": {Related: []string{relatedURL}}} {
		t.Run(name, func(t *testing.T) {
			h, _, tl := consumerToolsHarness(3, callTools(readCall("c1", "pkg/store.go")), toolAnswer(goodAnswer))
			res := runRelated(t, h, args)
			if len(tl.convs) == 0 {
				t.Fatal("no conversation")
			}
			for i, c := range tl.convs {
				if got, want := mustJSON(t, c.Tools), mustJSON(t, repotools.Definitions()); string(got) != string(want) {
					t.Errorf("request %d tools %s", i+1, got)
				}
				if !strings.Contains(c.System, ToolsInstruction(3)) || strings.Contains(c.System, "argument repo") {
					t.Errorf("request %d: the instruction is not that of 2.4", i+1)
				}
			}
			raw := mustJSON(t, toolsOf(res))
			if strings.Contains(string(raw), `"repo"`) {
				t.Errorf("tools %s", raw)
			}
		})
	}
}
