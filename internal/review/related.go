package review

import (
	"strconv"
	"strings"

	"github.com/nevzatcirak/review-mcp/internal/config"
	"github.com/nevzatcirak/review-mcp/internal/diffpipe"
	"github.com/nevzatcirak/review-mcp/internal/llmrun"
	"github.com/nevzatcirak/review-mcp/internal/provider"
	"github.com/nevzatcirak/review-mcp/internal/related"
	"github.com/nevzatcirak/review-mcp/internal/tokens"
)

// Related pull requests in the prompt of pr_review (X-41, v2.3 spec V-13a
// §1.4): the block is built and placed by internal/related; here are the
// review's own parts: the numbered diff, the instruction and the field of
// cross-repository findings, and the mapping of findings to related pull
// requests.

// RelatedHeader is the fixed sentence above the fenced related block.
const RelatedHeader = related.Header

// ReasonNothingToReview is the not_placed reason of a run with nothing to
// review (WP-11a DQ-5).
const ReasonNothingToReview = related.ReasonNothingToReview

// KeyRelated is the optional key-issue field of a run with a related block
// (X-41, v2.3 spec V-13a §2.1 item 2): R1 to R4 when the finding's file is
// in that related pull request.
const KeyRelated = "related"

// relatedKeyIssueField is its line in the KeyIssuesComponentLink class of
// the system prompt's schema; the example answer does not change.
const relatedKeyIssueField = "\n    " + KeyRelated + ": Optional[str] = Field(description=\"R1 to R4 when relevant_file is " +
	"in that related pull request; omit for the reviewed pull request\")"

// RelatedInstruction is the system prompt's instruction block of a run with
// a placed related block (spec §2.1 item 1); absent otherwise.
const RelatedInstruction = "Cross-repository review: the user prompt carries related pull requests (R1 to R4) next to " +
	"the reviewed one. Also look for problems between the reviewed pull request and the related ones:\n" +
	"- a field, route, status code, message or schema changed on one side and not on the other;\n" +
	"- compatibility while the changes roll out one after the other;\n" +
	"- the order in which the pull requests must be merged and deployed.\n" +
	"When such a problem is located in a related pull request, set related to its R number (R1 to R4), and " +
	"relevant_file, start_line and end_line to its place in that pull request's diff; otherwise omit related."

// markRelated sets the status of every read item that has none yet.
func (pl *Plan) markRelated(status, reason string) { related.Mark(pl.related, status, reason) }

// placeRelated tries the related block in the room the primary diff leaves
// (related.Place), in numbered mode so that a finding can name real lines.
// It is accepted only when the primary diff is not trimmed. It returns the
// prompt input and the fit to use.
func (pl *Plan) placeRelated(in PromptInput, fit *fitted, diff string, b tokens.Budget, cfg *config.Config) (PromptInput, *fitted, error) {
	cand, cfit := in, fit
	placed, err := related.Place(pl.related, cfg.Related.MaxTokens, diffpipe.ModeNumbered, cfg, pl.flt, fit.keptLines < 0,
		func(block string) (bool, error) {
			c := in
			c.Related, c.Toggles.Related = block, true
			f, err := fitPrompts(c, diff, b)
			if err != nil {
				return false, err
			}
			cand, cfit = c, f
			return f.keptLines < 0, nil
		})
	if err != nil || placed == nil {
		return in, fit, err
	}
	pl.toggles.Related = true
	pl.relatedShown = placed
	return cand, cfit, nil
}

// relatedCoverage is coverage.related of the run.
func (pl *Plan) relatedCoverage() []llmrun.RelatedItem {
	return related.Coverage(pl.related, pl.relatedShown)
}

// reportRelated sets coverage.related and adds the related notes, once per
// run.
func (pl *Plan) reportRelated(res *Result) {
	res.Coverage.Related = pl.relatedCoverage()
	if pl.relatedReported {
		return
	}
	pl.relatedReported = true
	res.Notes = append(res.Notes, related.Notes(pl.related, pl.relatedShown)...)
}

// NoteRelatedUnknown: a finding named a related pull request that was not
// in the review (spec §2.2 item 3, verbatim); it was kept as a finding on
// the reviewed pull request. Added once per run.
const NoteRelatedUnknown = "The model named a related pull request that was not in the review; the finding is treated as one on this pull request."

// relatedOf returns the placed related pull request a finding's related
// value names ("R<n>": the n-th placed item, R1 to R4); ok is false for any
// other value, or when no block was placed.
func (pl *Plan) relatedOf(tag string) (related.Section, bool) {
	n, err := strconv.Atoi(strings.TrimPrefix(tag, "R"))
	if !strings.HasPrefix(tag, "R") || err != nil || n < 1 || n > len(pl.relatedShown) {
		return related.Section{}, false
	}
	return pl.relatedShown[n-1], true
}

// attachRelated places a finding in the related pull request its related
// value names (spec §2.2 item 2): Related and RelatedName are set; the
// Link comes from that pull request's own provider, only when the file is
// one of its changed files (shown or listed); the Snippet from its diff,
// only when the file was shown. It reports whether the finding is related;
// an unknown value (not a placed item) is dropped, and unknown is set.
//
// The file decides, not the tag (spec §5 item 1): a file of the reviewed
// pull request's diff (primary) that the related pull request does not
// change makes a finding on the reviewed pull request, tag dropped, no
// note. The model reads "related" as "relates to" (acceptance on 2.3.0-rc.1).
func (pl *Plan) attachRelated(ki *KeyIssue, primary map[string]*provider.FilePatch, unknown *bool) bool {
	tag := ki.relatedTag
	ki.relatedTag = ""
	if tag == "" {
		return false
	}
	rs, ok := pl.relatedOf(tag)
	if !ok {
		*unknown = true
		return false
	}
	it := rs.Item
	if primary[ki.RelevantFile] != nil && !changes(it, ki.RelevantFile) {
		return false
	}
	ki.Related = it.URL
	ki.RelatedName = it.Ref.Repo + "#" + strconv.FormatInt(it.Ref.Number, 10)
	ki.Snippet, ki.SnippetNote, ki.Link = "", "", ""
	for i := range it.Diff.Files {
		fp := &it.Diff.Files[i]
		if fp.Path != ki.RelevantFile {
			continue
		}
		ki.Link = it.Provider.FileLineURL(it.Ref, it.PR, fp.Path, ki.StartLine)
		if rs.Shown[fp.Path] {
			ki.Snippet, ki.SnippetNote = snippet(fp, ki.StartLine, ki.EndLine)
		}
		break
	}
	return true
}

// changes reports whether the related pull request changes the file
// (shown or listed).
func changes(it *related.Item, path string) bool {
	for i := range it.Diff.Files {
		if it.Diff.Files[i].Path == path {
			return true
		}
	}
	return false
}
