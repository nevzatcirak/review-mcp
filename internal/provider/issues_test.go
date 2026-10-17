package provider

import (
	"slices"
	"strings"
	"testing"
)

func TestNewIssue(t *testing.T) {
	for state, want := range map[string]IssueState{
		"open": IssueOpen, "opened": IssueOpen, "locked": IssueOpen, "": IssueOpen, "closed": IssueClosed,
	} {
		if got := NewIssue(1, "", "", state, "", nil).State; got != want {
			t.Errorf("state %q: %q, want %q", state, got, want)
		}
	}
	is := NewIssue(4, "T", "B", "open", "javascript:alert(1)", []string{"", "a", ""})
	if is.URL != "" || !slices.Equal(is.Labels, []string{"a"}) || is.Number != 4 || is.Title != "T" || is.Body != "B" {
		t.Errorf("NewIssue = %+v, want no URL and the label a", is)
	}
	if is := NewIssue(4, "", "", "open", "https://git.example.com/o/r/issues/4", nil); is.URL != "https://git.example.com/o/r/issues/4" {
		t.Errorf("URL %q, want the issue's page", is.URL)
	}
}

func TestCutIssueBody(t *testing.T) {
	exact := strings.Repeat("a", MaxIssueBodyBytes)
	if got := CutIssueBody(exact); got != exact {
		t.Errorf("a body of exactly %d bytes was cut", MaxIssueBodyBytes)
	}
	over := exact + "b"
	if got := CutIssueBody(over); got != exact+IssueBodyCutMarker {
		t.Errorf("a body one byte over: %d bytes, want the first %d and the marker", len(got), MaxIssueBodyBytes)
	}
	fits := strings.Repeat("a", MaxIssueBodyBytes-2) + "\u00e9"
	if got := CutIssueBody(fits); got != fits {
		t.Error("a body of exactly the cap that ends with a two-byte character was changed")
	}
	straddles := strings.Repeat("a", MaxIssueBodyBytes-1) + "\u00e9"
	if got := CutIssueBody(straddles); got != strings.Repeat("a", MaxIssueBodyBytes-1)+IssueBodyCutMarker {
		t.Error("a character across the cap was not cut back to the character boundary before it")
	}
}
