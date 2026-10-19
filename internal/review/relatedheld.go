package review

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"strconv"
	"strings"

	"github.com/nevzatcirak/review-mcp/internal/provider"
	"github.com/nevzatcirak/review-mcp/internal/related"
)

// The related record (X-41, v2.3 spec V-13a §2.3): the overview records the
// related pull requests it was made with, by URL and head revision, in one
// hidden line of its trailer, so that a later incremental review can tell
// whether any of them changed:
//
//	[//]: # (review-mcp:related:<base64url of {"v":1,"items":[{"u":url,"h":head}]}, no padding>)
//
// It follows the rules of the held lines: written only with a reviewed
// commit and when at least one related pull request was read, defused in
// the body, and read from the trailer of review-mcp's own overview only. It
// sits after the issue verdict line and before the held lines.
//
// From v2.5 (spec V-13b §2.5) it also records the related repositories, by
// URL and branch, never by commit: {"k":"repo","u":url,"b":branch or "-"}.
// A record of pull requests only is written and read exactly as before.
const (
	relatedLinePrefix = "[//]: # (review-mcp:related:"
	relatedLineSuffix = ")"
	// maxRelatedLineBytes caps the line (spec §2.3 item 2).
	maxRelatedLineBytes  = 10000
	relatedRecordVersion = 1
)

// NoteRelatedChanged: an incremental run whose related pull requests
// differ from the last review's record (spec §2.3 item 3, verbatim).
const NoteRelatedChanged = "A related pull request changed since the last review, so the whole pull request was reviewed."

// relatedRecord is the JSON of the related line.
type relatedRecord struct {
	V     int                 `json:"v"`
	Items []relatedRecordItem `json:"items"`
}

type relatedRecordItem struct {
	// K is recordRepo for a related repository, absent for a pull request.
	K string `json:"k,omitempty"`
	U string `json:"u"`
	// H is a pull request's head revision; absent for a repository.
	H string `json:"h,omitempty"`
	// B is a repository's branch, recordDefaultBranch for its default
	// branch; absent for a pull request.
	B string `json:"b,omitempty"`
}

// The repository entries of the record (V-13b §2.5).
const (
	recordRepo          = "repo"
	recordDefaultBranch = "-"
)

// recordBranch is a repository item's branch as the record holds it.
func recordBranch(it *related.Item) string {
	if it.Repo.Branch == "" {
		return recordDefaultBranch
	}
	return it.Repo.Branch
}

// valid reports whether a record may be read: version 1, 1 to
// related.MaxItems items, no URL twice, each a pull request (a related URL,
// validRelatedURL, with a full lower-case hexadecimal head revision) or a
// repository (a related URL that may keep its Bitbucket Server "at" query,
// and a branch: recordDefaultBranch or a valid branch name).
func (r *relatedRecord) valid() bool {
	if r.V != relatedRecordVersion || len(r.Items) == 0 || len(r.Items) > related.MaxItems {
		return false
	}
	seen := map[string]bool{}
	for _, it := range r.Items {
		switch {
		case seen[it.U]:
			return false
		case it.K == "":
			if !validRelatedURL(it.U, false) || !isReviewedSHA(it.H) || it.B != "" {
				return false
			}
		case it.K == recordRepo:
			if !validRepoURL(it.U) || it.H != "" || (it.B != recordDefaultBranch && !provider.ValidBranchName(it.B)) {
				return false
			}
		default:
			return false
		}
		seen[it.U] = true
	}
	return true
}

// validRepoURL reports whether u is a repository URL a record may hold: as
// related.Validate writes it (related.Normalize, with the "at" query of a
// Bitbucket Server repository URL kept).
func validRepoURL(u string) bool {
	n, ok := related.Normalize(u)
	return ok && (n == u || strings.HasPrefix(u, n+"?at=")) && len(u) <= related.MaxURLBytes
}

// recordedRepositories are the repository items of items that belong to the
// related set the record keeps (V-13b §2.5): every repository item that is
// not ignored, whatever happened when it was read. A repository is recorded
// by URL and branch; whether it could be fetched or searched now does not
// change the set.
func recordedRepositories(items []*related.Item) []*related.Item {
	var out []*related.Item
	for _, it := range items {
		if it.IsRepository() && it.Status != related.StatusIgnored {
			out = append(out, it)
		}
	}
	return out
}

// validRelatedURL reports whether u is a URL a record may hold: as
// related.Normalize writes it, at most related.MaxURLBytes; "" only when
// empty is allowed.
func validRelatedURL(u string, empty bool) bool {
	if u == "" {
		return empty
	}
	n, ok := related.Normalize(u)
	return ok && n == u
}

// relatedRecordLine returns the related line of the run: the read related
// pull requests, in argument order, with their head revisions, then the
// recorded repositories (recordedRepositories) with their branches; "" when
// there is none, or when the line would be longer than maxRelatedLineBytes.
//
// DESIGN-QUESTION (WP-11b DQ-1): the spec limits the line to 10,000 bytes
// without saying what gives way. Four URLs of up to 2,048 bytes can exceed
// it; the line is then not written, so the next incremental run finds no
// record for its related pull requests and reviews in full (a doubt is a
// change). Recommendation: keep; dropping some items would record a set the
// run did not use.
func (pl *Plan) relatedRecordLine() string {
	var rec relatedRecord
	rec.V = relatedRecordVersion
	for _, it := range related.ReadItems(pl.related) {
		rec.Items = append(rec.Items, relatedRecordItem{U: it.URL, H: it.PR.HeadSHA})
	}
	for _, it := range recordedRepositories(pl.related) {
		rec.Items = append(rec.Items, relatedRecordItem{K: recordRepo, U: it.URL, B: recordBranch(it)})
	}
	if len(rec.Items) == 0 || !rec.valid() {
		return ""
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if enc.Encode(rec) != nil {
		return ""
	}
	line := relatedLinePrefix + base64.RawURLEncoding.EncodeToString(bytes.TrimSuffix(buf.Bytes(), []byte("\n"))) +
		relatedLineSuffix
	if len(line) > maxRelatedLineBytes {
		return ""
	}
	return line
}

// relatedPayload returns the payload of a line that is exactly a related
// line, else "".
func relatedPayload(line string) string {
	p, ok := strings.CutPrefix(line, relatedLinePrefix)
	if !ok {
		return ""
	}
	if p, ok = strings.CutSuffix(p, relatedLineSuffix); !ok || p == "" || len(line) > maxRelatedLineBytes {
		return ""
	}
	for i := 0; i < len(p); i++ {
		c := p[i]
		if (c < 'A' || c > 'Z') && (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' && c != '_' {
			return ""
		}
	}
	return p
}

// relatedRecordOf returns the related record of an overview body: the line
// that is exactly a related line directly above the run of held lines (or
// the reviewed-commit line) of the trailer, when its record is valid; nil
// otherwise. A related-looking line anywhere else is never read. Call it
// only on a body of our own overview.
func relatedRecordOf(body string) *relatedRecord {
	lines := strings.Split(strings.TrimRight(body, " \t\r\n"), "\n")
	for i := range lines {
		lines[i] = strings.TrimSuffix(lines[i], "\r")
	}
	n := len(lines)
	if n < 3 || lines[n-1] != OverviewMarker || !strings.HasPrefix(lines[n-2], reviewedLinePrefix) {
		return nil
	}
	at := n - 3
	for at >= 0 && heldPayload(lines[at]) != "" {
		at--
	}
	if at < 0 {
		return nil
	}
	p := relatedPayload(lines[at])
	if p == "" {
		return nil
	}
	raw, err := base64.RawURLEncoding.Strict().DecodeString(p)
	if err != nil {
		return nil
	}
	var rec relatedRecord
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if dec.Decode(&rec) != nil || !rec.valid() {
		return nil
	}
	return &rec
}

// relatedChanged reports whether the run's related items differ from the
// last review's record (spec V-13a §2.3 item 3, V-13b §2.5): an item that
// is not a repository could not be read now; the record is missing while
// the run has related items, or present while it has none; the sets of
// pull request URLs (in any order) or any head revision differ; or the sets
// of repositories by URL and branch differ. A repository's commit is not
// compared: a default branch moves for reasons unrelated to the pull
// request, and an incremental run searches it again at its new commit.
// Ignored items (the primary itself, duplicates) do not count.
//
// DESIGN-QUESTION (WP-13b DQ-1): V-13a makes "a related item that could not
// be read" a change, because a pull request's head is then unknown (a doubt
// is a change). A repository is recorded by URL and branch only, which are
// known whether or not it can be fetched now, so a failed repository item
// (repo_context_off here; a fetch or search failure happens later in the run
// and cannot be seen here at all) is part of the set and is no change by
// itself. An item whose URL could not be resolved has no kind and stays a
// change, as in V-13a. Recommendation: keep.
func (pl *Plan) relatedChanged() bool {
	for _, it := range pl.related {
		if it.Status == related.StatusFailed && !it.IsRepository() {
			return true
		}
	}
	read := related.ReadItems(pl.related)
	repos := recordedRepositories(pl.related)
	rec := pl.lastRelated
	if rec == nil {
		return len(read) > 0 || len(repos) > 0
	}
	heads, branches := map[string]string{}, map[string]string{}
	for _, it := range rec.Items {
		if it.K == recordRepo {
			branches[it.U] = it.B
		} else {
			heads[it.U] = it.H
		}
	}
	if len(read) != len(heads) || len(repos) != len(branches) {
		return true
	}
	for _, it := range read {
		if h, ok := heads[it.URL]; !ok || h != it.PR.HeadSHA {
			return true
		}
	}
	for _, it := range repos {
		if b, ok := branches[it.URL]; !ok || b != recordBranch(it) {
			return true
		}
	}
	return false
}

// carryRelated returns the carried finding of a held record located in a
// related pull request (h.R): only when the run's related set equals the
// recorded one and h.R is one of its read items; its link then comes from
// that pull request's provider, when the file is one of its changed files.
func (pl *Plan) carryRelated(h heldRecord) (KeyIssue, bool) {
	if pl.lastRelated == nil || pl.relatedChanged() {
		return KeyIssue{}, false
	}
	for _, it := range related.ReadItems(pl.related) {
		if it.URL != h.R {
			continue
		}
		ki := KeyIssue{RelevantFile: h.F, StartLine: h.S, EndLine: h.E, IssueHeader: h.H, IssueContent: h.C,
			CarriedFrom: h.From, Related: it.URL, RelatedName: it.Ref.Repo + "#" + strconv.FormatInt(it.Ref.Number, 10)}
		for i := range it.Diff.Files {
			if it.Diff.Files[i].Path == h.F {
				ki.Link = it.Provider.FileLineURL(it.Ref, it.PR, h.F, h.S)
				break
			}
		}
		return ki, true
	}
	return KeyIssue{}, false
}
