package repocfg

import (
	"fmt"
	"net/url"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/nevzatcirak/review-mcp/internal/provider"
)

// Related repositories declared in the repository config (v2.5 spec V-13b
// §3.2):
//
//	[[related.repos]]
//	url = "https://git.example/team/web-ui"   # a repository URL
//	branch = "main"                           # optional
//	paths = ["src/api/"]                      # optional, at most 10 prefixes
//
// The file is read at the base revision (X-40), so a pull request cannot add
// relations to itself. The declarations are kept only with the operator key
// related.from_repo_config; whether a run reads them (only when the call
// passes no related argument, and only through a configured provider) is
// the run's decision.

// keyRelatedRepos is the key of the declarations.
const keyRelatedRepos = "related.repos"

// Bounds of the declarations (§3.2 item 1).
const (
	// MaxRelatedRepos is the most declarations kept; later entries are
	// invalid.
	MaxRelatedRepos = 4
	// MaxRelatedPaths is the most path prefixes of one declaration.
	MaxRelatedPaths = 10
	// maxRelatedURLBytes is the longest declared URL (as a related URL).
	maxRelatedURLBytes = 2048
)

// NoteRelatedOff is the note of a file that declares related repositories
// while the operator has turned reading them off (§3.2 item 2, verbatim).
const NoteRelatedOff = "The repository config names related repositories, but reading them is turned off by the operator (related.from_repo_config)."

// Declared is one related repository the repository config declares: its
// URL as written, the branch it names ("" for none) and the path prefixes
// that confine its search and the tools' reads (nil: the whole tree).
type Declared struct {
	URL    string
	Branch string
	Paths  []string
}

// applyRelated handles the value v of related.repos: with
// related.from_repo_config off nothing is kept and off is true; otherwise
// the valid entries are kept in o.Related, in file order, and invalid names
// the invalid ones ("related.repos[i]", 0-based as in the related argument's
// messages), or the key itself when v is not a list of tables.
//
// DESIGN-QUESTION (WP-13c DQ-5): the spec reports invalid entries "like
// other invalid keys" while keeping the valid ones, but other keys are
// valid or not as a whole. Each invalid entry is named by its index
// (related.repos[1]) in repo_config.ignored and gets the invalid-key note;
// the key is applied when at least one entry is kept. An entry is invalid
// when it is not a table, has a key other than url, branch and paths, lacks
// url, has a URL that is not an absolute http(s) URL without credentials, a
// branch that is not a valid branch name, more than MaxRelatedPaths paths or
// a path that breaks the tools' path rules, or comes after the
// MaxRelatedRepos-th entry. Recommendation: keep.
func applyRelated(o *Outcome, v any) (applied bool, invalid []string, off bool) {
	if !o.Config.Related.FromRepoConfig {
		return false, nil, true
	}
	entries, ok := tables(v)
	if !ok {
		return false, []string{keyRelatedRepos}, false
	}
	for i, e := range entries {
		d, ok := declared(e)
		if !ok || i >= MaxRelatedRepos {
			invalid = append(invalid, fmt.Sprintf("%s[%d]", keyRelatedRepos, i))
			continue
		}
		o.Related = append(o.Related, d)
	}
	return len(o.Related) > 0, invalid, false
}

// tables reads a TOML array of tables ([[related.repos]], or an inline
// array of inline tables).
func tables(v any) ([]any, bool) {
	switch t := v.(type) {
	case []map[string]any:
		out := make([]any, len(t))
		for i, m := range t {
			out[i] = m
		}
		return out, true
	case []any:
		return t, true
	}
	return nil, false
}

// declared validates one entry.
func declared(e any) (Declared, bool) {
	m, ok := e.(map[string]any)
	if !ok {
		return Declared{}, false
	}
	var d Declared
	for k, v := range m {
		switch k {
		case "url":
			s, ok := v.(string)
			if !ok || !relatedURL(s) {
				return Declared{}, false
			}
			d.URL = strings.TrimSpace(s)
		case "branch":
			s, ok := v.(string)
			if !ok || !provider.ValidBranchName(s) {
				return Declared{}, false
			}
			d.Branch = s
		case "paths":
			l, ok := stringList(v)
			if !ok || len(l) == 0 || len(l) > MaxRelatedPaths {
				return Declared{}, false
			}
			for _, p := range l {
				if !ValidPathPrefix(p) {
					return Declared{}, false
				}
			}
			d.Paths = l
		default:
			return Declared{}, false
		}
	}
	return d, d.URL != ""
}

// relatedURL reports whether s is an absolute http or https URL with a host
// and without credentials, at most maxRelatedURLBytes: what the related
// argument accepts. Which provider and repository it names is decided when
// a run resolves it.
func relatedURL(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" || len(s) > maxRelatedURLBytes {
		return false
	}
	u, err := url.Parse(s)
	if err != nil || u.Host == "" || u.User != nil || u.Opaque != "" {
		return false
	}
	scheme := strings.ToLower(u.Scheme)
	return scheme == "http" || scheme == "https"
}

// maxPathBytes bounds a path prefix, as the repository tools bound a path.
const maxPathBytes = 4096

// ValidPathPrefix reports whether p is a directory prefix a declaration may
// confine a repository to: a directory the repository tools' path rules
// accept (repotools.AllowedPath with dir set: relative, "/"-separated, at
// most 4,096 bytes, valid UTF-8 without control characters, no "\", no
// empty, "." or ".." segment and no ".git" segment in any case, one
// trailing "/" allowed), other than the root. The rules are repeated here
// because the repository config may not depend on repository context (the
// dependency closure of pr_describe); a test of repotools keeps the two in
// agreement.
func ValidPathPrefix(p string) bool {
	p = strings.TrimSuffix(p, "/")
	if p == "" || len(p) > maxPathBytes || !utf8.ValidString(p) || strings.ContainsFunc(p, unicode.IsControl) ||
		strings.HasPrefix(p, "/") || strings.Contains(p, `\`) {
		return false
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == "" || seg == "." || seg == ".." || strings.EqualFold(seg, ".git") {
			return false
		}
	}
	return true
}
