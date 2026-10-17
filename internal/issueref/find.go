package issueref

import (
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/nevzatcirak/review-mcp/internal/provider"
)

// MaxRefs is the most references Find returns.
const MaxRefs = 3

// Source says where a reference was found.
type Source string

// Sources, in the order Find applies them.
const (
	// SourceKeyword: a closing keyword in the description ("fixes #12").
	SourceKeyword Source = "keyword"
	// SourceURL: an issue URL on the pull request's host in the title or
	// the description.
	SourceURL Source = "url"
	// SourceTitle: "#N" in the title.
	SourceTitle Source = "title"
	// SourceBranch: a number in the source branch's name.
	SourceBranch Source = "branch"
)

// Ref is one issue a pull request names.
type Ref struct {
	// Number is the issue's number in its repository (GitLab's iid).
	Number int64
	Source Source
	// Repo is "" for the pull request's own repository. Otherwise it is the
	// other repository on the same host, as the reference names it: the
	// path of "owner/repo#12" ("owner/repo", "group/sub/project"), or the
	// repository part of an issue URL's path (which starts with the host's
	// context path, if it has one). Segments are unescaped and joined by
	// "/". Repository turns it into a namespace and a name.
	Repo string
	// viaURL: Repo comes from a URL's path, not from "owner/repo#N".
	viaURL bool
}

// Refs are the references of a pull request, in Find's order.
type Refs []Ref

// Patterns of the references. A number has at most 9 digits.
const (
	// urlPattern is an http(s) URL up to the first space, quote, bracket or
	// backtick; trailing punctuation is trimmed afterwards (trimURL).
	urlPattern = "https?://[^\\s<>()\\[\\]\"'`]+"
	// repoPattern is "owner/repo" or a nested GitLab path.
	repoPattern = `[\w.-]+(?:/[\w.-]+){1,9}`
	// keywords are the closing keywords of GitHub, GitLab and Gitea.
	keywords = `close[sd]?|closing|fix(?:e[sd])?|fixing|resolve[sd]?|resolving|implement(?:s|ed|ing)?`
)

var (
	// DESIGN-QUESTION (WP-10a DQ-1): GitLab also closes a list after one
	// keyword ("Closes #1, #2 and #3"); GitHub and Gitea need the keyword
	// before each reference. Only the reference right after a keyword is
	// taken, which every host agrees on. Recommendation: keep it; the list
	// form adds references the author may not have meant as closing.
	keywordRE = regexp.MustCompile(`(?i)(?:^|[^\w])(?:` + keywords + `)(?::[ \t]*|[ \t]+)(` + urlPattern +
		`|(?:` + repoPattern + `)?#\d{1,9}\b)`)
	urlRE   = regexp.MustCompile(urlPattern)
	titleRE = regexp.MustCompile(`(?:^|[^\w/&#])#(\d{1,9})\b`)
	// branchRE is the first branch pattern: a branch that starts with the
	// number ("12-short-name").
	branchRE = regexp.MustCompile(`^(\d{1,9})[-_]`)
	// branchWordRE is the second branch pattern, matched at the start of the
	// name and after each "/" ("fix/12", "feature/issue-12-x").
	branchWordRE = regexp.MustCompile(`(?i)^(?:issue|issues|gh|bug|fix|feature)[-_/]?(\d{1,9})(?:[-_/]|$)`)
)

// Find returns the issues pr names, at most MaxRefs of them, in this order:
// closing keywords in the description, issue URLs on host in the title and
// then the description, "#N" in the title, and the source branch's name.
// A reference that repeats an earlier one is dropped. host is the host of
// the pull request's web URL ("name" or "name:port", as url.URL.Host); an
// issue URL on any other host is never a reference. A number in the
// description without a keyword is not a reference either.
func Find(pr *provider.PullRequest, host string) Refs {
	if pr == nil {
		return nil
	}
	f := finder{own: repoSegments(pr.WebURL), host: normHost(host)}
	title, desc := cleanLine(pr.Title), cleanDescription(pr.Description)
	for _, m := range keywordRE.FindAllStringSubmatch(desc, -1) {
		f.target(m[1], SourceKeyword)
	}
	for _, text := range []string{title, desc} {
		for _, raw := range urlRE.FindAllString(text, -1) {
			if repo, n, ok := f.issueURL(raw); ok {
				f.add(Ref{Number: n, Source: SourceURL, Repo: repo, viaURL: true})
			}
		}
	}
	for _, m := range titleRE.FindAllStringSubmatch(title, -1) {
		f.number(m[1], SourceTitle)
	}
	branch := cleanLine(pr.SourceBranch)
	if m := branchRE.FindStringSubmatch(branch); m != nil {
		f.number(m[1], SourceBranch)
	}
	for i := 0; i <= len(branch); i++ {
		if i == 0 || branch[i-1] == '/' {
			if m := branchWordRE.FindStringSubmatch(branch[i:]); m != nil {
				f.number(m[1], SourceBranch)
			}
		}
	}
	return f.refs
}

// finder collects the references of one pull request.
type finder struct {
	// own are the segments of the pull request's repository path in its web
	// URL (context path included); nil when it cannot be told.
	own  []string
	host string
	refs Refs
	seen map[string]bool
}

// add keeps r unless it repeats an earlier reference or MaxRefs are kept.
func (f *finder) add(r Ref) {
	if r.Number <= 0 || len(f.refs) >= MaxRefs {
		return
	}
	key := strings.ToLower(r.Repo) + "#" + strconv.FormatInt(r.Number, 10)
	if f.seen[key] {
		return
	}
	if f.seen == nil {
		f.seen = map[string]bool{}
	}
	f.seen[key] = true
	f.refs = append(f.refs, r)
}

// number adds the issue numbered digits of the pull request's repository.
func (f *finder) number(digits string, src Source) {
	if n, err := strconv.ParseInt(digits, 10, 64); err == nil {
		f.add(Ref{Number: n, Source: src})
	}
}

// target adds the reference after a closing keyword: an issue URL on the
// host, "#N" or "owner/repo#N".
func (f *finder) target(t string, src Source) {
	if strings.HasPrefix(strings.ToLower(t), "http") {
		if repo, n, ok := f.issueURL(t); ok {
			f.add(Ref{Number: n, Source: src, Repo: repo, viaURL: true})
		}
		return
	}
	path, digits, _ := strings.Cut(t, "#")
	n, err := strconv.ParseInt(digits, 10, 64)
	if err != nil {
		return
	}
	r := Ref{Number: n, Source: src}
	if path != "" && !hasSuffixFold(f.own, strings.Split(path, "/")) {
		r.Repo = path
	}
	f.add(r)
}

// issueURL parses raw as the URL of an issue on f.host: ".../issues/N" or
// GitLab's ".../-/issues/N", with at least two segments before it. repo is
// "" when the issue is in the pull request's own repository.
func (f *finder) issueURL(raw string) (repo string, n int64, ok bool) {
	u, err := url.Parse(trimURL(raw))
	if err != nil || u.User != nil || u.Host == "" || normHost(u.Host) != f.host || f.host == "" {
		return "", 0, false
	}
	if s := strings.ToLower(u.Scheme); s != "http" && s != "https" {
		return "", 0, false
	}
	segs, ok := segments(u.EscapedPath())
	if !ok || len(segs) < 4 || segs[len(segs)-2] != "issues" {
		return "", 0, false
	}
	n, err = strconv.ParseInt(segs[len(segs)-1], 10, 64)
	if err != nil || n <= 0 || !allDigits(segs[len(segs)-1]) {
		return "", 0, false
	}
	segs = segs[:len(segs)-2]
	if segs[len(segs)-1] == "-" {
		segs = segs[:len(segs)-1]
	}
	if len(segs) < 2 {
		return "", 0, false
	}
	if equalFold(segs, f.own) {
		return "", n, true
	}
	return strings.Join(segs, "/"), n, true
}

// Repository returns the namespace and the name of r's repository. namespace
// and repo are those of the pull request, and webURL is its web URL, which
// tells an issue URL's context path from its namespace. ok is false when the
// repository cannot be told.
func Repository(r Ref, webURL, namespace, repo string) (string, string, bool) {
	if r.Repo == "" {
		return namespace, repo, namespace != "" && repo != ""
	}
	segs := strings.Split(r.Repo, "/")
	if r.viaURL {
		own := repoSegments(webURL)
		pr := strings.Split(namespace+"/"+repo, "/")
		if namespace == "" || repo == "" || !hasSuffixFold(own, pr) {
			return "", "", false
		}
		prefix := own[:len(own)-len(pr)]
		if len(segs) < len(prefix) || (len(prefix) > 0 && !equalFold(segs[:len(prefix)], prefix)) {
			return "", "", false
		}
		segs = segs[len(prefix):]
	}
	if len(segs) < 2 {
		return "", "", false
	}
	for _, s := range segs {
		if s == "" || s == "." || s == ".." {
			return "", "", false
		}
	}
	return strings.Join(segs[:len(segs)-1], "/"), segs[len(segs)-1], true
}

// prMarkers are the path segments that follow a repository's path in a pull
// request's web URL: GitHub's pull, Gitea's pulls, GitLab's merge_requests
// and Bitbucket Server's pull-requests.
var prMarkers = map[string]bool{"pull": true, "pulls": true, "merge_requests": true, "pull-requests": true}

// repoSegments returns the repository path of a pull request's web URL:
// the segments before its last pull request marker that is followed by a
// number (GitLab's "-" dropped); nil when there is none.
func repoSegments(webURL string) []string {
	u, err := url.Parse(webURL)
	if err != nil {
		return nil
	}
	segs, ok := segments(u.EscapedPath())
	if !ok {
		return nil
	}
	for i := len(segs) - 2; i >= 1; i-- {
		if prMarkers[segs[i]] && allDigits(segs[i+1]) {
			out := segs[:i]
			if out[len(out)-1] == "-" {
				out = out[:len(out)-1]
			}
			if len(out) < 2 {
				return nil
			}
			return out
		}
	}
	return nil
}

// segments splits an escaped URL path into unescaped segments, without the
// leading and a trailing empty one. ok is false when a segment cannot be
// unescaped, is empty, or holds a "/" once unescaped.
func segments(escaped string) ([]string, bool) {
	escaped = strings.TrimSuffix(strings.TrimPrefix(escaped, "/"), "/")
	if escaped == "" {
		return nil, false
	}
	out := strings.Split(escaped, "/")
	for i, s := range out {
		u, err := url.PathUnescape(s)
		if err != nil || u == "" || strings.Contains(u, "/") {
			return nil, false
		}
		out[i] = u
	}
	return out, true
}

// trimURL drops the punctuation that ends a sentence or Markdown emphasis
// from the end of a URL found in text.
func trimURL(s string) string {
	return strings.TrimRight(s, ".,;:!?*_~")
}

// normHost lower-cases host and drops a default port (":80", ":443").
func normHost(host string) string {
	host = strings.ToLower(strings.TrimSpace(host))
	host = strings.TrimSuffix(host, ":443")
	return strings.TrimSuffix(host, ":80")
}

func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// equalFold reports whether a and b hold the same segments, without regard
// to case.
func equalFold(a, b []string) bool {
	if len(a) != len(b) || len(a) == 0 {
		return false
	}
	for i := range a {
		if !strings.EqualFold(a[i], b[i]) {
			return false
		}
	}
	return true
}

// hasSuffixFold reports whether s ends with the segments suffix, without
// regard to case.
func hasSuffixFold(s, suffix []string) bool {
	return len(suffix) > 0 && len(s) >= len(suffix) && equalFold(s[len(s)-len(suffix):], suffix)
}
