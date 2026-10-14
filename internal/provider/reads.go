package provider

import (
	"net/url"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

// CheckState is the provider-neutral state of one CI status or check (X-31).
type CheckState string

// Check states. Every provider value maps onto one of these; a value the
// provider does not document is CheckNeutral.
const (
	CheckSuccess CheckState = "success"
	CheckFailure CheckState = "failure"
	CheckPending CheckState = "pending"
	CheckNeutral CheckState = "neutral"
	CheckSkipped CheckState = "skipped"
	CheckError   CheckState = "error"
)

// Limits of the reads of WP-5a.
const (
	// MaxChecks is the most checks GetChecks returns; a commit with more
	// gets Checks.Truncated.
	MaxChecks = 500
	// MaxDirEntries is the most entries ListDir returns; a directory with
	// more gets DirListing.Truncated.
	MaxDirEntries = 500
	// MaxCheckNameRunes is the length a check name is cut to.
	MaxCheckNameRunes = 100
	// maxCheckURLBytes is the longest check URL kept; a longer one is
	// dropped.
	maxCheckURLBytes = 2048
)

// Check strategies reported in Checks.Strategy: which endpoint (or
// endpoints) answered GetChecks.
const (
	// ChecksGiteaCombined: GET /repos/{o}/{r}/commits/{sha}/status.
	ChecksGiteaCombined = "gitea:combined_status"
	// ChecksBBSBuilds: the repository-scoped build status, GET
	// /rest/api/latest/projects/{p}/repos/{r}/commits/{sha}/builds.
	ChecksBBSBuilds = "bbs:repo_builds"
	// ChecksBBSBuildStatus: the legacy build status, GET
	// /rest/build-status/1.0/commits/{sha}, read when the repository-scoped
	// endpoint answers 404 (an older server).
	ChecksBBSBuildStatus = "bbs:build_status"
	// ChecksGitHubRunsAndStatuses: the check runs and the combined status,
	// merged by name.
	ChecksGitHubRunsAndStatuses = "github:check_runs+statuses"
	// ChecksGitLabCommitStatuses: the commit statuses of the head commit,
	// GET /projects/{id}/repository/commits/{sha}/statuses (2C WP-6d).
	ChecksGitLabCommitStatuses = "gitlab:commit_statuses"
)

// Check is one CI status or check of a commit. Name and URL are untrusted
// third-party text, cleaned by NewCheck.
type Check struct {
	Name  string
	State CheckState
	// URL is an http(s) link to the check's details, or "".
	URL string
	// Required is nil unless the provider says, without an extra request,
	// whether the check is required for merging. No provider does yet.
	Required *bool
}

// Checks is the result of Provider.GetChecks.
type Checks struct {
	// Items are in the host's order (GitHub: the check runs first, then the
	// legacy statuses whose name no check run has).
	Items []Check
	// Truncated is true when the host had more checks than were read
	// (MaxChecks, or a page cap).
	Truncated bool
	// Partial is true when only some of the provider's check lists could be
	// read (GitHub: a token that may read the check runs but not the commit
	// statuses, or the reverse); Items then hold the lists that were read.
	Partial bool
	// Strategy says which endpoint answered, like PullRequest.BaseStrategy;
	// it is one of the Checks* constants.
	Strategy string
}

// DirEntry is one direct entry of a directory. Dir is true for a
// subdirectory; a file, a symbolic link and a submodule are not
// directories.
type DirEntry struct {
	Name string
	Dir  bool
}

// DirListing is the result of Provider.ListDir.
type DirListing struct {
	// Entries are sorted by name.
	Entries []DirEntry
	// Truncated is true when the directory had more than MaxDirEntries
	// entries; Entries then holds the first MaxDirEntries the host listed.
	Truncated bool
}

// NewCheck builds a Check from a provider's raw values: the name is cleaned
// by CleanCheckName and the URL by CleanCheckURL.
func NewCheck(name string, state CheckState, rawURL string) Check {
	return Check{Name: CleanCheckName(name), State: state, URL: CleanCheckURL(rawURL)}
}

// CleanCheckName makes an untrusted check name safe to show: invalid UTF-8
// is replaced, control characters and bidirectional formatting characters
// are removed, surrounding space is trimmed, and the result is cut to
// MaxCheckNameRunes runes.
func CleanCheckName(s string) string {
	s = strings.ToValidUTF8(s, "\ufffd")
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || isBidiControl(r) {
			return -1
		}
		return r
	}, s)
	s = strings.TrimSpace(s)
	if utf8.RuneCountInString(s) > MaxCheckNameRunes {
		s = string([]rune(s)[:MaxCheckNameRunes])
	}
	return s
}

// isBidiControl reports whether r is a bidirectional formatting character
// (embedding, override, isolate or mark), which can make text display in an
// order other than the one it is read in.
func isBidiControl(r rune) bool {
	return r == '\u200e' || r == '\u200f' || r == '\u061c' ||
		(r >= '\u202a' && r <= '\u202e') || (r >= '\u2066' && r <= '\u2069')
}

// CleanCheckURL returns s when it is an absolute http or https URL with a
// host, no user info, no control character or space, and at most 2048
// bytes; otherwise "".
func CleanCheckURL(s string) string {
	if s == "" || len(s) > maxCheckURLBytes {
		return ""
	}
	for _, r := range s {
		if r <= ' ' || r == 0x7f || unicode.IsControl(r) || unicode.IsSpace(r) || isBidiControl(r) {
			return ""
		}
	}
	u, err := url.Parse(s)
	if err != nil || u.User != nil || u.Host == "" || (!strings.EqualFold(u.Scheme, "http") && !strings.EqualFold(u.Scheme, "https")) {
		return ""
	}
	return s
}

// ValidRevision reports whether rev can name a revision in a request path
// or query: non-empty, at most 255 bytes, not "." or "..", and without
// control characters. Callers still escape it.
func ValidRevision(rev string) bool {
	if rev == "" || len(rev) > 255 || rev == "." || rev == ".." {
		return false
	}
	return !strings.ContainsFunc(rev, unicode.IsControl)
}

// FinishDirListing sorts entries by name and cuts them to MaxDirEntries,
// setting Truncated when entries were dropped or truncated is already true
// (the host said there were more). Providers build every DirListing with
// it.
func FinishDirListing(entries []DirEntry, truncated bool) *DirListing {
	if len(entries) > MaxDirEntries {
		entries, truncated = entries[:MaxDirEntries], true
	}
	sorted := slices.Clone(entries)
	slices.SortStableFunc(sorted, func(a, b DirEntry) int { return strings.Compare(a.Name, b.Name) })
	return &DirListing{Entries: sorted, Truncated: truncated}
}
