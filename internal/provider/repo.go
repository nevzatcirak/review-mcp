package provider

import "strings"

// ItemKind is the kind of a related item (X-41): a pull request (V-13a) or a
// repository, optionally a branch of it (V-13b).
type ItemKind string

const (
	// ItemPullRequest is a pull request URL; Resolve and ResolveItem return
	// its PRRef.
	ItemPullRequest ItemKind = "pull_request"
	// ItemRepository is a repository URL; ResolveItem returns its RepoRef.
	ItemRepository ItemKind = "repository"
)

// RepoRef identifies one repository, and optionally one branch of it, named
// by a repository URL (v2.5 spec V-13b §1.1).
//
// Kind, Namespace and Repo follow PRRef's rules (a GitLab namespace may hold
// "/"; whatever builds a URL, a cache path or a log field from it escapes it
// per segment). Branch is "" for the repository's default branch; otherwise
// ValidBranchName holds for it. URL is the user-supplied URL; it may only be
// logged through logging.RedactURL, and a branch never at all.
type RepoRef struct {
	Kind      Kind
	Namespace string
	Repo      string
	Branch    string
	URL       string
}

// MaxBranchBytes is the longest branch name a repository URL may name.
const MaxBranchBytes = 255

// ValidBranchName reports whether b is a branch name review-mcp accepts
// (spec §1.1 item 2), before any git call: 1 to MaxBranchBytes bytes of
// A-Z a-z 0-9 "/" "." "_" "-"; no "..", no "//", no leading or trailing "/"
// or "."; no "/"-separated segment that starts with "." or "-" or ends in
// ".lock"; and not "HEAD". Such a name is a valid git ref name below
// refs/heads/ that can neither be read as an option nor form a ref
// expression ("@{", "^", "~", ":" and the like are outside the set).
//
// The spec refuses a name "ending in .lock"; every segment is held to that
// here, since git refuses a ref with any segment ending in ".lock" and the
// fetch would otherwise fail later with a less precise reason.
func ValidBranchName(b string) bool {
	if b == "" || len(b) > MaxBranchBytes || b == "HEAD" {
		return false
	}
	for i := 0; i < len(b); i++ {
		c := b[i]
		if (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && (c < '0' || c > '9') && c != '/' && c != '.' && c != '_' && c != '-' {
			return false
		}
	}
	if strings.Contains(b, "..") || strings.Contains(b, "//") ||
		strings.HasPrefix(b, "/") || strings.HasSuffix(b, "/") ||
		strings.HasPrefix(b, ".") || strings.HasSuffix(b, ".") {
		return false
	}
	for _, seg := range strings.Split(b, "/") {
		if strings.HasPrefix(seg, ".") || strings.HasPrefix(seg, "-") || strings.HasSuffix(seg, ".lock") {
			return false
		}
	}
	return true
}

// TrimGitSuffix drops one trailing ".git" from a repository name taken from
// a repository URL (spec §1.1 item 1); "" when nothing would be left.
func TrimGitSuffix(name string) string {
	return strings.TrimSuffix(name, ".git")
}
