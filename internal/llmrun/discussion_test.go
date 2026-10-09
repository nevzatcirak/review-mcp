package llmrun

import (
	"testing"

	"github.com/nevzatcirak/review-mcp/internal/provider"
)

func TestHasToolMarkerLastLine(t *testing.T) {
	for body, want := range map[string]bool{
		"text\n\n[//]: # (review-mcp:overview:v1)":            true,
		"text\n\n[//]: # (review-mcp:finding:0123456789ab)\n": true,
		"text\n\n[//]: # (review-mcp:describe:v1)  \r\n":      true,
		"[//]: # (review-mcp:overview:v1)\nmore text":         false,
		"text\n\n[//]: # (other-tool:overview:v1)":            false,
		"text\n\n[//]: # (review-mcp:overview:v1":             false,
		"": false,
	} {
		if got := HasToolMarkerLastLine(body); got != want {
			t.Errorf("HasToolMarkerLastLine(%q) = %v, want %v", body, got, want)
		}
	}
}

// TestFingerprintUnchanged: the moved fingerprint gives pr_review's pinned
// value (internal/review fingerprint_test.go).
func TestFingerprintUnchanged(t *testing.T) {
	if got := Fingerprint("src/app.go", "Possible Bug", "Line 11 is wrong."); got != "763daeb0f0c1" {
		t.Errorf("Fingerprint = %s", got)
	}
}

func TestHumanThreadsWithPredicate(t *testing.T) {
	threads := []provider.Thread{
		{Kind: provider.ThreadGeneral, Comments: []provider.CommentItem{{Body: "ours"}}},
		{Kind: provider.ThreadInline, Comments: []provider.CommentItem{{Body: "ours"}, {Body: "a reply"}}},
	}
	got := HumanThreads(threads, func(c *provider.CommentItem) bool { return c.Body == "ours" })
	if len(got) != 1 || len(got[0].Comments) != 1 || got[0].Comments[0].Body != "a reply" || len(threads[1].Comments) != 2 {
		t.Errorf("HumanThreads = %+v", got)
	}
}
