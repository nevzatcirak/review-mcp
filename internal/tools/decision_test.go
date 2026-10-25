package tools

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/nevzatcirak/review-mcp/internal/config"
	"github.com/nevzatcirak/review-mcp/internal/provider"
	"github.com/nevzatcirak/review-mcp/internal/provider/bitbucketserver"
	"github.com/nevzatcirak/review-mcp/internal/provider/gitea"
	"github.com/nevzatcirak/review-mcp/internal/provider/github"
	"github.com/nevzatcirak/review-mcp/internal/provider/gitlab"
)

// pr_review_decision (v2.7 spec V-14 §2.3, §2.4).

const (
	decHead  = "a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1"
	decOther = "c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3"
	decURL   = "https://user:FAKE-pw@bitbucket.example.com/projects/PRJ/repos/demo/pull-requests/7?token=FAKE-q" //nolint:gosec // synthetic fake credentials used to test redaction
)

var decMe = provider.User{ID: "900", Name: "review-bot"}

// decisionFake is a Provider with a DecisionWriter that records every call
// in order. Any other method panics (the embedded Provider is nil), so a
// test also proves which calls a step makes.
type decisionFake struct {
	provider.Provider
	caps   provider.Capabilities
	pr     *provider.PullRequest
	me     provider.User
	meErr  error
	status *provider.ReviewStatus
	res    *provider.DecisionResult
	err    error

	calls []string
	got   provider.DecisionRequest
}

func (f *decisionFake) Capabilities() provider.Capabilities { return f.caps }

func (f *decisionFake) GetPullRequest(context.Context, provider.PRRef) (*provider.PullRequest, error) {
	f.calls = append(f.calls, "GetPullRequest")
	cp := *f.pr
	return &cp, nil
}

func (f *decisionFake) CurrentUser(context.Context) (provider.User, error) {
	f.calls = append(f.calls, "CurrentUser")
	return f.me, f.meErr
}

func (f *decisionFake) GetReviewStatus(_ context.Context, _ provider.PRRef, _ *provider.PullRequest, o provider.ReviewStatusOptions) *provider.ReviewStatus {
	f.calls = append(f.calls, "GetReviewStatus")
	if o.Me == nil || o.IsOwn == nil {
		panic("GetReviewStatus without Me and IsOwn")
	}
	return f.status
}

func (f *decisionFake) SetReviewDecision(_ context.Context, _ provider.PRRef, _ *provider.PullRequest, req provider.DecisionRequest) (*provider.DecisionResult, error) {
	f.calls = append(f.calls, "SetReviewDecision")
	f.got = req
	return f.res, f.err
}

// writes counts the write calls.
func (f *decisionFake) writes() int {
	n := 0
	for _, c := range f.calls {
		if c == "SetReviewDecision" || c == "PostComment" {
			n++
		}
	}
	return n
}

// readOnlyProvider implements no DecisionWriter.
type readOnlyProvider struct {
	provider.Provider
	caps provider.Capabilities
}

func (r *readOnlyProvider) Capabilities() provider.Capabilities { return r.caps }

type decisionResolver struct {
	p    provider.Provider
	kind provider.Kind
}

func (r decisionResolver) Resolve(u string) (provider.PRRef, provider.Provider, error) {
	return provider.PRRef{Kind: r.kind, Namespace: "PRJ", Repo: "demo", Number: 7, URL: u}, r.p, nil
}

// newDecisionFake is an open pull request by alice at decHead on a host
// with kind's real capabilities, where the token's user has no verdict.
func newDecisionFake(caps provider.Capabilities) *decisionFake {
	return &decisionFake{
		caps:   caps,
		pr:     &provider.PullRequest{State: "OPEN", Author: "alice", AuthorID: "101", HeadSHA: decHead},
		me:     decMe,
		status: &provider.ReviewStatus{Reviewers: []provider.Reviewer{{User: provider.User{ID: "101", Name: "alice"}, State: provider.ReviewApproved}}},
		res:    &provider.DecisionResult{State: "APPROVED"},
	}
}

var (
	bbsCaps    = bitbucketserver.NewFactory().Capabilities()
	githubCaps = github.NewFactory().Capabilities()
	giteaCaps  = gitea.NewFactory().Capabilities()
	gitlabCaps = gitlab.NewFactory().Capabilities()
)

func decisionCfg(allowed ...string) *config.Config {
	cfg := config.Defaults()
	cfg.Decision.Enabled = true
	if allowed != nil {
		cfg.Decision.Allowed = allowed
	}
	return cfg
}

func decide(t *testing.T, cfg *config.Config, p provider.Provider, kind provider.Kind, a PRReviewDecisionArgs) (PRReviewDecisionResult, error) {
	t.Helper()
	if a.PRURL == "" {
		a.PRURL = decURL
	}
	return PRReviewDecision(t.Context(), cfg, decisionResolver{p: p, kind: kind}, a)
}

// TestDecisionRefusalsWriteNothing: every refusal of steps 1 to 7 is the
// spec's fixed message, and no write call is made; a refusal before step 3
// does not even resolve a provider method, and each later step stops the
// calls where it refuses.
func TestDecisionRefusalsWriteNothing(t *testing.T) {
	approve := PRReviewDecisionArgs{Decision: "approve", HeadSHA: decHead}
	with := func(f func(*PRReviewDecisionArgs)) PRReviewDecisionArgs { a := approve; f(&a); return a }
	merged := func(f *decisionFake) { f.pr.State, f.pr.Merged = "MERGED", true }
	for _, c := range []struct {
		name  string
		cfg   *config.Config
		caps  provider.Capabilities
		kind  provider.Kind
		args  PRReviewDecisionArgs
		tweak func(*decisionFake)
		want  string
		calls []string
	}{
		// 1. The arguments.
		{name: "unknown_decision", args: with(func(a *PRReviewDecisionArgs) { a.Decision = "merge" }), want: DecisionInvalidMessage},
		{name: "empty_decision", args: with(func(a *PRReviewDecisionArgs) { a.Decision = "" }), want: DecisionInvalidMessage},
		{name: "short_head", args: with(func(a *PRReviewDecisionArgs) { a.HeadSHA = decHead[:39] }), want: DecisionHeadInvalidMessage},
		{name: "non_hex_head", args: with(func(a *PRReviewDecisionArgs) { a.HeadSHA = strings.Repeat("z", 40) }), want: DecisionHeadInvalidMessage},
		{name: "marker_body", args: with(func(a *PRReviewDecisionArgs) { a.Body = "ok\n[//]: # (review-mcp:overview)" }), want: CreateBodyMarkerMessage},
		{name: "long_body", args: with(func(a *PRReviewDecisionArgs) { a.Body = strings.Repeat("é", MaxCreateBodyChars+1) }), want: CreateBodyTooLongMessage},
		{name: "invalid_utf8_body", args: with(func(a *PRReviewDecisionArgs) { a.Body = "ok \xff" }), want: DecisionBodyUTF8Message},
		// 2. decision.allowed.
		{name: "not_allowed", cfg: decisionCfg("request_changes"), want: "decision.allowed does not include approve"},
		// 3. The provider's support.
		{name: "no_decision_writer", kind: provider.KindGitea, args: approve, want: "Gitea does not support approve through its API"},
		{name: "clear_on_github", caps: githubCaps, kind: provider.KindGitHub, args: with(func(a *PRReviewDecisionArgs) { a.Decision = "clear" }),
			want: "GitHub does not support clear through its API"},
		{name: "request_changes_on_gitlab", caps: gitlabCaps, kind: provider.KindGitLab,
			args: with(func(a *PRReviewDecisionArgs) { a.Decision, a.Body = "request_changes", "Fix it." }),
			want: "GitLab does not support request_changes through its API"},
		// 4. A required body.
		{name: "no_body_on_gitea", caps: giteaCaps, kind: provider.KindGitea, args: with(func(a *PRReviewDecisionArgs) { a.Decision = "request_changes" }),
			want: "body is required for request_changes on Gitea"},
		{name: "blank_body_on_github", caps: githubCaps, kind: provider.KindGitHub,
			args: with(func(a *PRReviewDecisionArgs) { a.Decision, a.Body = "request_changes", " \n\t " }),
			want: "body is required for request_changes on GitHub"},
		// 5. An open pull request.
		{name: "merged", tweak: merged, want: DecisionNotOpenMessage, calls: []string{"GetPullRequest"}},
		{name: "declined", tweak: func(f *decisionFake) { f.pr.State = "DECLINED" }, want: DecisionNotOpenMessage, calls: []string{"GetPullRequest"}},
		{name: "unknown_state", tweak: func(f *decisionFake) { f.pr.State = "locked" }, want: DecisionNotOpenMessage, calls: []string{"GetPullRequest"}},
		// 6. Not the user's own pull request.
		{name: "own_by_id", tweak: func(f *decisionFake) { f.pr.AuthorID, f.pr.Author = "900", "someone-else" }, want: DecisionOwnPRMessage,
			calls: []string{"GetPullRequest", "CurrentUser"}},
		{name: "own_by_name", tweak: func(f *decisionFake) { f.pr.AuthorID, f.pr.Author = "", "Review-Bot" }, want: DecisionOwnPRMessage,
			calls: []string{"GetPullRequest", "CurrentUser"}},
		{name: "token_user_unreadable", tweak: func(f *decisionFake) { f.meErr = &provider.Error{Class: provider.ClassAuth, Status: 403} },
			want: "authentication failed: check the token and its scopes (HTTP 403)", calls: []string{"GetPullRequest", "CurrentUser"}},
		// 7. The reviewed head.
		{name: "head_moved", tweak: func(f *decisionFake) { f.pr.HeadSHA = decOther }, want: "the pull request head is now c3c3c3c, not a1a1a1a; review the new commits before deciding",
			calls: []string{"GetPullRequest", "CurrentUser"}},
		{name: "head_moved_to_a_non_sha", tweak: func(f *decisionFake) { f.pr.HeadSHA = "refs/heads/x" },
			want: "the pull request head is now a newer commit, not a1a1a1a; review the new commits before deciding", calls: []string{"GetPullRequest", "CurrentUser"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			cfg, caps, kind := c.cfg, c.caps, c.kind
			if cfg == nil {
				cfg = decisionCfg()
			}
			if kind == "" {
				kind, caps = provider.KindBitbucketServer, bbsCaps
			}
			f := newDecisionFake(caps)
			var p provider.Provider = f
			if c.name == "no_decision_writer" {
				p = &readOnlyProvider{caps: giteaCaps}
			}
			if c.tweak != nil {
				c.tweak(f)
			}
			args := c.args
			if args.Decision == "" && args.HeadSHA == "" {
				args = approve
			}
			res, err := decide(t, cfg, p, kind, args)
			if err == nil || UserMessage(err) != c.want {
				t.Fatalf("PRReviewDecision = %+v, %v; want the refusal %q", res, err, c.want)
			}
			if f.writes() != 0 {
				t.Errorf("%d write calls for a refusal: %q", f.writes(), f.calls)
			}
			if !slices.Equal(f.calls, c.calls) {
				t.Errorf("calls %q, want %q", f.calls, c.calls)
			}
		})
	}
}

// TestDecisionUnchanged: the user's own entry already in the requested
// state at this head writes nothing and says unchanged; a stale entry, or
// another user's, is not the user's state at this head and writes.
func TestDecisionUnchanged(t *testing.T) {
	own := func(s provider.ReviewState, stale bool) []provider.Reviewer {
		return []provider.Reviewer{{User: provider.User{ID: "101", Name: "alice"}, State: provider.ReviewApproved},
			{User: provider.User{ID: "900", Name: "review-bot"}, State: s, Stale: stale}}
	}
	for _, c := range []struct {
		name      string
		decision  string
		reviewers []provider.Reviewer
		unchanged bool
	}{
		{"approve_already_approved", "approve", own(provider.ReviewApproved, false), true},
		{"approve_stale_approval", "approve", own(provider.ReviewApproved, true), false},
		{"approve_other_user_approved", "approve", own(provider.ReviewPending, false), false},
		{"request_changes_already", "request_changes", own(provider.ReviewChangesRequested, false), true},
		{"request_changes_stale", "request_changes", own(provider.ReviewChangesRequested, true), false},
		{"request_changes_over_approval", "request_changes", own(provider.ReviewApproved, false), false},
		{"clear_without_approval", "clear", own(provider.ReviewChangesRequested, false), true},
		{"clear_with_no_entry", "clear", own(provider.ReviewPending, false)[:1], true},
		{"clear_approval", "clear", own(provider.ReviewApproved, false), false},
		{"clear_stale_approval", "clear", own(provider.ReviewApproved, true), false},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newDecisionFake(bbsCaps)
			f.status.Reviewers = c.reviewers
			res, err := decide(t, decisionCfg(), f, provider.KindBitbucketServer, PRReviewDecisionArgs{Decision: c.decision, HeadSHA: decHead})
			if err != nil {
				t.Fatal(err)
			}
			if res.Unchanged != c.unchanged {
				t.Errorf("unchanged %v, want %v", res.Unchanged, c.unchanged)
			}
			if wantWrites := map[bool]int{true: 0, false: 1}[c.unchanged]; f.writes() != wantWrites {
				t.Errorf("%d writes, want %d (%q)", f.writes(), wantWrites, f.calls)
			}
			if c.unchanged && (res.State != "" || res.BodyPosted != "" || len(res.Notes) != 0) {
				t.Errorf("unchanged result %+v", res)
			}
		})
	}
}

// TestDecisionStatusUnreadable: without a review status the decision is
// sent, with the fixed note.
func TestDecisionStatusUnreadable(t *testing.T) {
	for _, st := range []*provider.ReviewStatus{nil, {Notes: []string{provider.NoteReviewsUnreadable}}} {
		f := newDecisionFake(bbsCaps)
		f.status = st
		res, err := decide(t, decisionCfg(), f, provider.KindBitbucketServer, PRReviewDecisionArgs{Decision: "approve", HeadSHA: decHead})
		if err != nil || f.writes() != 1 || !slices.Equal(res.Notes, []string{NoteDecisionStatusUnreadable}) {
			t.Errorf("result %+v, %v, writes %d", res, err, f.writes())
		}
	}
}

// TestDecisionWrite: the head is lowered, a blank body is no body, the
// result carries the provider's state and body_posted, and a body that
// was not posted gets the note.
func TestDecisionWrite(t *testing.T) {
	f := newDecisionFake(bbsCaps)
	f.res = &provider.DecisionResult{State: "NEEDS_WORK", BodyPosted: provider.BodyNotPosted}
	res, err := decide(t, decisionCfg(), f, provider.KindBitbucketServer,
		PRReviewDecisionArgs{Decision: "request_changes", HeadSHA: strings.ToUpper(decHead), Body: "Rename it."})
	if err != nil {
		t.Fatal(err)
	}
	want := PRReviewDecisionResult{
		PR:       DecisionPR{URL: "https://bitbucket.example.com/projects/PRJ/repos/demo/pull-requests/7?token=REDACTED", Provider: "bitbucket_server"},
		Decision: "request_changes", HeadSHA: decHead, State: "NEEDS_WORK", BodyPosted: provider.BodyNotPosted,
		Notes: []string{NoteDecisionBodyNotPosted},
	}
	if !equalDecision(res, want) {
		t.Errorf("result %+v, want %+v", res, want)
	}
	if f.got != (provider.DecisionRequest{Decision: provider.DecisionRequestChanges, HeadSHA: decHead, Body: "Rename it."}) {
		t.Errorf("request %+v", f.got)
	}
	if !slices.Equal(f.calls, []string{"GetPullRequest", "CurrentUser", "GetReviewStatus", "SetReviewDecision"}) {
		t.Errorf("calls %q", f.calls)
	}

	f = newDecisionFake(bbsCaps)
	if _, err := decide(t, decisionCfg(), f, provider.KindBitbucketServer, PRReviewDecisionArgs{Decision: "approve", HeadSHA: decHead, Body: "  \n "}); err != nil {
		t.Fatal(err)
	}
	if f.got.Body != "" {
		t.Errorf("a blank body reached the provider: %q", f.got.Body)
	}
}

func equalDecision(a, b PRReviewDecisionResult) bool {
	return a.PR == b.PR && a.Decision == b.Decision && a.HeadSHA == b.HeadSHA && a.State == b.State &&
		a.Unchanged == b.Unchanged && a.BodyPosted == b.BodyPosted && slices.Equal(a.Notes, b.Notes)
}

// TestDecisionQuickActionBody: on GitLab's capabilities (QuickActions) a
// body line that starts with /approve reaches the provider neutralised.
func TestDecisionQuickActionBody(t *testing.T) {
	f := newDecisionFake(gitlabCaps)
	f.res = &provider.DecisionResult{State: "approved", BodyPosted: provider.BodyPostedComment}
	if _, err := decide(t, decisionCfg(), f, provider.KindGitLab,
		PRReviewDecisionArgs{Decision: "approve", HeadSHA: decHead, Body: "/approve\nLooks good.\n/merge"}); err != nil {
		t.Fatal(err)
	}
	if want := " /approve\nLooks good.\n /merge"; f.got.Body != want {
		t.Errorf("body %q, want %q", f.got.Body, want)
	}
}

// TestDecisionConflict: a conflict of the decision request is the step 7
// refusal, with the head the re-read saw or "a newer commit".
func TestDecisionConflict(t *testing.T) {
	for _, c := range []struct {
		name string
		err  error
		want string
	}{
		{"moved_head", &provider.HeadMovedError{Head: decOther, Status: 400}, "the pull request head is now c3c3c3c, not a1a1a1a; review the new commits before deciding"},
		{"unknown_head", &provider.HeadMovedError{Status: 409}, "the pull request head is now a newer commit, not a1a1a1a; review the new commits before deciding"},
		{"plain_conflict", &provider.Error{Class: provider.ClassConflict, Status: 409}, "the pull request head is now a newer commit, not a1a1a1a; review the new commits before deciding"},
		{"other_error", &provider.Error{Class: provider.ClassProtocol, Status: 400}, "the server sent an unexpected response (HTTP 400)"},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newDecisionFake(bbsCaps)
			f.res, f.err = nil, c.err
			_, err := decide(t, decisionCfg(), f, provider.KindBitbucketServer, PRReviewDecisionArgs{Decision: "approve", HeadSHA: decHead})
			if got := UserMessage(err); got != c.want {
				t.Errorf("message %q, want %q", got, c.want)
			}
		})
	}
	if !errors.Is(&provider.HeadMovedError{}, provider.ErrConflict) {
		t.Fatal("HeadMovedError is not a conflict")
	}
}

// TestRenderPRReviewDecisionText: one line per outcome, then the notes.
func TestRenderPRReviewDecisionText(t *testing.T) {
	r := PRReviewDecisionResult{PR: DecisionPR{Provider: "bitbucket_server"}, HeadSHA: decHead}
	for _, c := range []struct {
		decision  string
		unchanged bool
		want      string
	}{
		{"approve", false, "Approved at a1a1a1a on Bitbucket Server.\n"},
		{"request_changes", false, "Changes requested at a1a1a1a on Bitbucket Server.\n"},
		{"clear", false, "Approval cleared at a1a1a1a on Bitbucket Server.\n"},
		{"approve", true, "Already approved at a1a1a1a; nothing was changed.\n"},
		{"request_changes", true, "Changes already requested at a1a1a1a; nothing was changed.\n"},
		{"clear", true, "No approval to clear at a1a1a1a; nothing was changed.\n"},
	} {
		r.Decision, r.Unchanged = c.decision, c.unchanged
		if got := RenderPRReviewDecisionText(r); got != c.want {
			t.Errorf("%s unchanged %v: %q, want %q", c.decision, c.unchanged, got, c.want)
		}
	}
	r.Decision, r.Unchanged, r.Notes = "approve", false, []string{NoteDecisionStatusUnreadable}
	if got := RenderPRReviewDecisionText(r); got != "Approved at a1a1a1a on Bitbucket Server.\n"+NoteDecisionStatusUnreadable+"\n" {
		t.Errorf("with a note: %q", got)
	}
}
