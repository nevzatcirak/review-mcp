package llmrun

import (
	"testing"
	"time"

	"github.com/nevzatcirak/review-mcp/internal/provider"
)

const testMarker = "[//]: # (review-mcp:test:v1)"

func TestHasMarkerLastLine(t *testing.T) {
	for body, want := range map[string]bool{
		"text\n\n" + testMarker:              true,
		"text\r\n\r\n" + testMarker + "\r\n": true,
		testMarker + "  \t\n":                true,
		testMarker:                           true,
		testMarker + "\ntext":                false,
		"text " + testMarker:                 false,
		testMarker + " x":                    false,
		"text":                               false,
		"":                                   false,
		"[//]: # (review-mcp:other:v1)":      false,
	} {
		if got := HasMarkerLastLine(body, testMarker); got != want {
			t.Errorf("HasMarkerLastLine(%q) = %v, want %v", body, got, want)
		}
	}
}

func TestWithMarker(t *testing.T) {
	if got := WithMarker("body \n\n", testMarker); got != "body\n\n"+testMarker {
		t.Errorf("WithMarker = %q", got)
	}
	if !HasMarkerLastLine(WithMarker("x", testMarker), testMarker) {
		t.Errorf("a marked body does not have the marker as its last line")
	}
}

func thread(id string, kind provider.ThreadKind, body, login, uid string, at time.Time) provider.Thread {
	return provider.Thread{ID: id, Kind: kind, Comments: []provider.CommentItem{{
		ID: id, Body: body, CreatedAt: at, AuthorID: uid, AuthorLogin: login, URL: "https://your-gitea.example/c/" + id,
	}}}
}

// FindMarked: marker plus author; the newest wins, older duplicates are
// counted and not adopted; another user's marker, a marker that is not the
// last line and an inline thread are never adopted.
func TestFindMarked(t *testing.T) {
	me := provider.User{ID: "42", Name: "review-bot"}
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	marked := "x\n\n" + testMarker
	threads := []provider.Thread{
		thread("5", provider.ThreadGeneral, marked, "review-bot", "42", t0),
		thread("9", provider.ThreadGeneral, marked, "REVIEW-BOT", "", t0.Add(time.Hour)),
		thread("7", provider.ThreadGeneral, marked, "mallory", "66", t0.Add(3*time.Hour)),
		thread("8", provider.ThreadGeneral, testMarker+"\nmore", "review-bot", "42", t0.Add(4*time.Hour)),
		thread("10", provider.ThreadInline, marked, "review-bot", "42", t0.Add(5*time.Hour)),
		thread("3", provider.ThreadGeneral, marked, "review-bot", "42", t0),
	}
	got := FindMarked(threads, me, testMarker)
	if got == nil || got.ID != "9" || got.Older != 2 || got.URL != "https://your-gitea.example/c/9" {
		t.Errorf("FindMarked = %+v, want comment 9 with 2 older", got)
	}
	if FindMarked(threads[2:5], me, testMarker) != nil {
		t.Errorf("a marker of another user, not on the last line, or inline was adopted")
	}
	if FindMarked(nil, me, testMarker) != nil {
		t.Errorf("no threads, yet a comment was found")
	}
}

// Equal times fall back to the larger id, compared as numbers.
func TestNewerComment(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	c := func(id string, at time.Time) *provider.CommentItem {
		return &provider.CommentItem{ID: id, CreatedAt: at}
	}
	for _, tc := range []struct {
		a, b *provider.CommentItem
		want bool
	}{
		{c("1", t0.Add(time.Second)), c("9", t0), true},
		{c("9", t0), c("10", t0), false},
		{c("10", t0), c("9", t0), true},
		{c("a", t0), c("b", t0), false},
	} {
		if got := NewerComment(tc.a, tc.b); got != tc.want {
			t.Errorf("NewerComment(%v, %v) = %v, want %v", tc.a, tc.b, got, tc.want)
		}
	}
}
