package provider

import (
	"slices"
	"strings"
	"testing"
)

func TestCleanCheckName(t *testing.T) {
	long := strings.Repeat("\u00e9", 130)
	cases := map[string]string{
		"build":                 "build",
		"  padded  ":            "padded",
		"a\nb\tc\x1b[0md\x7f":   "abc[0md",
		"over\u202eride\u2066x": "overridex",
		"bad\xffutf8":           "bad\ufffdutf8",
		long:                    strings.Repeat("\u00e9", 100),
		"\n\t":                  "",
	}
	for in, want := range cases {
		if got := CleanCheckName(in); got != want {
			t.Errorf("CleanCheckName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCleanCheckURL(t *testing.T) {
	keep := []string{"https://ci.example.com/x?y=1#z", "http://ci.example.com", "HTTPS://ci.example.com/a"}
	drop := []string{
		"", "javascript:alert(1)", "ftp://ci.example.com/x", "data:text/html,x", "//ci.example.com/x",
		"/relative", "https://someone@ci.example.com/", "https://ci.example.com/a b", "https://ci.example.com/\n",
		"https:///nohost", "https://ci.example.com/" + strings.Repeat("a", 2100),
	}
	for _, u := range keep {
		if got := CleanCheckURL(u); got != u {
			t.Errorf("CleanCheckURL(%q) = %q, want it kept", u, got)
		}
	}
	for _, u := range drop {
		if got := CleanCheckURL(u); got != "" {
			t.Errorf("CleanCheckURL(%q) = %q, want it dropped", u, got)
		}
	}
}

func TestValidRevision(t *testing.T) {
	for _, ok := range []string{"a1b2c3", "main", "feature/x", "a b&c"} {
		if !ValidRevision(ok) {
			t.Errorf("ValidRevision(%q) = false", ok)
		}
	}
	for _, bad := range []string{"", ".", "..", "a\nb", "a\x00b", strings.Repeat("a", 256)} {
		if ValidRevision(bad) {
			t.Errorf("ValidRevision(%q) = true", bad)
		}
	}
}

func TestFinishDirListing(t *testing.T) {
	got := FinishDirListing([]DirEntry{{Name: "b"}, {Name: "a", Dir: true}}, false)
	if want := []DirEntry{{Name: "a", Dir: true}, {Name: "b"}}; !slices.Equal(got.Entries, want) || got.Truncated {
		t.Errorf("FinishDirListing = %+v", got)
	}
	if got := FinishDirListing(nil, true); !got.Truncated || len(got.Entries) != 0 {
		t.Errorf("FinishDirListing(nil, true) = %+v", got)
	}
	var many []DirEntry
	for i := MaxDirEntries + 5; i > 0; i-- {
		many = append(many, DirEntry{Name: strings.Repeat("z", i%7) + string(rune('a'+i%26))})
	}
	got = FinishDirListing(many, false)
	if len(got.Entries) != MaxDirEntries || !got.Truncated {
		t.Errorf("%d entries, truncated %v", len(got.Entries), got.Truncated)
	}
	// The host's first MaxDirEntries entries are kept, then sorted.
	kept := slices.Clone(many[:MaxDirEntries])
	slices.SortStableFunc(kept, func(a, b DirEntry) int { return strings.Compare(a.Name, b.Name) })
	if !slices.Equal(got.Entries, kept) {
		t.Error("the kept entries are not the host's first ones, sorted")
	}
}
