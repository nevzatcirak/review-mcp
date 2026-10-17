package ask

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/nevzatcirak/review-mcp/internal/provider"
	"github.com/nevzatcirak/review-mcp/internal/related"
)

const relatedURL = "https://gitlab.example/grp/svc/-/merge_requests/3"

// relProvider serves the related merge request.
type relProvider struct {
	provider.Provider
	files []provider.FilePatch
}

func (r *relProvider) GetPullRequest(context.Context, provider.PRRef) (*provider.PullRequest, error) {
	return &provider.PullRequest{State: "open", TargetBranch: "main", HeadSHA: "1234567890abcdef1234567890abcdef12345678"}, nil
}

func (r *relProvider) GetDiff(context.Context, provider.PRRef, *provider.PullRequest, provider.DiffOptions) (*provider.Diff, error) {
	return &provider.Diff{Files: r.files}, nil
}

type relResolver struct {
	primary *fakeProvider
	rel     *relProvider
}

func (r relResolver) Resolve(u string) (provider.PRRef, provider.Provider, error) {
	if u == relatedURL {
		return provider.PRRef{Kind: provider.KindGitLab, Namespace: "grp", Repo: "svc", Number: 3, URL: u}, r.rel, nil
	}
	return provider.PRRef{Kind: provider.KindGitea, Namespace: "octo", Repo: "demo", Number: 7, URL: u}, r.primary, nil
}

func addedPatch(path, marker string, n int) provider.FilePatch {
	var b strings.Builder
	fmt.Fprintf(&b, "@@ -0,0 +1,%d @@\n", n)
	for i := range n {
		fmt.Fprintf(&b, "+%s line %d of a file that takes room in the prompt\n", marker, i)
	}
	return provider.FilePatch{Path: path, Type: provider.ChangeAdded, Patch: b.String(),
		BaseStatus: provider.ContentNotApplicable, HeadStatus: provider.ContentNotFetchedSizeCap}
}

func relatedHarness(files ...provider.FilePatch) *harness {
	h := newHarness("The change matches. " + answerMarker)
	if files == nil {
		files = []provider.FilePatch{addedPatch("proto/user.proto", "PROTO-MARKER", 4)}
	}
	h.deps.Resolver = relResolver{primary: h.prov, rel: &relProvider{files: files}}
	return h
}

// TestAskRelated: pr_ask reads the related pull request and gives it to the
// model fenced under the fixed sentence, before the diff, in plain mode;
// coverage.related accounts for every item and the notes say what happened.
func TestAskRelated(t *testing.T) {
	h := relatedHarness()
	args := h.args()
	args.Related = []string{relatedURL, testPRURL}
	res, err := Run(context.Background(), h.deps, args)
	if err != nil {
		t.Fatal(err)
	}
	user := h.llm.calls[0].user
	head := strings.Index(user, related.Header)
	if head < 0 || head > strings.Index(user, "The PR Git Diff:") || !strings.Contains(user[head:], "PROTO-MARKER") ||
		!strings.Contains(user[head:], "Related pull request R1: gitlab.example/grp/svc#3 — open, into main, head 1234567890ab") {
		t.Fatal("the related block is not in the prompt before the diff")
	}
	cov := res.Coverage.Related
	if len(cov) != 2 || cov[0].Status != related.StatusFull || cov[0].FilesIncluded != 1 ||
		cov[1].Status != related.StatusIgnored || cov[1].Reason != related.ReasonPrimary {
		t.Errorf("coverage.related %+v", cov)
	}
	if !slices.Contains(res.Notes, related.NoteIgnored(testPRURL)) {
		t.Errorf("notes %q", res.Notes)
	}
	h.checkNoLeaks(t)
}

// TestAskRelatedEdgeCases: without the argument coverage.related is [];
// a block that does not fit is left out with the note and the diff whole;
// with nothing to ask about it is not placed with nothing_to_review.
func TestAskRelatedEdgeCases(t *testing.T) {
	h := relatedHarness()
	res, err := Run(context.Background(), h.deps, h.args())
	if err != nil || res.Coverage.Related == nil || len(res.Coverage.Related) != 0 {
		t.Errorf("without the argument: %v %v", res.Coverage.Related, err)
	}

	var big []provider.FilePatch
	for i := range 40 {
		big = append(big, addedPatch(fmt.Sprintf("api/handler%02d.go", i), "HANDLER", 60))
	}
	h = relatedHarness(big...)
	h.deps.Config.Related.MaxTokens = 40000
	h.deps.Config.LLM.ContextWindow = 9000
	args := h.args()
	args.Related = []string{relatedURL}
	res, err = Run(context.Background(), h.deps, args)
	if err != nil {
		t.Fatal(err)
	}
	if e := res.Coverage.Related[0]; e.Status != related.StatusNotPlaced || e.Reason != related.ReasonDidNotFit ||
		!slices.Contains(res.Notes, related.NoteNotPlaced) || res.Metadata.DiffTrimmed {
		t.Errorf("did not fit: %+v, notes %q, trimmed %v", e, res.Notes, res.Metadata.DiffTrimmed)
	}

	h = relatedHarness()
	h.prov.files = nil
	res, err = Run(context.Background(), h.deps, args)
	if err != nil {
		t.Fatal(err)
	}
	if e := res.Coverage.Related[0]; e.Status != related.StatusNotPlaced || e.Reason != related.ReasonNothingToReview {
		t.Errorf("nothing to ask about: %+v", e)
	}
}
