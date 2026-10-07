package mcpserver

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/nevzatcirak/review-mcp/internal/provider"
	"github.com/nevzatcirak/review-mcp/internal/tools"
)

const infoBodyMarker = "BODYMARKER-info-2c7e"

// infoFake serves canned pr_info data. The embedded nil interface makes any
// other method panic, so a call that writes or posts cannot go unnoticed.
type infoFake struct {
	provider.Provider
	status  *provider.ReviewStatus
	calls   atomic.Int64
	threads []provider.Thread
}

func (f *infoFake) GetPullRequest(context.Context, provider.PRRef) (*provider.PullRequest, error) {
	f.calls.Add(1)
	return &provider.PullRequest{Title: "Add feature", Author: "alice", State: "open", SourceBranch: "feature/x",
		TargetBranch: "main", HeadSHA: "headsha", BaseSHA: "basesha", BaseStrategy: provider.BaseGiteaMergeBase,
		WebURL: "https://your-gitea.example/octo/demo/pulls/7"}, nil
}
func (f *infoFake) CurrentUser(context.Context) (provider.User, error) {
	return provider.User{ID: "42", Name: "review-bot"}, nil
}
func (f *infoFake) GetReviewStatus(context.Context, provider.PRRef, *provider.PullRequest, provider.ReviewStatusOptions) *provider.ReviewStatus {
	return f.status
}
func (f *infoFake) ListThreads(context.Context, provider.PRRef) ([]provider.Thread, error) {
	return f.threads, nil
}

func fullStatus() *provider.ReviewStatus {
	two, yes := 2, true
	return &provider.ReviewStatus{
		Reviewers: []provider.Reviewer{
			{User: provider.User{Name: "bob"}, DisplayName: "Bob", State: provider.ReviewApproved, At: ts(1)},
			{User: provider.User{Name: "cat"}, State: provider.ReviewPending, Requested: true},
		},
		RequiredApprovals: &two, Mergeable: &yes, MergeBlockers: []string{},
	}
}

// TestPRInfoToolDefinition: pr_info is listed in stdio and in serve mode,
// read-only, with an input and an output schema.
func TestPRInfoToolDefinition(t *testing.T) {
	for name, deps := range map[string]Deps{"stdio": depsFor(validEnv(), nil), "serve": serveDeps(t, nil)} {
		t.Run(name, func(t *testing.T) {
			list, err := connect(t, deps).ListTools(context.Background(), nil)
			if err != nil {
				t.Fatal(err)
			}
			var tl *mcp.Tool
			for _, x := range list.Tools {
				if x.Name == "pr_info" {
					tl = x
				}
			}
			if tl == nil {
				t.Fatal("pr_info is not listed")
			}
			a := tl.Annotations
			if a == nil || !a.ReadOnlyHint || !a.IdempotentHint || a.DestructiveHint == nil || *a.DestructiveHint ||
				a.OpenWorldHint == nil || !*a.OpenWorldHint {
				t.Errorf("annotations = %+v", a)
			}
			if !strings.Contains(tl.Description, "never count as approvals") || !strings.Contains(tl.Description, "Read-only") {
				t.Errorf("description = %q", tl.Description)
			}
			in, _ := json.Marshal(tl.InputSchema)
			var inS struct {
				Required   []string                  `json:"required"`
				Properties map[string]map[string]any `json:"properties"`
			}
			if err := json.Unmarshal(in, &inS); err != nil {
				t.Fatal(err)
			}
			if strings.Join(inS.Required, ",") != "pr_url" || len(inS.Properties) != 1 || inS.Properties["pr_url"]["description"] == "" {
				t.Errorf("input schema = %s", in)
			}
			if tl.OutputSchema == nil {
				t.Fatal("pr_info has no output schema")
			}
			out, _ := json.Marshal(tl.OutputSchema)
			var outS struct {
				Required   []string                  `json:"required"`
				Properties map[string]map[string]any `json:"properties"`
			}
			if err := json.Unmarshal(out, &outS); err != nil {
				t.Fatal(err)
			}
			want := []string{"approvals", "author", "base_strategy", "head_sha", "merge_base_sha", "merge_blockers", "mergeable", "notes", "pr",
				"required_approvals", "review_mcp_activity", "reviewers", "source_branch", "state", "target_branch", "title", "web_url"}
			sort.Strings(outS.Required)
			if strings.Join(outS.Required, ",") != strings.Join(want, ",") {
				t.Errorf("required = %v, want %v", outS.Required, want)
			}
			for _, p := range append(want, "draft", "required_approvals_note") {
				if outS.Properties[p]["description"] == "" {
					t.Errorf("output property %s has no description", p)
				}
			}
		})
	}
}

// TestCallPRInfo: a call returns the markdown and the structured result, and
// the structured result validates against the schema both with every part
// readable and with every optional part null.
func TestCallPRInfo(t *testing.T) {
	f := &infoFake{status: fullStatus(), threads: []provider.Thread{{ID: "1", Kind: provider.ThreadGeneral,
		Comments: []provider.CommentItem{{ID: "1", Author: "review-bot", AuthorLogin: "review-bot", Body: infoBodyMarker}}}}}
	deps, built := withFake(depsFor(validEnv(), nil), f)
	cs := connect(t, deps)

	res := callTool(t, cs, "pr_info", map[string]any{"pr_url": prURL + "?token=" + fakeGitea})
	if res.IsError {
		t.Fatalf("tool error: %+v", res.Content)
	}
	var got tools.PRInfoResult
	decodeStructured(t, res, &got)
	if got.TargetBranch != "main" || got.Approvals == nil || got.Approvals.Approved != 1 || got.Approvals.Pending != 1 ||
		got.RequiredApprovals == nil || *got.RequiredApprovals != 2 || got.PR.Kind != "gitea" || strings.Contains(got.PR.URL, fakeGitea) {
		t.Errorf("structured = %+v", got)
	}
	text := textOf(t, res)
	if text != tools.RenderPRInfoMarkdown(got) || !strings.Contains(text, "`feature/x` → `main`") {
		t.Errorf("text and structured content disagree:\n%s", text)
	}
	if strings.Contains(text, infoBodyMarker) {
		t.Error("a comment body reached the text")
	}
	if raw, _ := json.Marshal(res.StructuredContent); strings.Contains(string(raw), infoBodyMarker) {
		t.Error("a comment body reached the structured content")
	}
	if built.Load() != 1 {
		t.Errorf("resolver built %d times, want 1", built.Load())
	}

	// Every optional part unreadable: nulls must still satisfy the schema
	// (the client validates the structured content of this call).
	f.status = &provider.ReviewStatus{Notes: []string{provider.NoteReviewsUnreadable, provider.NoteMergeUnreadable}}
	res = callTool(t, cs, "pr_info", map[string]any{"pr_url": prURL})
	if res.IsError {
		t.Fatalf("tool error with null parts: %+v", res.Content)
	}
	raw, _ := json.Marshal(res.StructuredContent)
	var m map[string]json.RawMessage
	_ = json.Unmarshal(raw, &m)
	for _, k := range []string{"reviewers", "approvals", "required_approvals", "mergeable"} {
		if string(m[k]) != "null" {
			t.Errorf("%s = %s, want null", k, m[k])
		}
	}
	if string(m["merge_blockers"]) != "[]" || !strings.Contains(string(m["required_approvals_note"]), "not readable with this token") {
		t.Errorf("merge_blockers %s note %s", m["merge_blockers"], m["required_approvals_note"])
	}
}

// TestPRInfoDegradedStartMakesNoResolverCall: an invalid configuration gives
// the fixed tool error and touches nothing.
func TestPRInfoDegradedStartMakesNoResolverCall(t *testing.T) {
	f := &infoFake{status: fullStatus()}
	deps, built := withFake(depsFor(invalidEnv(), nil), f)
	res := callTool(t, connect(t, deps), "pr_info", map[string]any{"pr_url": prURL})
	if !res.IsError || textOf(t, res) != tools.ConfigInvalidMessage || built.Load() != 0 || f.calls.Load() != 0 {
		t.Errorf("IsError=%v text=%q built=%d calls=%d", res.IsError, textOf(t, res), built.Load(), f.calls.Load())
	}
}
