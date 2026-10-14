package gitlab

import (
	"errors"
	"slices"
	"strconv"
	"testing"

	"github.com/nevzatcirak/review-mcp/internal/provider"
	"github.com/nevzatcirak/review-mcp/internal/provider/contract"
)

func ctThread(kind provider.ThreadKind, line int, id int64, author contract.User) contract.Thread {
	th := contract.Thread{Kind: kind, Comments: []contract.Comment{{ID: id, Author: author, Body: "Comment " + strconv.FormatInt(id, 10)}}}
	if kind == provider.ThreadInline {
		th.Path, th.Line = "src/app.go", line
	}
	return th
}

func ctThreadByID(t *testing.T, ts []provider.Thread, id string) *provider.Thread {
	t.Helper()
	for i := range ts {
		if ts[i].ID == id {
			return &ts[i]
		}
	}
	t.Fatalf("thread %s is not listed", id)
	return nil
}

// TestContractOutdated: a DiffNote whose position is left on an older head
// than the merge request's diff_refs.head_sha is outdated, one on the
// current head is not; the head is read once, with the merge request.
// [canary]
func TestContractOutdated(t *testing.T) {
	spec := ctSpec(ctModified)
	old := ctThread(provider.ThreadInline, 2, 51, contract.User{ID: 101, Login: "alice"})
	old.Outdated = true
	spec.Threads = []contract.Thread{old, ctThread(provider.ThreadInline, 2, 52, contract.User{ID: 101, Login: "alice"})}
	p, ref, _, _ := ctFixture{}.serve(t, spec)
	ts, err := p.ListThreads(t.Context(), ref)
	if err != nil {
		t.Fatalf("ListThreads: %v", err)
	}
	if got := ctThreadByID(t, ts, "51").Outdated; !got {
		t.Error("the DiffNote on an older head is not outdated")
	}
	if got := ctThreadByID(t, ts, "52").Outdated; got {
		t.Error("the DiffNote on the current head is outdated")
	}
}

// TestContractThreadShapes: system notes are dropped, a note on a removed
// line has Line 0, an individual note is not resolvable (Resolved nil), a
// resolvable discussion reports its state, and note ids are the ids.
func TestContractThreadShapes(t *testing.T) {
	bob := contract.User{ID: 102, Login: "bob"}
	spec := ctSpec(ctModified)
	removed := ctThread(provider.ThreadInline, 0, 61, bob)
	spec.Threads = []contract.Thread{
		ctThread(provider.ThreadGeneral, 0, 60, bob), // unresolved: an individual note
		removed,
		ctThread(provider.ThreadInline, 2, 62, bob),
	}
	spec.Threads[2].Resolved = true
	p, ref, _, _ := ctFixture{individual: true, systemNote: true}.serve(t, spec)
	ts, err := p.ListThreads(t.Context(), ref)
	if err != nil {
		t.Fatalf("ListThreads: %v", err)
	}
	var ids []string
	for _, th := range ts {
		ids = append(ids, th.ID)
	}
	if !slices.Equal(ids, []string{"60", "61", "62"}) {
		t.Errorf("thread ids = %q, want the three note ids and no system note", ids)
	}
	if g := ctThreadByID(t, ts, "60"); g.Kind != provider.ThreadGeneral || g.Resolved != nil || !g.ReplyInThread {
		t.Errorf("individual note: kind %s, resolved %v, reply in thread %v", g.Kind, g.Resolved, g.ReplyInThread)
	}
	if r := ctThreadByID(t, ts, "61"); r.Kind != provider.ThreadInline || r.Line != 0 || r.Path != "src/app.go" || r.Resolved == nil || *r.Resolved {
		t.Errorf("removed line: %+v", r)
	}
	if r := ctThreadByID(t, ts, "62"); r.Resolved == nil || !*r.Resolved || r.Line != 2 {
		t.Errorf("resolved inline thread: %+v", r)
	}
	if u := ctThreadByID(t, ts, "62").Comments[0].URL; u != "https://gitlab.example.com/group/sub/project/-/merge_requests/7#note_62" {
		t.Errorf("comment URL = %q", u)
	}
}

// TestContractReplyInThread: a reply to an individual note and to an inline
// note joins the discussion (POST .../discussions/{id}/notes), and a note
// that is on no discussion is not_found before any write.
func TestContractReplyInThread(t *testing.T) {
	bob := contract.User{ID: 102, Login: "bob"}
	spec := ctSpec(ctModified)
	spec.Threads = []contract.Thread{ctThread(provider.ThreadGeneral, 0, 70, bob), ctThread(provider.ThreadInline, 2, 71, bob)}
	p, ref, g, _ := ctFixture{individual: true}.serve(t, spec)
	for _, id := range []string{"70", "71"} {
		rr, err := p.ReplyToComment(t.Context(), ref, id, "Thanks.")
		if err != nil || !rr.InThread {
			t.Fatalf("ReplyToComment(%s) = %+v, %v; want in thread", id, rr, err)
		}
	}
	ts, err := p.ListThreads(t.Context(), ref)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"70", "71"} {
		if n := len(ctThreadByID(t, ts, id).Comments); n != 2 {
			t.Errorf("thread %s has %d comments, want 2", id, n)
		}
	}
	nWrites := len(g.writes)
	_, err = p.ReplyToComment(t.Context(), ref, "424242", "Thanks.")
	if !errors.Is(err, provider.ErrNotFound) || len(g.writes) != nWrites {
		t.Errorf("reply to an unknown note: %v (%d writes)", err, len(g.writes)-nWrites)
	}
}

// TestContractEditAndPost: PostComment creates a general note with the
// {web_url}#note_{id} link; EditComment refuses another user's note
// before any write and replaces the body of the token user's own note.
func TestContractEditAndPost(t *testing.T) {
	spec := ctSpec(ctModified)
	spec.Threads = []contract.Thread{
		ctThread(provider.ThreadGeneral, 0, 80, contract.User{ID: 102, Login: "bob"}),
		ctThread(provider.ThreadGeneral, 0, 81, spec.TokenUser),
	}
	p, ref, g, _ := ctFixture{}.serve(t, spec)
	c, err := p.PostComment(t.Context(), ref, "A new comment.")
	if err != nil || c.ID == "" || c.URL == "" {
		t.Fatalf("PostComment = %+v, %v", c, err)
	}
	// The link is built from the merge request's web_url, which the fake
	// serves as https://gitlab.example.com/..., not from the configured base.
	if want := "https://gitlab.example.com/group/sub/project/-/merge_requests/7#note_" + c.ID; c.URL != want {
		t.Errorf("URL = %q, want %q", c.URL, want)
	}
	if err := p.EditComment(t.Context(), ref, "80", "Mine now."); !errors.Is(err, provider.ErrNotOwner) {
		t.Errorf("edit of bob's note: %v, want not_owner", err)
	}
	if err := p.EditComment(t.Context(), ref, "81", "Updated."); err != nil {
		t.Fatalf("EditComment: %v", err)
	}
	if want := []string{"A new comment.", "Updated."}; !slices.Equal(g.writes, want) {
		t.Errorf("writes = %q, want %q", g.writes, want)
	}
	ts, _ := p.ListThreads(t.Context(), ref)
	if b := ctThreadByID(t, ts, "81").Comments[0].Body; b != "Updated." {
		t.Errorf("edited body = %q", b)
	}
}

// TestContractQuickActionBody: a body goes through SanitizeBody before it
// is published (the callers do it, with the capability this provider
// sets), so a line "/close" reaches the server as " /close".
func TestContractQuickActionBody(t *testing.T) {
	p, ref, g, _ := ctFixture{}.serve(t, ctSpec(ctModified))
	if !p.Capabilities().QuickActions {
		t.Fatal("Capabilities().QuickActions is false")
	}
	body := provider.SanitizeBody(p.Capabilities(), "Please look.\n/close\nThanks.")
	if _, err := p.PostComment(t.Context(), ref, body); err != nil {
		t.Fatal(err)
	}
	if want := []string{"Please look.\n /close\nThanks."}; !slices.Equal(g.writes, want) {
		t.Errorf("published %q, want %q", g.writes, want)
	}
}
