package provider

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
)

const testHead = "a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1"

func TestDecisionCapsAllows(t *testing.T) {
	all := DecisionCaps{Approve: true, RequestChanges: true, Clear: true}
	for _, d := range []Decision{DecisionApprove, DecisionRequestChanges, DecisionClear} {
		if !all.Allows(d) || (DecisionCaps{}).Allows(d) {
			t.Errorf("Allows(%q) does not follow the caps", d)
		}
	}
	for _, d := range []Decision{"", "merge", "APPROVE", "approve "} {
		if all.Allows(d) {
			t.Errorf("Allows(%q) = true for an unknown decision", d)
		}
	}
	if (DecisionCaps{AtomicHead: true, BodyInReview: true, RequestChangesNeedsBody: true}).Any() || !(DecisionCaps{Clear: true}).Any() {
		t.Error("Any does not follow the decision flags")
	}
}

func TestValidHeadSHA(t *testing.T) {
	for s, want := range map[string]bool{
		testHead:                      true,
		strings.Repeat("0f", 32):      true,
		"":                            false,
		testHead[:39]:                 false,
		testHead + "a":                false,
		strings.ToUpper(testHead):     false,
		strings.Repeat("g", 40):       false,
		strings.Repeat("a", 63) + " ": false,
	} {
		if ValidHeadSHA(s) != want {
			t.Errorf("ValidHeadSHA(%q) = %v, want %v", s, !want, want)
		}
	}
}

func TestValidateDecision(t *testing.T) {
	gh := DecisionCaps{Approve: true, RequestChanges: true, BodyInReview: true, RequestChangesNeedsBody: true}
	for _, c := range []struct {
		name string
		caps DecisionCaps
		req  DecisionRequest
		hint string
	}{
		{"ok", gh, DecisionRequest{Decision: DecisionApprove, HeadSHA: testHead}, ""},
		{"ok_with_body", gh, DecisionRequest{Decision: DecisionRequestChanges, HeadSHA: testHead, Body: "x"}, ""},
		{"not_allowed", gh, DecisionRequest{Decision: DecisionClear, HeadSHA: testHead}, "the decision is not supported"},
		{"unknown", gh, DecisionRequest{Decision: "merge", HeadSHA: testHead}, "the decision is not supported"},
		{"bad_head", gh, DecisionRequest{Decision: DecisionApprove, HeadSHA: "abc"}, "invalid head sha"},
		{"no_body", gh, DecisionRequest{Decision: DecisionRequestChanges, HeadSHA: testHead}, "a body is required for request_changes"},
		{"blank_body", gh, DecisionRequest{Decision: DecisionRequestChanges, HeadSHA: testHead, Body: " \t\n"}, "a body is required for request_changes"},
		{"no_body_needed", DecisionCaps{RequestChanges: true}, DecisionRequest{Decision: DecisionRequestChanges, HeadSHA: testHead}, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			err := ValidateDecision(c.caps, c.req)
			if c.hint == "" {
				if err != nil {
					t.Errorf("ValidateDecision: %v, want nil", err)
				}
				return
			}
			var pe *Error
			if !errors.As(err, &pe) || pe.Class != ClassProtocol || pe.Hint != c.hint {
				t.Errorf("ValidateDecision: %v, want protocol with hint %q", err, c.hint)
			}
		})
	}
}

func TestHeadMovedError(t *testing.T) {
	err := error(&HeadMovedError{Head: testHead, Status: http.StatusBadRequest})
	var pe *Error
	if !errors.Is(err, ErrConflict) || !errors.As(err, &pe) || pe.Hint != HintHeadMoved || pe.Status != http.StatusBadRequest {
		t.Errorf("HeadMovedError is not a conflict with the hint %q: %#v", HintHeadMoved, err)
	}
	if want := "the request conflicts with the current state on the server (HTTP 400): head moved"; err.Error() != want {
		t.Errorf("Error() = %q, want %q", err.Error(), want)
	}
}

func TestDecisionRefused(t *testing.T) {
	moved := strings.Repeat("e", 40)
	for _, c := range []struct {
		name    string
		err     error
		head    string
		reread  error
		wantRe  bool
		wantErr error
	}{
		{"400_moved", StatusError(400), moved, nil, true, ErrConflict},
		{"404_moved", StatusError(404), moved, nil, true, ErrConflict},
		{"409_moved", StatusError(409), moved, nil, true, ErrConflict},
		{"422_unmoved", StatusError(422), testHead, nil, true, ErrProtocol},
		{"400_moved_to_a_non_sha", StatusError(400), "not-a-sha", nil, true, ErrConflict},
		{"400_moved_to_uppercase", StatusError(400), strings.ToUpper(moved), nil, true, ErrConflict},
		{"400_reread_fails", StatusError(400), "", ErrUpstream, true, ErrProtocol},
		{"401", StatusError(401), moved, nil, false, ErrAuth},
		{"403", StatusError(403), moved, nil, false, ErrAuth},
		{"429", StatusError(429), moved, nil, false, ErrRateLimited},
		{"500", StatusError(500), moved, nil, false, ErrUpstream},
		{"transport", TransportError(context.DeadlineExceeded), moved, nil, false, ErrTransport},
	} {
		t.Run(c.name, func(t *testing.T) {
			reads := 0
			err := DecisionRefused(t.Context(), c.err, testHead, func(context.Context) (*PullRequest, error) {
				reads++
				if c.reread != nil {
					return nil, c.reread
				}
				return &PullRequest{HeadSHA: c.head}, nil
			})
			if (reads == 1) != c.wantRe || reads > 1 {
				t.Errorf("%d re-reads, want re-read %v", reads, c.wantRe)
			}
			if !errors.Is(err, c.wantErr) {
				t.Errorf("error %v, want %v", err, c.wantErr)
			}
			var hm *HeadMovedError
			wantMoved := errors.Is(c.wantErr, ErrConflict)
			// The new head is kept only when it is a commit id.
			wantHead := ""
			if ValidHeadSHA(c.head) {
				wantHead = c.head
			}
			if moved := errors.As(err, &hm); moved != wantMoved || (moved && hm.Head != wantHead) {
				t.Errorf("error %#v: want a HeadMovedError with head %q: %v", err, wantHead, wantMoved)
			}
		})
	}
}

// commentFake is a Provider whose PostComment records its body and fails
// with err.
type commentFake struct {
	Provider
	bodies []string
	err    error
}

func (f *commentFake) PostComment(_ context.Context, _ PRRef, body string) (*Comment, error) {
	f.bodies = append(f.bodies, body)
	if f.err != nil {
		return nil, f.err
	}
	return &Comment{ID: "1"}, nil
}

func TestPostDecisionBody(t *testing.T) {
	for _, c := range []struct {
		name     string
		caps     DecisionCaps
		body     string
		err      error
		want     string
		comments int
	}{
		{"no_body", DecisionCaps{}, "", nil, "", 0},
		{"in_review", DecisionCaps{BodyInReview: true}, "x", nil, BodyPostedReview, 0},
		{"comment", DecisionCaps{}, "x", nil, BodyPostedComment, 1},
		{"comment_fails", DecisionCaps{}, "x", StatusError(500), BodyNotPosted, 1},
		{"blank_body", DecisionCaps{}, " \r\n ", nil, "", 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := &commentFake{err: c.err}
			if got := PostDecisionBody(t.Context(), f, PRRef{}, c.caps, c.body); got != c.want {
				t.Errorf("PostDecisionBody = %q, want %q", got, c.want)
			}
			if len(f.bodies) != c.comments || (c.comments == 1 && f.bodies[0] != c.body) {
				t.Errorf("comments %q, want %d with the body", f.bodies, c.comments)
			}
		})
	}
}

func TestDecisionBody(t *testing.T) {
	for in, want := range map[string]string{"": "", "   ": "", "\t\r\n": "", " x ": " x ", "LGTM": "LGTM"} {
		if got := DecisionBody(in); got != want {
			t.Errorf("DecisionBody(%q) = %q, want %q", in, got, want)
		}
	}
}
