package provider

import (
	"errors"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// TestCleanListText: canary (f) of WP-19a. Newlines and tabs become spaces,
// controls, line separators and bidi characters are removed, space runs
// collapse, and the cut is at a rune boundary.
func TestCleanListText(t *testing.T) {
	for _, c := range []struct {
		name, in string
		max      int
		want     string
	}{
		{"plain", "Fix the parser", 200, "Fix the parser"},
		{"newline_and_tab", "Fix\nthe\r\nparser\tnow", 200, "Fix the parser now"},
		{"bidi", "abc\u202edcba\u2066x\u2069", 200, "abcdcbax"},
		{"controls", "a\x00b\x07c\x1bd\u0085e\u009bf\x7fg", 200, "abcdefg"},
		{"line_separators", "one\u2028two\u2029three", 200, "onetwothree"},
		{"space_runs", "  a   b \n\n c  ", 200, "a b c"},
		{"invalid_utf8", "a\xffb", 200, "a�b"},
		{"cut_runes", strings.Repeat("é", 300), 200, strings.Repeat("é", 200)},
		{"cut_then_trim", strings.Repeat("a", 199) + " b", 200, strings.Repeat("a", 199)},
		{"empty", " \n\t ", 200, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := CleanListText(c.in, c.max)
			if got != c.want {
				t.Errorf("CleanListText(%q, %d) = %q, want %q", c.in, c.max, got, c.want)
			}
			if !utf8.ValidString(got) {
				t.Errorf("CleanListText(%q) is not valid UTF-8", c.in)
			}
		})
	}
	// The canary's own case: a title with a newline, a bidi control and
	// more than 200 runes.
	in := "Rename\n\u202ethe helper " + strings.Repeat("x", 300)
	got := CleanListText(in, MaxListTitleRunes)
	if strings.ContainsAny(got, "\n\u202e") || utf8.RuneCountInString(got) != MaxListTitleRunes ||
		!strings.HasPrefix(got, "Rename the helper x") {
		t.Errorf("CleanListText(title) = %q (%d runes), want one line without the bidi control, cut at %d runes",
			got, utf8.RuneCountInString(got), MaxListTitleRunes)
	}
}

func TestValidateListQuery(t *testing.T) {
	caps := ListCaps{Reviewer: true, Author: true}
	for _, c := range []struct {
		q    ListQuery
		want *Error
	}{
		{ListQuery{Role: ListReviewer, Limit: 1}, nil},
		{ListQuery{Role: ListAuthor, Limit: MaxListLimit}, nil},
		{ListQuery{Role: ListAssignee, Limit: 30}, ErrUnsupported},
		{ListQuery{Role: "owner", Limit: 30}, ErrUnsupported},
		{ListQuery{Role: "", Limit: 30}, ErrUnsupported},
		{ListQuery{Role: ListReviewer, Limit: 0}, ErrProtocol},
		{ListQuery{Role: ListReviewer, Limit: MaxListLimit + 1}, ErrProtocol},
	} {
		err := ValidateListQuery(caps, c.q)
		if (c.want == nil) != (err == nil) || (c.want != nil && !errors.Is(err, c.want)) {
			t.Errorf("ValidateListQuery(%+v) = %v, want %v", c.q, err, c.want)
		}
	}
	if (ListCaps{}).Any() || !caps.Any() {
		t.Error("Any is wrong")
	}
}

func TestListedRef(t *testing.T) {
	parse := func(rem string) (string, string, int64, error) {
		segs := strings.Split(strings.TrimPrefix(rem, "/"), "/")
		if len(segs) != 4 || segs[2] != "pulls" || segs[0] == ".." || segs[1] == ".." {
			return "", "", 0, errors.New("bad")
		}
		if segs[3] != "7" {
			return segs[0], segs[1], 8, nil
		}
		return segs[0], segs[1], 7, nil
	}
	ref, ok := ListedRef(KindGitea, "https://gitea.example.com/", "/octo/demo/pulls/7", "octo", "demo", 7, parse)
	if !ok || ref.URL != "https://gitea.example.com/octo/demo/pulls/7" || ref.Kind != KindGitea ||
		ref.Namespace != "octo" || ref.Repo != "demo" || ref.Number != 7 {
		t.Errorf("ListedRef = %+v, %v", ref, ok)
	}
	for _, c := range []struct {
		name, rem, ns, repo string
		n                   int64
	}{
		{"dots", "/../demo/pulls/7", "..", "demo", 7},
		{"number_differs", "/octo/demo/pulls/9", "octo", "demo", 9},
		{"empty_namespace", "//demo/pulls/7", "", "demo", 7},
		{"no_number", "/octo/demo/pulls/0", "octo", "demo", 0},
		{"no_slash", "octo/demo/pulls/7", "octo", "demo", 7},
	} {
		if _, ok := ListedRef(KindGitea, "https://gitea.example.com", c.rem, c.ns, c.repo, c.n, parse); ok {
			t.Errorf("%s: ListedRef accepted %q", c.name, c.rem)
		}
	}
}

func TestFinishList(t *testing.T) {
	t0 := time.Date(2026, 1, 2, 3, 4, 0, 0, time.UTC)
	item := func(u string, h int) ListedPR {
		return ListedPR{Ref: PRRef{URL: u}, UpdatedAt: t0.Add(time.Duration(h) * time.Hour)}
	}
	l := FinishList([]ListedPR{item("b", 1), item("c", 3), item("a", 1)}, 2, false, 1)
	if len(l.Items) != 2 || l.Items[0].Ref.URL != "c" || l.Items[1].Ref.URL != "a" || !l.HasMore || l.Dropped != 1 {
		t.Errorf("FinishList = %+v", l)
	}
	if l := FinishList(nil, 30, false, 0); l.Items == nil || len(l.Items) != 0 || l.HasMore {
		t.Errorf("FinishList(nil) = %+v, want an empty, non-nil list", l)
	}
	if l := FinishList([]ListedPR{item("a", 1)}, 30, true, 0); !l.HasMore {
		t.Error("the host's HasMore was lost")
	}
}

func TestListBranch(t *testing.T) {
	if b := ListBranch("release/1.2"); b == nil || *b != "release/1.2" {
		t.Errorf("ListBranch(release/1.2) = %v", b)
	}
	for _, s := range []string{"", "a b", "../x", "-x", "x\ny"} {
		if ListBranch(s) != nil {
			t.Errorf("ListBranch(%q) is not nil", s)
		}
	}
}
