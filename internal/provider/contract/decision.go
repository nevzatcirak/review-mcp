package contract

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/nevzatcirak/review-mcp/internal/provider"
)

// WriteRequest is one write request as a host documents it: the method,
// the escaped path the fake server sees (from its root, any context path
// included) and the JSON body; a nil Body is a request without a body.
type WriteRequest struct {
	Method, Path string
	Body         any
}

// DecisionExpecter is implemented by the Fixture of a provider that
// implements provider.DecisionWriter. It names the one request the host's
// API documents for decision d on spec's pull request at spec.HeadSHA, by
// spec.TokenUser, with body (the provider's DecisionRequest.Body), and the
// state the provider reports for it (provider.DecisionResult.State).
type DecisionExpecter interface {
	DecisionRequest(spec Spec, d provider.Decision, body string) WriteRequest
	DecisionState(d provider.Decision) string
}

// decided is the outcome of one decision of the decision case: what
// SetReviewDecision returned, the writes it caused, and the provider and
// pull request it was sent to.
type decided struct {
	res    *provider.DecisionResult
	err    error
	writes []request
	p      provider.Provider
	ref    provider.PRRef
}

// decisionBody is the body of the decisions that carry one.
const decisionBody = "Please rename the helper before this is merged."

// movedHead is a commit pushed just before a decision arrived.
const movedHead = "f6f6f6f6f6f6f6f6f6f6f6f6f6f6f6f6f6f6f6f6"

var allDecisions = []provider.Decision{provider.DecisionApprove, provider.DecisionRequestChanges, provider.DecisionClear}

// decision: a provider reports Capabilities().Decisions allowing something
// exactly when it implements provider.DecisionWriter, and GetPullRequest
// fills AuthorID. Such a provider sends, for every decision it allows,
// exactly the request its host documents (DecisionExpecter) and no other
// write; refuses a decision it does not allow, a malformed head and a
// missing body where one is required without any request; reports a
// refusal whose re-read shows a moved head as a conflict and one whose
// head did not move as the status error; puts the body in the review
// where the host has a review body, and otherwise posts it after the
// decision, and only after one that succeeded; and reports a failed
// comment as not_posted, not as an error.
func (s *suite) decision(t *testing.T) {
	spec := samplePR()
	p, ref, _ := s.serve(t, spec)
	caps := p.Capabilities().Decisions
	_, isWriter := p.(provider.DecisionWriter)
	if caps.Any() != isWriter {
		t.Fatalf("Capabilities().Decisions %+v allows a decision: %v, but the provider implements provider.DecisionWriter: %v",
			caps, caps.Any(), isWriter)
	}
	for _, d := range allDecisions {
		want := map[provider.Decision]bool{provider.DecisionApprove: caps.Approve,
			provider.DecisionRequestChanges: caps.RequestChanges, provider.DecisionClear: caps.Clear}[d]
		if caps.Allows(d) != want {
			t.Errorf("Allows(%q) = %v, want %v", d, caps.Allows(d), want)
		}
	}
	if caps.Allows("merge") || caps.Allows("") {
		t.Error("Allows accepts an unknown decision")
	}
	t.Run("author_id", func(t *testing.T) {
		pr := mustPR(t, p, ref)
		if want := strconv.FormatInt(spec.Author.ID, 10); pr.AuthorID != want {
			t.Errorf("AuthorID = %q, want %q", pr.AuthorID, want)
		}
	})
	if !isWriter {
		return
	}
	exp, ok := s.f.(DecisionExpecter)
	if !ok {
		t.Fatal("the provider implements provider.DecisionWriter, but the fixture does not implement contract.DecisionExpecter")
	}
	first := provider.Decision("")
	for _, d := range allDecisions {
		if caps.Allows(d) {
			first = d
			break
		}
	}
	// decide serves spec, sends one decision on its head and returns the
	// outcome with the writes the decision caused.
	decide := func(t *testing.T, spec Spec, d provider.Decision, body string) decided {
		t.Helper()
		p, ref, log := s.serve(t, spec)
		pr := mustPR(t, p, ref)
		n, _ := log.count()
		res, err := p.(provider.DecisionWriter).SetReviewDecision(t.Context(), ref, pr,
			provider.DecisionRequest{Decision: d, HeadSHA: spec.HeadSHA, Body: body})
		return decided{res: res, err: err, writes: log.writesFrom(n), p: p, ref: ref}
	}

	t.Run("requests", func(t *testing.T) {
		for _, d := range allDecisions {
			if !caps.Allows(d) {
				continue
			}
			t.Run(string(d), func(t *testing.T) {
				body := ""
				if d == provider.DecisionRequestChanges && caps.RequestChangesNeedsBody {
					body = decisionBody
				}
				o := decide(t, samplePR(), d, body)
				res, err, writes := o.res, o.err, o.writes
				// The request first: a host may refuse a wrong one.
				if len(writes) != 1 {
					t.Fatalf("%d write requests %s, want exactly the decision request", len(writes), describeWrites(writes))
				}
				checkWrite(t, "the decision request", writes[0], exp.DecisionRequest(spec, d, body))
				if err != nil {
					t.Fatalf("SetReviewDecision(%s): %v", d, err)
				}
				wantPosted := ""
				if body != "" {
					wantPosted = provider.BodyPostedReview
				}
				if res == nil || res.State != exp.DecisionState(d) || res.BodyPosted != wantPosted {
					t.Errorf("SetReviewDecision(%s) = %+v, want state %q and body_posted %q", d, res, exp.DecisionState(d), wantPosted)
				}
			})
		}
	})

	t.Run("refused_before_any_request", func(t *testing.T) {
		p, ref, log := s.serve(t, spec)
		pr := mustPR(t, p, ref)
		w := p.(provider.DecisionWriter)
		head := spec.HeadSHA
		type bad struct {
			name string
			req  provider.DecisionRequest
		}
		cases := []bad{
			{"unknown_decision", provider.DecisionRequest{Decision: "merge", HeadSHA: head}},
			{"empty_decision", provider.DecisionRequest{HeadSHA: head}},
			{"decision_in_another_case", provider.DecisionRequest{Decision: "APPROVE", HeadSHA: head}},
			{"uppercase_head", provider.DecisionRequest{Decision: first, HeadSHA: strings.ToUpper(head)}},
			{"short_head", provider.DecisionRequest{Decision: first, HeadSHA: head[:39]}},
			{"long_head", provider.DecisionRequest{Decision: first, HeadSHA: head + "0"}},
			{"non_hex_head", provider.DecisionRequest{Decision: first, HeadSHA: strings.Repeat("g", 40)}},
			{"no_head", provider.DecisionRequest{Decision: first}},
		}
		for _, d := range allDecisions {
			if !caps.Allows(d) {
				cases = append(cases, bad{"unsupported_" + string(d), provider.DecisionRequest{Decision: d, HeadSHA: head, Body: decisionBody}})
			}
		}
		if caps.RequestChanges && caps.RequestChangesNeedsBody {
			cases = append(cases, bad{"request_changes_without_body", provider.DecisionRequest{Decision: provider.DecisionRequestChanges, HeadSHA: head}})
		}
		n0, _ := log.count()
		for _, c := range cases {
			res, err := w.SetReviewDecision(t.Context(), ref, pr, c.req)
			if !errors.Is(err, provider.ErrProtocol) || res != nil {
				t.Errorf("%s: SetReviewDecision = %+v, %v; want nil and a protocol error", c.name, res, err)
			}
			checkClean(t, c.name, err)
		}
		if _, err := w.SetReviewDecision(t.Context(), ref, nil, provider.DecisionRequest{Decision: first, HeadSHA: head}); !errors.Is(err, provider.ErrProtocol) {
			t.Errorf("no pull request: error %v, want protocol", err)
		}
		if n, _ := log.count(); n != n0 {
			t.Errorf("%d requests were sent for refused decisions, want none", n-n0)
		}
	})

	t.Run("refusals", func(t *testing.T) {
		for _, c := range []struct {
			name   string
			status int
			moved  string
			want   *provider.Error
		}{
			{"status_409_head_moved", http.StatusConflict, movedHead, provider.ErrConflict},
			{"status_400_head_moved", http.StatusBadRequest, movedHead, provider.ErrConflict},
			{"status_400_head_unchanged", http.StatusBadRequest, "", provider.ErrProtocol},
			{"status_403_is_not_reread", http.StatusForbidden, movedHead, provider.ErrAuth},
		} {
			t.Run(c.name, func(t *testing.T) {
				spec := samplePR()
				spec.Env.Decision = DecisionEnv{Status: c.status, MovedHead: c.moved}
				o := decide(t, spec, first, decisionBody)
				res, err, writes := o.res, o.err, o.writes
				if !errors.Is(err, c.want) || res != nil {
					t.Fatalf("SetReviewDecision = %+v, %v; want nil and %s", res, err, c.want.Class)
				}
				checkClean(t, c.name, err)
				var hm *provider.HeadMovedError
				switch isMoved := errors.As(err, &hm); {
				case c.want == provider.ErrConflict && !isMoved:
					t.Errorf("error %v is not a *provider.HeadMovedError", err)
				case c.want != provider.ErrConflict && isMoved:
					t.Errorf("error %v is a *provider.HeadMovedError", err)
				case isMoved && c.status == http.StatusBadRequest && hm.Head != movedHead:
					t.Errorf("HeadMovedError.Head = %q, want the re-read's %q", hm.Head, movedHead)
				case isMoved && hm.Head != "" && hm.Head != movedHead:
					t.Errorf("HeadMovedError.Head = %q, want %q or unknown", hm.Head, movedHead)
				}
				// A refused decision posts nothing, not even its body.
				if len(writes) != 1 {
					t.Fatalf("%d write requests %s, want the decision request only", len(writes), describeWrites(writes))
				}
				checkWrite(t, "the decision request", writes[0], exp.DecisionRequest(spec, first, decisionBody))
			})
		}
	})

	t.Run("body", func(t *testing.T) {
		if caps.BodyInReview {
			o := decide(t, samplePR(), first, decisionBody)
			res, err, writes := o.res, o.err, o.writes
			if err != nil {
				t.Fatalf("SetReviewDecision: %v", err)
			}
			if res.BodyPosted != provider.BodyPostedReview {
				t.Errorf("body_posted %q, want %q", res.BodyPosted, provider.BodyPostedReview)
			}
			if len(writes) != 1 {
				t.Fatalf("%d write requests %s, want the review with its body only", len(writes), describeWrites(writes))
			}
			checkWrite(t, "the review", writes[0], exp.DecisionRequest(spec, first, decisionBody))
			return
		}
		t.Run("comment_after_the_decision", func(t *testing.T) {
			o := decide(t, samplePR(), first, decisionBody)
			res, err, writes, p, ref := o.res, o.err, o.writes, o.p, o.ref
			if err != nil {
				t.Fatalf("SetReviewDecision: %v", err)
			}
			if res.State != exp.DecisionState(first) || res.BodyPosted != provider.BodyPostedComment {
				t.Errorf("SetReviewDecision = %+v, want state %q and body_posted %q", res, exp.DecisionState(first), provider.BodyPostedComment)
			}
			if len(writes) != 2 {
				t.Fatalf("%d write requests %s, want the decision, then the comment", len(writes), describeWrites(writes))
			}
			// The order: the decision first, the body only after it.
			checkWrite(t, "the first write (the decision)", writes[0], exp.DecisionRequest(spec, first, decisionBody))
			if !bytes.Contains(writes[1].Body, []byte(decisionBody)) {
				t.Errorf("the second write %s %s does not carry the body", writes[1].Method, writes[1].Path)
			}
			if !hasGeneralComment(t, p, ref, decisionBody) {
				t.Error("the body is not a general comment of the pull request")
			}
		})
		t.Run("comment_fails", func(t *testing.T) {
			spec := samplePR()
			spec.Env.Decision.CommentStatus = http.StatusInternalServerError
			o := decide(t, spec, first, decisionBody)
			res, err, writes := o.res, o.err, o.writes
			if err != nil {
				t.Fatalf("SetReviewDecision: %v, want no error: the decision succeeded", err)
			}
			if res.State != exp.DecisionState(first) || res.BodyPosted != provider.BodyNotPosted {
				t.Errorf("SetReviewDecision = %+v, want state %q and body_posted %q", res, exp.DecisionState(first), provider.BodyNotPosted)
			}
			if len(writes) != 2 {
				t.Fatalf("%d write requests %s, want the decision, then the comment", len(writes), describeWrites(writes))
			}
			checkWrite(t, "the first write (the decision)", writes[0], exp.DecisionRequest(spec, first, decisionBody))
		})
	})
}

// checkWrite fails t when got is not want: the same method and path, and
// the same JSON value as body (no body when want.Body is nil).
func checkWrite(t *testing.T, what string, got request, want WriteRequest) {
	t.Helper()
	if got.Method != want.Method || got.Path != want.Path {
		t.Errorf("%s is %s %s, want %s %s", what, got.Method, got.Path, want.Method, want.Path)
	}
	if want.Body == nil {
		if len(bytes.TrimSpace(got.Body)) != 0 {
			t.Errorf("%s has the body %s, want none", what, got.Body)
		}
		return
	}
	wb, err := json.Marshal(want.Body)
	if err != nil {
		t.Fatalf("the expected body of %s cannot be encoded: %v", what, err)
	}
	var w, g any
	_ = json.Unmarshal(wb, &w)
	if err := json.Unmarshal(got.Body, &g); err != nil || !reflect.DeepEqual(g, w) {
		t.Errorf("%s has the body %s, want %s", what, got.Body, wb)
	}
}

func describeWrites(ws []request) string {
	var parts []string
	for _, w := range ws {
		parts = append(parts, w.Method+" "+w.Path)
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

// hasGeneralComment reports whether a general thread of the pull request
// has a comment with body.
func hasGeneralComment(t *testing.T, p provider.Provider, ref provider.PRRef, body string) bool {
	t.Helper()
	ts, err := p.ListThreads(t.Context(), ref)
	if err != nil {
		t.Fatalf("ListThreads: %v", err)
	}
	for _, th := range ts {
		for _, c := range th.Comments {
			if th.Kind == provider.ThreadGeneral && c.Body == body {
				return true
			}
		}
	}
	return false
}
