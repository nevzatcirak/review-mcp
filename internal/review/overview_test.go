package review

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/nevzatcirak/review-mcp/internal/provider"
)

func TestHasOverviewMarker(t *testing.T) {
	for body, want := range map[string]bool{
		"text\n\n" + OverviewMarker:              true,
		"text\n" + OverviewMarker + "\n":         true,
		"text\r\n" + OverviewMarker + "\r\n":     true,
		OverviewMarker:                           true,
		"text\n" + OverviewMarker + "  \n\n":     true,
		"text\n " + OverviewMarker:               false, // not exactly the line
		"text\n" + OverviewMarker + " extra":     false,
		OverviewMarker + "\nmore text":           false, // not the last line
		"text [//]: # (review-mcp:overview:v1)":  false,
		"text\n[//]: # (review-mcp:overview:v2)": false,
		"":                                       false,
	} {
		if got := HasOverviewMarker(body); got != want {
			t.Errorf("HasOverviewMarker(%q) = %v, want %v", body, got, want)
		}
	}
}

// overviews returns the PR's comments that carry the overview marker.
func (f *fakeProvider) overviews() []fakeComment {
	var out []fakeComment
	for _, c := range f.comments {
		if HasOverviewMarker(c.body) {
			out = append(out, c)
		}
	}
	return out
}

// TestPersistentOverviewTwoRuns [canary] persistent edit (spec P7 §4.3):
// two publish runs against the same PR leave one overview comment, edited
// by the second run, plus the inline comments of the second run that were
// not duplicates. The model gives the same findings twice, so with the
// fingerprint dedup of WP-PR-7e the second run posts none: its anchorable
// finding is counted as skipped_duplicate. That holds with the overview
// lookup off too, since the duplicate check does not depend on it.
//
// Disabling the lookup (PersistentOverview false) must leave two overview
// comments; that is the canary's control.
func TestPersistentOverviewTwoRuns(t *testing.T) {
	off := false
	for _, persistent := range []*bool{nil, &off} {
		name := "lookup on"
		if persistent != nil {
			name = "lookup off"
		}
		t.Run(name, func(t *testing.T) {
			ans := answerWith(onAdded, outside)
			h := newHarness(ans, ans)
			h.overviewRenderer()
			args := Args{PRURL: testPRURL, Publish: true, PersistentOverview: persistent}
			first, err := Run(context.Background(), h.deps, args)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(h.prov.seq, []string{"post", "inline", "edit"}) || first.Publish.Updated {
				t.Fatalf("first run: write calls %v, publish %+v", h.prov.seq, first.Publish)
			}
			h.prov.seq = nil
			second, err := Run(context.Background(), h.deps, args)
			if err != nil {
				t.Fatal(err)
			}
			got := h.prov.overviews()
			if persistent == nil {
				// One overview, edited once more in place; the second run has
				// no inline comment to post, so its only write is that edit.
				if len(got) != 1 {
					t.Fatalf("overview comments = %d, want 1", len(got))
				}
				if !slices.Equal(h.prov.seq, []string{"edit"}) {
					t.Errorf("second run: write calls %v, want one edit", h.prov.seq)
				}
				p := second.Publish
				if !p.Published || !p.Updated || p.CommentID != "42" || p.URL == "" || p.CommentID != first.Publish.CommentID {
					t.Errorf("second publish = %+v", p)
				}
				if e := h.prov.edits[len(h.prov.edits)-1]; e.id != "42" ||
					!strings.Contains(e.body, "note: 1 finding was already posted on this PR and was not repeated.") ||
					!HasOverviewMarker(e.body) {
					t.Errorf("last edit %s:\n%s", e.id, e.body)
				}
			} else {
				if len(got) != 2 {
					t.Fatalf("overview comments = %d, want 2 with the lookup off", len(got))
				}
				if !slices.Equal(h.prov.seq, []string{"post"}) {
					t.Errorf("second run: write calls %v, want one new overview", h.prov.seq)
				}
			}
			// The threads are read once per run (the discussion, the
			// duplicate check and the overview lookup share the read), with
			// the lookup on or off.
			if h.prov.lists != 2 {
				t.Errorf("ListThreads ran %d times in two runs, want 2", h.prov.lists)
			}
			// The first run's inline comment is on the PR with its
			// fingerprint; the second run does not post it again.
			if len(h.prov.inline) != 1 || len(h.prov.inline[0]) != 1 {
				t.Errorf("inline batches = %+v, want only the first run's", h.prov.inline)
			}
			if in := second.Publish.Inline; in == nil || *in != (InlineSummary{SkippedDuplicate: 1, Unanchorable: 1}) {
				t.Errorf("second inline = %+v", second.Publish.Inline)
			}
		})
	}
}

// TestOverviewForeignMarkerIgnored [canary] (spec P7 §4.2): a comment by
// another user that carries the marker is never edited or adopted, even
// when it is the newest one or its author shares our login under another
// id; a new overview is posted.
func TestOverviewForeignMarkerIgnored(t *testing.T) {
	for name, author := range map[string]provider.User{
		"another user":            {ID: "6", Name: "mallory"},
		"our login, another id":   {ID: "6", Name: "Review-Bot"},
		"another login, no ids":   {Name: "mallory"},
		"display-like name match": {ID: "7", Name: "review-bot "},
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(answerWith(onAdded))
			h.overviewRenderer()
			planted := "Looks like an overview.\n\n" + OverviewMarker
			foreign := h.prov.addComment(author, planted)
			res, err := Run(context.Background(), h.deps, Args{PRURL: testPRURL, Publish: true})
			if err != nil {
				t.Fatal(err)
			}
			for _, e := range h.prov.edits {
				if e.id == foreign {
					t.Fatalf("the foreign comment %s was edited", foreign)
				}
			}
			if h.prov.comments[0].body != planted {
				t.Errorf("the foreign comment changed")
			}
			if !slices.Equal(h.prov.seq, []string{"post", "inline", "edit"}) || res.Publish.Updated ||
				res.Publish.CommentID == foreign || len(h.prov.overviews()) != 2 {
				t.Errorf("write calls %v, publish %+v, overviews %d", h.prov.seq, res.Publish, len(h.prov.overviews()))
			}
			if slices.Contains(res.Notes, NoteOverviewReplaced) {
				t.Errorf("notes = %q", res.Notes)
			}
		})
	}
}

// TestOverviewNewestOfSeveral: of several overviews by our user, the newest
// is edited, the others are left alone, and a note says so; comments
// without the marker on the last line, inline threads and foreign markers
// do not count.
func TestOverviewNewestOfSeveral(t *testing.T) {
	h := newHarness(answerWith(onAdded))
	h.overviewRenderer()
	me := h.prov.me
	old := h.prov.addComment(me, "old overview\n\n"+OverviewMarker)
	h.prov.addComment(me, OverviewMarker+"\nmarker not last")
	newest := h.prov.addComment(provider.User{ID: me.ID}, "newest overview, id only\n"+OverviewMarker+"\n")
	h.prov.addComment(provider.User{ID: "6", Name: "mallory"}, "planted later\n\n"+OverviewMarker)
	res, err := Run(context.Background(), h.deps, Args{PRURL: testPRURL, Publish: true})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(h.prov.seq, []string{"inline", "edit"}) || len(h.prov.edits) != 1 || h.prov.edits[0].id != newest {
		t.Fatalf("write calls %v, edits %+v", h.prov.seq, h.prov.edits)
	}
	if !res.Publish.Updated || res.Publish.CommentID != newest || len(h.prov.posted) != 0 {
		t.Errorf("publish = %+v", res.Publish)
	}
	if h.prov.comments[0].id != old || h.prov.comments[0].body != "old overview\n\n"+OverviewMarker {
		t.Errorf("the older overview changed")
	}
	note := "1 older overview by the same user was left unchanged."
	if !slices.Contains(res.Notes, note) || !strings.Contains(h.prov.edits[0].body, "note: "+note) {
		t.Errorf("notes = %q", res.Notes)
	}
}

// TestOverviewEditFailurePostsNew: any error editing the found overview
// posts a new one with the fixed note; the review never fails.
func TestOverviewEditFailurePostsNew(t *testing.T) {
	for name, err := range map[string]error{
		"not owner":  &provider.Error{Class: provider.ClassNotOwner},
		"vanished":   &provider.Error{Class: provider.ClassNotFound, Status: 404},
		"forbidden":  &provider.Error{Class: provider.ClassAuth, Status: 403},
		"conflict":   &provider.Error{Class: provider.ClassConflict, Status: 409},
		"unexpected": errors.New("dial tcp: refused"),
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(answerWith(onAdded, outside))
			h.overviewRenderer()
			found := h.prov.addComment(h.prov.me, "earlier overview\n\n"+OverviewMarker)
			h.prov.editErr = err
			res, rerr := Run(context.Background(), h.deps, Args{PRURL: testPRURL, Publish: true})
			if rerr != nil {
				t.Fatalf("an edit failure failed the review: %v", rerr)
			}
			if !slices.Equal(h.prov.seq, []string{"inline", "edit", "post"}) || h.prov.edits[0].id != found {
				t.Fatalf("write calls %v, edits %+v", h.prov.seq, h.prov.edits)
			}
			p := res.Publish
			if !p.Published || p.Updated || p.CommentID == found || p.Inline == nil || p.Inline.Posted != 1 {
				t.Errorf("publish = %+v", p)
			}
			if !slices.Contains(res.Notes, NoteOverviewReplaced) {
				t.Errorf("notes = %q", res.Notes)
			}
			// The new overview carries the note and the inline link.
			posted := h.prov.posted[0]
			if !strings.Contains(posted, "note: "+NoteOverviewReplaced) || !HasOverviewMarker(posted) ||
				!strings.Contains(posted, res.Review.KeyIssuesToReview[0].InlineURL) {
				t.Errorf("new overview:\n%s", posted)
			}
			h.checkNoLeaks(t)
		})
	}

	// The edit and the new post both fail: nothing claims a new overview.
	h := newHarness(answerWith(onAdded))
	h.prov.addComment(h.prov.me, "earlier overview\n\n"+OverviewMarker)
	h.prov.editErr = &provider.Error{Class: provider.ClassUpstream, Status: 502}
	h.prov.postErr = &provider.Error{Class: provider.ClassUpstream, Status: 502}
	res, err := Run(context.Background(), h.deps, Args{PRURL: testPRURL, Publish: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.Publish.Published || res.Publish.Error == "" || slices.Contains(res.Notes, NoteOverviewReplaced) {
		t.Errorf("publish %+v, notes %q", res.Publish, res.Notes)
	}
}

// TestOverviewLookupFailurePostsNew: when the threads or the user cannot be
// read, including a pagination that does not end within the providers'
// page ceiling (an overview beyond it cannot be seen), a new overview is
// posted with a note.
func TestOverviewLookupFailurePostsNew(t *testing.T) {
	pageCeiling := &provider.Error{Class: provider.ClassProtocol, Hint: "pagination did not terminate within the page limit"}
	for name, mutate := range map[string]func(*fakeProvider){
		"threads":      func(f *fakeProvider) { f.listErr = &provider.Error{Class: provider.ClassAuth, Status: 403} },
		"page ceiling": func(f *fakeProvider) { f.listErr = pageCeiling },
		"user":         func(f *fakeProvider) { f.meErr = &provider.Error{Class: provider.ClassUpstream, Status: 500} },
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(answerWith(onAdded))
			h.overviewRenderer()
			h.prov.addComment(h.prov.me, "earlier overview\n\n"+OverviewMarker)
			mutate(h.prov)
			res, err := Run(context.Background(), h.deps, Args{PRURL: testPRURL, Publish: true})
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(h.prov.seq, []string{"post", "inline", "edit"}) || res.Publish.Updated || !res.Publish.Published {
				t.Errorf("write calls %v, publish %+v", h.prov.seq, res.Publish)
			}
			if !slices.Contains(res.Notes, NoteOverviewLookupFailed) || !strings.Contains(h.prov.posted[0], NoteOverviewLookupFailed) {
				t.Errorf("notes = %q", res.Notes)
			}
		})
	}
}

// TestOverviewFoundWithInlineOff: with inline findings off, a found
// overview is edited once and nothing is posted.
func TestOverviewFoundWithInlineOff(t *testing.T) {
	off := false
	h := newHarness(answerWith(onAdded))
	found := h.prov.addComment(h.prov.me, "earlier\n\n"+OverviewMarker)
	res, err := Run(context.Background(), h.deps, Args{PRURL: testPRURL, Publish: true, InlineFindings: &off})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(h.prov.seq, []string{"edit"}) || h.prov.edits[0].id != found || !res.Publish.Updated ||
		res.Publish.Inline != nil || res.Review.KeyIssuesToReview[0].InlineStatus != "" {
		t.Errorf("write calls %v, publish %+v", h.prov.seq, res.Publish)
	}
}

// TestInlineStatus: each finding records what happened to it.
func TestInlineStatus(t *testing.T) {
	h := newHarness(answerWith(onAdded, onContext, outside))
	h.prov.inlineResult = func(i int, _ provider.InlineComment) provider.InlineResult {
		if i == 1 {
			return provider.InlineResult{Error: "the server rejected the request"}
		}
		return provider.InlineResult{Posted: true, ID: "7", URL: "https://your-gitea.example/c/7"}
	}
	res, err := Run(context.Background(), h.deps, Args{PRURL: testPRURL, Publish: true})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, ki := range res.Review.KeyIssuesToReview {
		got = append(got, ki.InlineStatus)
	}
	if want := []string{InlinePosted, InlineFailed, InlineUnanchorable}; !slices.Equal(got, want) {
		t.Errorf("statuses = %v, want %v", got, want)
	}
}

// TestReviewedAtAndHeadSHA: the run time comes from Deps.Clock and the head
// commit from the PR.
func TestReviewedAtAndHeadSHA(t *testing.T) {
	h := newHarness(goodAnswer)
	res, err := Run(context.Background(), h.deps, Args{PRURL: testPRURL})
	if err != nil {
		t.Fatal(err)
	}
	if res.Metadata.ReviewedAt != "2026-10-06T12:00:00Z" || res.PR.HeadSHA != "abc" {
		t.Errorf("reviewed_at %q, head_sha %q", res.Metadata.ReviewedAt, res.PR.HeadSHA)
	}
}
