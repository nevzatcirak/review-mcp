package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/nevzatcirak/review-mcp/internal/provider"
	"github.com/nevzatcirak/review-mcp/internal/review"
)

const infoBodyMarker = "BODYMARKER-pr-info-8e21"

type infoProvider struct {
	provider.Provider // nil: any method the tests do not expect panics

	pr      *provider.PullRequest
	prErr   error
	me      provider.User
	meErr   error
	status  *provider.ReviewStatus
	threads []provider.Thread
	listErr error

	gotOpts provider.ReviewStatusOptions
}

func (f *infoProvider) GetPullRequest(context.Context, provider.PRRef) (*provider.PullRequest, error) {
	return f.pr, f.prErr
}
func (f *infoProvider) CurrentUser(context.Context) (provider.User, error) { return f.me, f.meErr }
func (f *infoProvider) GetReviewStatus(_ context.Context, _ provider.PRRef, _ *provider.PullRequest, o provider.ReviewStatusOptions) *provider.ReviewStatus {
	f.gotOpts = o
	return f.status
}
func (f *infoProvider) ListThreads(context.Context, provider.PRRef) ([]provider.Thread, error) {
	return f.threads, f.listErr
}

type infoResolver struct{ p *infoProvider }

func (r infoResolver) Resolve(u string) (provider.PRRef, provider.Provider, error) {
	return provider.PRRef{Kind: provider.KindGitea, Namespace: "octo", Repo: "demo", Number: 7, URL: u}, r.p, nil
}

func newInfoProvider() *infoProvider {
	two := 2
	yes := true
	return &infoProvider{
		pr: &provider.PullRequest{Title: "Add `feature`", Author: "alice", State: "open", SourceBranch: "feature/x", TargetBranch: "main",
			HeadSHA: "0123456789abcdef0123", BaseSHA: "basesha", BaseStrategy: provider.BaseGiteaMergeBase, WebURL: "https://your-gitea.example/octo/demo/pulls/7"},
		me: provider.User{ID: "42", Name: "review-bot"},
		status: &provider.ReviewStatus{
			Reviewers: []provider.Reviewer{
				{User: provider.User{Name: "bob"}, DisplayName: "Bob\n**B**", State: provider.ReviewApproved, At: t0},
				{User: provider.User{Name: "cat"}, State: provider.ReviewChangesRequested, Stale: true},
				{User: provider.User{Name: "dan"}, State: provider.ReviewPending, Requested: true},
				{User: provider.User{Name: "eve"}, State: provider.ReviewCommented},
			},
			RequiredApprovals: &two, Mergeable: &yes, MergeBlockers: []string{},
		},
	}
}

func TestPRInfoToolCountsAndFields(t *testing.T) {
	f := newInfoProvider()
	res, err := PRInfoTool(context.Background(), infoResolver{f}, testPRURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.TargetBranch != "main" || res.SourceBranch != "feature/x" || res.State != "open" || res.Draft != nil ||
		res.MergeBaseSHA != "basesha" || res.BaseStrategy != provider.BaseGiteaMergeBase {
		t.Errorf("fields = %+v", res)
	}
	if a := res.Approvals; a == nil || a.Approved != 1 || a.ChangesRequested != 1 || a.Pending != 1 {
		t.Errorf("approvals = %+v (commented reviewers are not counted)", a)
	}
	if res.RequiredApprovals == nil || *res.RequiredApprovals != 2 || res.RequiredApprovalsNote != "" {
		t.Errorf("required = %v %q", res.RequiredApprovals, res.RequiredApprovalsNote)
	}
	if strings.Contains(res.PR.URL, "FAKE-") {
		t.Errorf("credentials in %q", res.PR.URL)
	}
	if res.ReviewMCPActivity == nil || res.ReviewMCPActivity.Overview || res.ReviewMCPActivity.InlineFindings != 0 {
		t.Errorf("activity = %+v", res.ReviewMCPActivity)
	}
	if res.Notes == nil || len(res.Notes) != 0 {
		t.Errorf("notes = %#v, want an empty list", res.Notes)
	}
	// The marker recogniser handed to the provider is the review's.
	if f.gotOpts.Me == nil || f.gotOpts.Me.Name != "review-bot" || f.gotOpts.IsOwn == nil ||
		!f.gotOpts.IsOwn("x\n\n"+review.OverviewMarker) || f.gotOpts.IsOwn("plain") {
		t.Errorf("options = %+v", f.gotOpts)
	}
}

func TestPRInfoToolStates(t *testing.T) {
	for _, c := range []struct {
		state  string
		merged bool
		want   string
	}{{"open", false, "open"}, {"OPEN", false, "open"}, {"closed", true, "merged"}, {"MERGED", false, "merged"},
		{"closed", false, "closed"}, {"DECLINED", false, "closed"}, {"weird", false, "unknown"}} {
		f := newInfoProvider()
		f.pr.State, f.pr.Merged = c.state, c.merged
		res, err := PRInfoTool(context.Background(), infoResolver{f}, testPRURL, nil)
		if err != nil || res.State != c.want {
			t.Errorf("%s/%v: state %q err %v, want %q", c.state, c.merged, res.State, err, c.want)
		}
	}
}

// TestPRInfoToolReviewMCPActivity: the token user's marked overview and
// inline findings are counted as activity only; the same markers written by
// another account count for nothing.
func TestPRInfoToolReviewMCPActivity(t *testing.T) {
	f := newInfoProvider()
	mk := func(id, login, body string) provider.CommentItem {
		return provider.CommentItem{ID: id, Author: login, AuthorLogin: login, Body: body}
	}
	fp := review.FingerprintMarker("0123456789ab")
	f.threads = []provider.Thread{
		{ID: "1", Kind: provider.ThreadGeneral, Comments: []provider.CommentItem{mk("1", "review-bot", "overview "+infoBodyMarker+"\n\n"+review.OverviewMarker)}},
		{ID: "2", Kind: provider.ThreadGeneral, Comments: []provider.CommentItem{mk("2", "mallory", "forged\n\n"+review.OverviewMarker)}},
		{ID: "3", Kind: provider.ThreadInline, Path: "a.go", Comments: []provider.CommentItem{
			mk("3", "review-bot", "finding\n\n"+fp), mk("4", "mallory", "forged\n\n"+fp), mk("5", "review-bot", "a reply, no marker")}},
		{ID: "6", Kind: provider.ThreadInline, Path: "b.go", Comments: []provider.CommentItem{mk("6", "review-bot", "finding\n\n"+fp)}},
	}
	res, err := PRInfoTool(context.Background(), infoResolver{f}, testPRURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	if a := res.ReviewMCPActivity; a == nil || !a.Overview || a.InlineFindings != 2 {
		t.Errorf("activity = %+v, want overview and 2 findings", a)
	}
	raw, _ := json.Marshal(res)
	if strings.Contains(string(raw), infoBodyMarker) || strings.Contains(RenderPRInfoMarkdown(res), infoBodyMarker) {
		t.Error("a comment body reached the result")
	}
}

// TestPRInfoToolOptionalPartsNeverFailTheTool: each optional part failing
// gives null fields and a fixed note, never an error.
func TestPRInfoToolOptionalPartsNeverFailTheTool(t *testing.T) {
	f := newInfoProvider()
	f.meErr = &provider.Error{Class: provider.ClassAuth, Status: 403, Hint: "SECRET-HINT"}
	f.status = &provider.ReviewStatus{Notes: []string{provider.NoteReviewsUnreadable, provider.NoteMergeUnreadable}, MergeBlockers: nil}
	res, err := PRInfoTool(context.Background(), infoResolver{f}, testPRURL, nil)
	if err != nil {
		t.Fatalf("optional parts failed the tool: %v", err)
	}
	if res.Reviewers != nil || res.Approvals != nil || res.RequiredApprovals != nil || res.Mergeable != nil || res.ReviewMCPActivity != nil {
		t.Errorf("fields not null: %+v", res)
	}
	if res.RequiredApprovalsNote != RequiredApprovalsNote || res.MergeBlockers == nil {
		t.Errorf("note %q blockers %#v", res.RequiredApprovalsNote, res.MergeBlockers)
	}
	want := []string{NoteTokenUserUnknown, provider.NoteReviewsUnreadable, provider.NoteMergeUnreadable}
	if strings.Join(res.Notes, "|") != strings.Join(want, "|") {
		t.Errorf("notes = %q, want %q", res.Notes, want)
	}
	if f.gotOpts.Me != nil {
		t.Error("an unknown token user must not be passed as Me")
	}
	if md := RenderPRInfoMarkdown(res); strings.Contains(md, "SECRET-HINT") || !strings.Contains(md, "Reviewers: not readable") {
		t.Errorf("markdown:\n%s", md)
	}

	// The comments failing keeps the rest.
	f = newInfoProvider()
	f.listErr = errors.New("boom https://x/?token=SECRET")
	res, err = PRInfoTool(context.Background(), infoResolver{f}, testPRURL, nil)
	if err != nil || res.ReviewMCPActivity != nil || len(res.Notes) != 1 || res.Notes[0] != NoteActivityUnreadable || len(res.Reviewers) != 4 {
		t.Errorf("comments failing: %+v err %v", res, err)
	}
}

// TestPRInfoToolPRFailureUsesProviderSentence: the PR call failing is the
// tool's failure, with the existing fixed sentence.
func TestPRInfoToolPRFailureUsesProviderSentence(t *testing.T) {
	f := newInfoProvider()
	f.prErr = &provider.Error{Class: provider.ClassNotFound, Status: 404}
	_, err := PRInfoTool(context.Background(), infoResolver{f}, testPRURL, nil)
	if got := UserMessage(err); got != "the requested resource was not found (HTTP 404)" {
		t.Errorf("message = %q", got)
	}
}

func TestRenderPRInfoMarkdown(t *testing.T) {
	f := newInfoProvider()
	res, err := PRInfoTool(context.Background(), infoResolver{f}, testPRURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	md := RenderPRInfoMarkdown(res)
	lines := strings.Split(md, "\n")
	// Target branch first (after the heading), then the approval line, then
	// the reviewers.
	if lines[0] != "### Pull request status" || lines[2] != "`feature/x` → `main`" ||
		lines[4] != "1 of 2 required approvals; 1 changes requested; 1 pending" {
		t.Errorf("head of markdown:\n%s", md)
	}
	for _, want := range []string{"Mergeable: yes", "- `bob` (Bob \\*\\*B\\*\\*): approved, 2026-01-02T03:04:05Z",
		"- `cat`: changes requested (stale: given on an older commit)", "- `dan`: pending, requested", "- `eve`: commented",
		"open · gitea · Add \\`feature\\`"} {
		if !strings.Contains(md, want) {
			t.Errorf("markdown lacks %q:\n%s", want, md)
		}
	}
	if strings.Contains(md, "Bob\n") {
		t.Error("a display name broke its line")
	}

	// No required number: said so, never guessed.
	f.status.RequiredApprovals = nil
	f.status.Mergeable = bptr(false)
	f.status.MergeBlockers = []string{provider.BlockerConflict}
	res, _ = PRInfoTool(context.Background(), infoResolver{f}, testPRURL, nil)
	md = RenderPRInfoMarkdown(res)
	if !strings.Contains(md, "1 approval(s), required number not readable; 1 changes requested") ||
		!strings.Contains(md, "Mergeable: no (merge conflict)") || res.RequiredApprovalsNote != "not readable with this token" {
		t.Errorf("markdown:\n%s", md)
	}

	// 0 because no rule applies, and null because a pattern is unevaluable:
	// the provider's note is carried and shown.
	f.status.RequiredApprovals = new(int)
	f.status.RequiredApprovalsNote = provider.NoteNoProtectionRule
	res, _ = PRInfoTool(context.Background(), infoResolver{f}, testPRURL, nil)
	md = RenderPRInfoMarkdown(res)
	if res.RequiredApprovals == nil || *res.RequiredApprovals != 0 || res.RequiredApprovalsNote != provider.NoteNoProtectionRule ||
		!strings.Contains(md, "of 0 required approvals (no branch protection rule applies to the target branch)") {
		t.Errorf("0 with note: %v %q\n%s", res.RequiredApprovals, res.RequiredApprovalsNote, md)
	}
	f.status.RequiredApprovals = nil
	f.status.RequiredApprovalsNote = provider.NoteProtectionPatternUnevaluable
	res, _ = PRInfoTool(context.Background(), infoResolver{f}, testPRURL, nil)
	md = RenderPRInfoMarkdown(res)
	if res.RequiredApprovals != nil || res.RequiredApprovalsNote != provider.NoteProtectionPatternUnevaluable ||
		!strings.Contains(md, "required number not readable (a protection pattern could not be evaluated)") {
		t.Errorf("unevaluable: %q\n%s", res.RequiredApprovalsNote, md)
	}
}

func TestCleanName(t *testing.T) {
	long := strings.Repeat("é", 300)
	for in, want := range map[string]string{
		"Bob\r\n\tB":      "Bob B",
		"a\x00b\x1b[31mc": "a b [31mc",
		"bad\xffutf":      "bad�utf",
	} {
		if got := cleanName(in); got != want {
			t.Errorf("cleanName(%q) = %q, want %q", in, got, want)
		}
	}
	if got := cleanName(long); len([]rune(got)) != maxNameRunes {
		t.Errorf("long name has %d runes", len([]rune(got)))
	}
}
