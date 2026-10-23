package ask

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/nevzatcirak/review-mcp/internal/mdutil"
	"github.com/nevzatcirak/review-mcp/internal/provider"
	"github.com/nevzatcirak/review-mcp/internal/related"
)

// RelatedRepositoryInstruction is the paragraph the system prompt gets when
// the placed related block holds a related repository (v2.5 spec V-13b §4
// item 1); absent otherwise, so the prompt is unchanged.
const RelatedRepositoryInstruction = "Related repositories (header line \"R<n> — repository ...\") are other " +
	"repositories at one commit, given with the identifiers and routes the pull request removes, renames or " +
	"changes, and the places where the repository uses them. When the question is about their use, answer from " +
	"those places. Cite a consumer location as `R<n>:path:line` with the path and line shown in the hits; the " +
	"citation becomes a link to that file at the commit. Say \"needs verification\" when only the name matches. " +
	"A repository whose section says no use was found shows only that text search found none, not that nothing " +
	"is affected."

// citeRE matches a consumer citation, `R<n>:path:line`, with or without the
// code span around it. The path has no space, backtick or colon. The line
// part is one line (`L`), a range (`L-M`) or a list (`L,M[,K...]`); the whole
// of it belongs to the citation, and the link goes to its first line.
var citeRE = regexp.MustCompile("`?\\bR([1-9]):([^\\s`:]+):([0-9]{1,9}(?:[-,][0-9]{1,9})*)`?")

// linkConsumers turns each citation of a consumer location in answer into a
// link to the file at the related repository's pinned commit, built by that
// repository's own provider (V-13b spec §4 item 1). A citation is linked
// only when the run saw the file in that repository (a kept hit, or a file a
// tool read: related.Item.Saw); any other citation keeps its text with
// " (location not verified)". The rest of the answer is not changed.
//
// DESIGN-QUESTION (WP-13d DQ-1): the spec says the answer "may cite consumer
// locations with links" but not who builds them. The model cannot know a
// URL, so it cites R<n>:path:line and the server links it, with the same
// seen-file rule as pr_review's findings. Recommendation: keep.
//
// DESIGN-QUESTION (WP-16 item 3): the link text of a citation with several
// lines is the location exactly as the model wrote it ("path:1,6", "path:3-9")
// after the repository name, as for a single line. Recommendation: keep.
func linkConsumers(answer string, placed []related.Section) string {
	if !hasRepository(placed) {
		return answer
	}
	return citeRE.ReplaceAllStringFunc(answer, func(m string) string {
		sm := citeRE.FindStringSubmatch(m)
		n, _ := strconv.Atoi(sm[1])
		first, _, _ := strings.Cut(sm[3], "-")
		first, _, _ = strings.Cut(first, ",")
		line, err := strconv.Atoi(first)
		if n < 1 || n > len(placed) || err != nil || line < 1 || !placed[n-1].Item.IsRepository() {
			return m
		}
		it, path := placed[n-1].Item, sm[2]
		text := path + ":" + sm[3]
		if !it.Saw(path) {
			return mdutil.CodeSpan(text) + " (location not verified)"
		}
		url := it.Provider.FileLineURL(provider.PRRef{Kind: it.Repo.Kind, Namespace: it.Repo.Namespace, Repo: it.Repo.Repo},
			&provider.PullRequest{HeadSHA: it.Checkout.HeadSHA}, path, line)
		if url == "" {
			return mdutil.CodeSpan(text)
		}
		return "[" + mdutil.CodeSpan(it.Name()+" "+text) + "](" + url + ")"
	})
}

func hasRepository(placed []related.Section) bool {
	for _, s := range placed {
		if s.Item.IsRepository() {
			return true
		}
	}
	return false
}
