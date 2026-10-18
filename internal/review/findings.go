package review

import (
	"context"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/nevzatcirak/review-mcp/internal/llmrun"
	"github.com/nevzatcirak/review-mcp/internal/prompt"
	"github.com/nevzatcirak/review-mcp/internal/provider"
	"github.com/nevzatcirak/review-mcp/internal/repoctx"
)

// SuppliedFinding is one item of the findings argument (X-33): a finding
// in the shape of key_issues_to_review that a client merged from focused
// runs, published without a model call.
type SuppliedFinding struct {
	RelevantFile string
	StartLine    int
	EndLine      int
	IssueHeader  string
	IssueContent string
	// Focus is the focus of the run the finding came from; "" when not
	// given. It is validated like the focus argument.
	Focus string
}

// NoteClientFindings: the findings of a merged publish came from the
// client, and no model was called (X-33).
const NoteClientFindings = "These findings were supplied by the client (merged from focused reviews); " +
	"no model was called for this publish."

// noteSuppliedDropped: supplied findings that failed validation.
func noteSuppliedDropped(n int) string {
	return countPhrase(n, "supplied finding was", "supplied findings were") + " dropped: a finding needs a " +
		"changed file of the diff, positive lines that do not end before they start, a header and a description " +
		"that are neither empty nor too long, and a valid focus when it has one."
}

// noteSuppliedMerged: supplied findings that repeated an earlier one.
func noteSuppliedMerged(n int) string {
	return countPhrase(n, "supplied finding repeated", "supplied findings repeated") +
		" an earlier one at the same place with the same header and " + wasWere(n) + " merged into it."
}

// noteHeldDropped: findings the found overview listed only there (held
// lines) that a merged publish does not carry, because the client's set
// replaces the overview's findings.
func noteHeldDropped(n int) string {
	return countPhrase(n, "finding listed only in the earlier overview was",
		"findings listed only in the earlier overview were") + " not carried into this publish."
}

// supplyFindings builds the review of a merged publish (X-33) from
// args.Findings, against the files of the prepared plan's diff, and
// returns it with its notes. In order:
//
//   - validation: the file must be one of the diff's files, matched
//     exactly by its (new) path as a model finding's file is; start_line
//     positive and end_line at least start_line; header and content not
//     empty after trimming and within MaxSuppliedHeaderRunes and
//     MaxSuppliedContentRunes; the focus, when given, valid. An invalid
//     item is dropped and counted (noteSuppliedDropped).
//   - exact repeats (the same file and lines, and the same header after the
//     fingerprint's normalization: case and runs of white space) are merged
//     into the first, which is kept as given (noteSuppliedMerged).
//   - the list is capped at the call's effective review.max_total_findings,
//     as the merge of a review in parts is (noteTotalCap).
//
// The optional fields a model fills (effort, tests) stay absent: nothing
// is invented for them. The two concerns texts are converted as a model's
// answer is (convertConcerns: "No" or the trimmed text), only when the
// configuration asks for the field and the client sent a text.
func (pl *Plan) supplyFindings(args Args) (*Review, []string) {
	files := map[string]bool{}
	for i := range pl.d.Files {
		files[pl.d.Files[i].Path] = true
	}
	rev := &Review{KeyIssuesToReview: []KeyIssue{}}
	seen := map[string]bool{}
	dropped, merged := 0, 0
	for _, f := range args.Findings {
		ki, ok := suppliedIssue(f, files)
		if !ok {
			dropped++
			continue
		}
		key := ki.RelevantFile + "\x00" + strconv.Itoa(ki.StartLine) + "\x00" + strconv.Itoa(ki.EndLine) + "\x00" +
			normalizedHeader(ki.IssueHeader)
		if seen[key] {
			merged++
			continue
		}
		seen[key] = true
		rev.KeyIssuesToReview = append(rev.KeyIssuesToReview, ki)
	}
	notes := []string{NoteClientFindings}
	if dropped > 0 {
		notes = append(notes, noteSuppliedDropped(dropped))
	}
	if merged > 0 {
		notes = append(notes, noteSuppliedMerged(merged))
	}
	if extra := len(rev.KeyIssuesToReview) - pl.maxTotal; pl.maxTotal > 0 && extra > 0 {
		rev.KeyIssuesToReview = rev.KeyIssuesToReview[:pl.maxTotal]
		notes = append(notes, noteTotalCap(extra))
	}
	if pl.toggles.Security && strings.TrimSpace(args.SecurityConcerns) != "" {
		convertConcerns(args.SecurityConcerns, &rev.SecurityConcerns, SecurityNo)
	}
	if pl.toggles.Performance && strings.TrimSpace(args.PerformanceConcerns) != "" {
		convertConcerns(args.PerformanceConcerns, &rev.PerformanceConcerns, PerformanceNo)
	}
	pl.log.Debug("review: supplied findings", "supplied", len(args.Findings), "dropped", dropped, "merged", merged,
		"kept", len(rev.KeyIssuesToReview), "max_total_findings", pl.maxTotal)
	return rev, notes
}

// suppliedIssue validates one supplied finding against the diff's files
// and converts it; ok is false for an invalid one.
func suppliedIssue(f SuppliedFinding, files map[string]bool) (KeyIssue, bool) {
	ki := KeyIssue{
		RelevantFile: prompt.PyStrip(f.RelevantFile),
		IssueHeader:  prompt.PyStrip(f.IssueHeader),
		IssueContent: prompt.PyStrip(f.IssueContent),
		StartLine:    f.StartLine,
		EndLine:      f.EndLine,
	}
	focus, err := NormalizeFocus(f.Focus)
	switch {
	case err != nil,
		!files[ki.RelevantFile],
		ki.StartLine <= 0 || ki.EndLine < ki.StartLine,
		ki.IssueHeader == "" || utf8.RuneCountInString(ki.IssueHeader) > MaxSuppliedHeaderRunes,
		ki.IssueContent == "" || utf8.RuneCountInString(ki.IssueContent) > MaxSuppliedContentRunes:
		return KeyIssue{}, false
	}
	ki.Focus = focus
	return ki, true
}

// normalizedHeader is a header as the fingerprint compares it (llmrun's
// normalization): lower case, runs of white space as one space.
func normalizedHeader(s string) string {
	return strings.Join(strings.Fields(strings.ToLower(s)), " ")
}

// publishSupplied finishes a merged publish (X-33) from a prepared plan:
// the supplied findings take the place of the model's answer, then the
// steps after the model call run as for a model's review (snippets, links,
// the publish with its X-29 duplicate check). No model is called. The
// coverage is the one Prepare computed from the diff; it records no model
// call, no project context (none was read) and repository context as
// skipped.
func (pl *Plan) publishSupplied(ctx context.Context, deps Deps, args Args) *Result {
	res := pl.Result
	res.Coverage.ModelCalls = 0
	res.Coverage.Guidelines, res.Coverage.FileTree = nil, nil
	if deps.Config.Context.Repo.Enabled {
		res.Coverage.RepoContext = llmrun.RepoContext{Status: llmrun.RepoSkipped, Reason: repoctx.ReasonClientFindings}
	}
	res.Metadata.RequestTokens = 0
	// No model was shown the discussion in this run.
	res.Metadata.AlreadyDiscussed = 0
	rev, notes := pl.supplyFindings(args)
	res.Notes = append(notes, res.Notes...)
	pl.attachSnippets(rev)
	res.Review = rev
	publish(ctx, deps, args, pl)
	return res
}

// attachSnippets runs steps 10 and 11 on rev: each finding's verified
// snippet and its link, against every file of the PR.
func (pl *Plan) attachSnippets(rev *Review) {
	ref, p, pr, d := pl.ref, pl.p, pl.pr, pl.d
	files := map[string]*provider.FilePatch{}
	for i := range d.Files {
		files[d.Files[i].Path] = &d.Files[i]
	}
	unverified := 0
	unknownRelated := false
	for i := range rev.KeyIssuesToReview {
		ki := &rev.KeyIssuesToReview[i]
		// A finding located in a related pull request (X-41) takes its
		// snippet and link from there.
		if pl.attachRelated(ki, files, &unknownRelated) {
			continue
		}
		// DESIGN-QUESTION: how is relevant_file matched to a changed file? —
		// chose an exact match (after trimming) against the paths of the
		// PR's reviewable files, because any normalization (a "./" or "a/"
		// prefix, a basename) could pick the wrong file and show unrelated
		// lines; an unmatched file keeps the finding without a snippet or link.
		fp := files[ki.RelevantFile]
		ki.Snippet, ki.SnippetNote = snippet(fp, ki.StartLine, ki.EndLine)
		if ki.SnippetNote == SnippetNoteUnverified {
			unverified++
		}
		// The path is model-authored: link only a file the PR contains. A
		// hallucinated path (for example one with ".." segments, which
		// browsers resolve to another location on the same host) keeps its
		// file and line text but gets no link.
		if fp != nil {
			ki.Link = p.FileLineURL(ref, pr, ki.RelevantFile, ki.StartLine)
		}
	}
	if unknownRelated && pl.Result != nil {
		pl.Result.Notes = append(pl.Result.Notes, NoteRelatedUnknown)
	}
	pl.log.Debug("review: snippets", "findings", len(rev.KeyIssuesToReview), "unverified_snippets", unverified)
}
