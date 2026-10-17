package issueref

import (
	"slices"
	"strings"
	"testing"

	"github.com/nevzatcirak/review-mcp/internal/provider"
)

const (
	ghHost = "git.example.com"
	ghWeb  = "https://git.example.com/octo/demo/pull/7"
	// glWeb is a GitLab merge request under a context path ("/gitlab").
	glWeb = "https://code.example.com/gitlab/group/sub/demo/-/merge_requests/7"
)

// wref is a reference as the tests write it.
type wref struct {
	n    int64
	src  Source
	repo string
}

func refsOf(rs Refs) []wref {
	out := []wref{}
	for _, r := range rs {
		out = append(out, wref{r.Number, r.Source, r.Repo})
	}
	return out
}

func find(title, desc, branch string) Refs {
	return Find(&provider.PullRequest{Title: title, Description: desc, SourceBranch: branch, WebURL: ghWeb}, ghHost)
}

func TestFindPatterns(t *testing.T) {
	cases := []struct {
		name                string
		title, desc, branch string
		want                []wref
	}{
		{name: "keyword fixes", desc: "This change fixes #12.", want: []wref{{12, SourceKeyword, ""}}},
		{name: "keyword with colon, any case", desc: "CLOSES: #4", want: []wref{{4, SourceKeyword, ""}}},
		{name: "every keyword form", desc: "close #1\ncloses #2\nclosed #3", want: []wref{
			{1, SourceKeyword, ""}, {2, SourceKeyword, ""}, {3, SourceKeyword, ""}}},
		{name: "gitlab keyword forms", desc: "Implements #5, resolving #6", want: []wref{
			{5, SourceKeyword, ""}, {6, SourceKeyword, ""}}},
		{name: "keyword at the start of a line", desc: "Summary.\nResolves #8", want: []wref{{8, SourceKeyword, ""}}},
		{name: "keyword owner/repo", desc: "fixes other/lib#9", want: []wref{{9, SourceKeyword, "other/lib"}}},
		{name: "keyword owner/repo of the own repository", desc: "fixes Octo/Demo#9", want: []wref{{9, SourceKeyword, ""}}},
		{name: "keyword nested path", desc: "closes group/sub/lib#3", want: []wref{{3, SourceKeyword, "group/sub/lib"}}},
		{name: "keyword issue URL", desc: "resolves https://git.example.com/octo/demo/issues/21",
			want: []wref{{21, SourceKeyword, ""}}},
		{name: "keyword issue URL of another repository", desc: "fixes https://git.example.com/acme/tools/issues/2.",
			want: []wref{{2, SourceKeyword, "acme/tools"}}},
		{name: "url in the description", desc: "See https://git.example.com/octo/demo/issues/30 for context.",
			want: []wref{{30, SourceURL, ""}}},
		{name: "url in the title", title: "Follow up on https://git.example.com/octo/demo/issues/31",
			want: []wref{{31, SourceURL, ""}}},
		{name: "url with a default port and a fragment", desc: "https://GIT.example.com:443/octo/demo/issues/32#note-1",
			want: []wref{{32, SourceURL, ""}}},
		{name: "markdown link", desc: "[the bug](https://git.example.com/octo/demo/issues/33)",
			want: []wref{{33, SourceURL, ""}}},
		{name: "title #N", title: "Handle empty input (#40)", want: []wref{{40, SourceTitle, ""}}},
		{name: "branch number first", branch: "41-short-name", want: []wref{{41, SourceBranch, ""}}},
		{name: "branch number with underscore", branch: "42_short", want: []wref{{42, SourceBranch, ""}}},
		{name: "branch word", branch: "fix/43-null-check", want: []wref{{43, SourceBranch, ""}}},
		{name: "branch word with dash", branch: "feature/issue-44-login", want: []wref{{44, SourceBranch, ""}}},
		{name: "branch gh word at the end", branch: "user/GH45", want: []wref{{45, SourceBranch, ""}}},
		{name: "branch issues word", branch: "issues/46", want: []wref{{46, SourceBranch, ""}}},
		{name: "branch both patterns", branch: "47-x/bug-48", want: []wref{{47, SourceBranch, ""}, {48, SourceBranch, ""}}},

		{name: "no keyword in the description", desc: "This is related to #123 and #124.", want: []wref{}},
		{name: "keyword without a reference", desc: "This fixes the bug in parsing 12 inputs.", want: []wref{}},
		{name: "keyword inside a word", desc: "prefixes #12 and suffixes #13", want: []wref{}},
		{name: "keyword on the line before", desc: "fixes\n#12", want: []wref{}},
		{name: "url on another host", desc: "fixes https://other.example/octo/demo/issues/50 and https://other.example/octo/demo/issues/51",
			want: []wref{}},
		{name: "url with credentials", desc: "https://user@git.example.com/octo/demo/issues/52", want: []wref{}},
		{name: "url on another port", desc: "https://git.example.com:8443/octo/demo/issues/53", want: []wref{}},
		{name: "pull request url", desc: "fixes https://git.example.com/octo/demo/pull/54", want: []wref{}},
		{name: "url without a repository", desc: "https://git.example.com/issues/55", want: []wref{}},
		{name: "url with a non-numeric issue", desc: "https://git.example.com/octo/demo/issues/new", want: []wref{}},
		{name: "references in a fenced block", desc: "Text.\n```\nfixes #60\nhttps://git.example.com/octo/demo/issues/61\n```\n~~~go\ncloses #62\n~~~",
			want: []wref{}},
		{name: "after a closed fence", desc: "```\nfixes #63\n```\nfixes #64", want: []wref{{64, SourceKeyword, ""}}},
		{name: "unclosed fence runs to the end", desc: "fixes #65\n```\nfixes #66", want: []wref{{65, SourceKeyword, ""}}},
		{name: "title owner/repo#N is not a title #N", title: "Port other/lib#70", want: []wref{}},
		{name: "title html entity", title: "Escape &#39; quotes", want: []wref{}},
		{name: "title #N followed by letters", title: "Support #1abc", want: []wref{}},
		{name: "branch with a version", branch: "v2-release", want: []wref{}},
		{name: "branch with a version path", branch: "release/v2.1-hotfix", want: []wref{}},
		{name: "branch word inside a word", branch: "prefix-12", want: []wref{}},
		{name: "branch word followed by letters", branch: "fix12abc", want: []wref{}},
		{name: "number zero", desc: "fixes #0", title: "#0", want: []wref{}},
		{name: "number too long", desc: "fixes #1234567890", want: []wref{}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := refsOf(find(c.title, c.desc, c.branch))
			if !slices.Equal(got, c.want) {
				t.Errorf("Find = %v, want %v", got, c.want)
			}
		})
	}
}

func TestFindOrderAndCap(t *testing.T) {
	desc := "See https://git.example.com/octo/demo/issues/2.\nFixes #1"
	got := refsOf(find("Title #3", desc, "4-x"))
	want := []wref{{1, SourceKeyword, ""}, {2, SourceURL, ""}, {3, SourceTitle, ""}}
	if !slices.Equal(got, want) {
		t.Errorf("Find = %v, want %v (keyword, URL, title; the branch reference is past the cap)", got, want)
	}

	got = refsOf(find("Title #3", "", "4-x"))
	want = []wref{{3, SourceTitle, ""}, {4, SourceBranch, ""}}
	if !slices.Equal(got, want) {
		t.Errorf("Find = %v, want %v", got, want)
	}

	got = refsOf(find("Url https://git.example.com/octo/demo/issues/6", "https://git.example.com/octo/demo/issues/5", ""))
	want = []wref{{6, SourceURL, ""}, {5, SourceURL, ""}}
	if !slices.Equal(got, want) {
		t.Errorf("Find = %v, want %v (title URLs before description URLs)", got, want)
	}
}

func TestFindDuplicates(t *testing.T) {
	desc := "Fixes #7. Also fixes https://git.example.com/octo/demo/issues/7 and octo/demo#7; fixes other/lib#7"
	got := refsOf(find("Fix (#7)", desc, "7-x"))
	want := []wref{{7, SourceKeyword, ""}, {7, SourceKeyword, "other/lib"}}
	if !slices.Equal(got, want) {
		t.Errorf("Find = %v, want %v (each issue once, the first source kept)", got, want)
	}
}

func TestFindCapOfThree(t *testing.T) {
	got := find("", "fixes #1, fixes #2, fixes #3, fixes #4, fixes #5", "")
	if len(got) != MaxRefs || got[2].Number != 3 {
		t.Errorf("Find = %v, want the first %d", refsOf(got), MaxRefs)
	}
}

func TestFindSanitisesText(t *testing.T) {
	t.Run("control characters", func(t *testing.T) {
		got := refsOf(find("Title\x00 #\x1b8", "fix\x07es #9", "1\x0010-x"))
		want := []wref{{9, SourceKeyword, ""}, {8, SourceTitle, ""}, {110, SourceBranch, ""}}
		if !slices.Equal(got, want) {
			t.Errorf("Find = %v, want %v (control characters removed before the scan)", got, want)
		}
	})
	t.Run("only the first 10 KiB of the description", func(t *testing.T) {
		pad := strings.Repeat("x", MaxDescriptionBytes-len("fixes #1 "))
		got := refsOf(find("", "fixes #1 "+pad+" fixes #2", ""))
		if want := []wref{{1, SourceKeyword, ""}}; !slices.Equal(got, want) {
			t.Errorf("Find = %v, want %v", got, want)
		}
	})
	t.Run("a cut inside a character", func(t *testing.T) {
		s := "x" + strings.Repeat("é", MaxDescriptionBytes)
		if got := cleanDescription(s); len(got) > MaxDescriptionBytes || !strings.HasPrefix(s, got) || len(got)%2 != 1 {
			t.Errorf("cleanDescription cut %d bytes, want a whole number of characters within %d", len(got), MaxDescriptionBytes)
		}
	})
	t.Run("line breaks", func(t *testing.T) {
		got := refsOf(find("", "Summary.\r\nfixes #3\rfixes #4", ""))
		if want := []wref{{3, SourceKeyword, ""}, {4, SourceKeyword, ""}}; !slices.Equal(got, want) {
			t.Errorf("Find = %v, want %v", got, want)
		}
	})
}

func TestFindHost(t *testing.T) {
	pr := &provider.PullRequest{Description: "fixes https://git.example.com/octo/demo/issues/1", WebURL: ghWeb}
	for _, host := range []string{"", "other.example", "git.example.com:8443"} {
		if got := Find(pr, host); len(got) != 0 {
			t.Errorf("Find with host %q = %v, want none", host, refsOf(got))
		}
	}
	for _, host := range []string{"git.example.com", "GIT.EXAMPLE.COM", "git.example.com:443"} {
		if got := Find(pr, host); len(got) != 1 {
			t.Errorf("Find with host %q = %v, want the issue", host, refsOf(got))
		}
	}
	if got := Find(nil, ghHost); got != nil {
		t.Errorf("Find(nil) = %v, want nil", got)
	}
}

func TestFindGitLabContextPath(t *testing.T) {
	desc := "Closes https://code.example.com/gitlab/group/sub/demo/-/issues/5\n" +
		"See https://code.example.com/gitlab/group/other/-/issues/6 and https://code.example.com/gitlab/group/sub/demo/issues/7\n" +
		"fixes group/sub/demo#8, fixes group/tools#9"
	pr := &provider.PullRequest{Description: desc, WebURL: glWeb}
	got := Find(pr, "code.example.com")
	want := []wref{{5, SourceKeyword, ""}, {8, SourceKeyword, ""}, {9, SourceKeyword, "group/tools"}}
	if g := refsOf(got); !slices.Equal(g, want) {
		t.Fatalf("Find = %v, want %v", g, want)
	}
	// The URL references after the cap: read them alone.
	pr.Description = "See https://code.example.com/gitlab/group/other/-/issues/6 and https://code.example.com/gitlab/group/sub/demo/issues/7"
	got = Find(pr, "code.example.com")
	want = []wref{{6, SourceURL, "gitlab/group/other"}, {7, SourceURL, ""}}
	if g := refsOf(got); !slices.Equal(g, want) {
		t.Fatalf("Find = %v, want %v", g, want)
	}
	ns, name, ok := Repository(got[0], glWeb, "group/sub", "demo")
	if !ok || ns != "group" || name != "other" {
		t.Errorf("Repository = %q %q %v, want group other (the context path dropped)", ns, name, ok)
	}
}

func TestRepository(t *testing.T) {
	pr := &provider.PullRequest{WebURL: ghWeb, Description: "fixes other/lib#1, fixes group/sub/lib#2, " +
		"fixes https://git.example.com/acme/tools/issues/3"}
	refs := Find(pr, ghHost)
	if len(refs) != 3 {
		t.Fatalf("Find = %v, want 3 references", refsOf(refs))
	}
	cases := []struct {
		ref      Ref
		ns, name string
	}{
		{Ref{Number: 9}, "octo", "demo"},
		{refs[0], "other", "lib"},
		{refs[1], "group/sub", "lib"},
		{refs[2], "acme", "tools"},
	}
	for _, c := range cases {
		ns, name, ok := Repository(c.ref, ghWeb, "octo", "demo")
		if !ok || ns != c.ns || name != c.name {
			t.Errorf("Repository(%v) = %q %q %v, want %q %q", c.ref, ns, name, ok, c.ns, c.name)
		}
	}
	// An issue URL whose repository cannot be told from the pull request's
	// web URL is not located.
	for _, c := range []struct{ web, ns, repo string }{
		{"", "octo", "demo"},
		{"https://git.example.com/elsewhere/x/pull/7", "octo", "demo"},
		{ghWeb, "", ""},
	} {
		if _, _, ok := Repository(refs[2], c.web, c.ns, c.repo); ok {
			t.Errorf("Repository with web URL %q and %q/%q: ok, want not located", c.web, c.ns, c.repo)
		}
	}
	if _, _, ok := Repository(Ref{Number: 1, Repo: "../x"}, ghWeb, "octo", "demo"); ok {
		t.Error("Repository of a path with a dot segment: ok, want not located")
	}
}

func TestRepoSegments(t *testing.T) {
	cases := map[string][]string{
		ghWeb: {"octo", "demo"},
		"https://git.example.com/sub/octo/demo/pulls/7": {"sub", "octo", "demo"},
		glWeb: {"gitlab", "group", "sub", "demo"},
		"https://bbs.example.com/projects/P/repos/r/pull-requests/7/overview": {"projects", "P", "repos", "r"},
		"https://git.example.com/octo/demo":                                   nil,
		"https://git.example.com/pull/7":                                      nil,
		"":                                                                    nil,
	}
	for in, want := range cases {
		if got := repoSegments(in); !slices.Equal(got, want) {
			t.Errorf("repoSegments(%q) = %v, want %v", in, got, want)
		}
	}
}
