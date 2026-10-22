package ask

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/nevzatcirak/review-mcp/internal/config"
	"github.com/nevzatcirak/review-mcp/internal/provider"
	"github.com/nevzatcirak/review-mcp/internal/related"
)

// linkProvider is relProvider with a FileLineURL that puts the commit in
// the link.
type linkProvider struct{ *relProvider }

func (linkProvider) FileLineURL(ref provider.PRRef, pr *provider.PullRequest, path string, line int) string {
	return fmt.Sprintf("https://gitlab.example/%s/%s/blob/%s/%s#L%d", ref.Namespace, ref.Repo, pr.HeadSHA, path, line)
}

type linkResolver struct{ askRepoResolver }

func (r linkResolver) ResolveItem(u string) (provider.ItemKind, provider.PRRef, provider.RepoRef, provider.Provider, error) {
	k, ref, repo, p, err := r.askRepoResolver.ResolveItem(u)
	if err == nil && k == provider.ItemRepository {
		p = linkProvider{r.rel}
	}
	return k, ref, repo, p, err
}

// consumerHarness is a pr_ask run with a related pull request (R1) and the
// consumer repository (R2), whose search finds src/client/profile.ts; the
// OpenAPI patch removes userName and the common names name and type.
func consumerHarness(t *testing.T, answer string) (*harness, *Result) {
	t.Helper()
	h, tr, _ := toolHarness(0)
	h.llm.answers = []string{answer}
	h.prov.files = append(h.prov.files, provider.FilePatch{Path: "api/openapi.yaml", Type: provider.ChangeModified,
		Patch: "@@ -10,5 +10,3 @@ components:\n       properties:\n-        userName:\n-        name:\n-        type: string\n" +
			"+        displayName:\n           id:\n",
		BaseStatus: provider.ContentNotFetchedSizeCap, HeadStatus: provider.ContentNotFetchedSizeCap})
	h.deps.Resolver = linkResolver{askRepoResolver{relResolver{primary: h.prov, rel: &relProvider{files: []provider.FilePatch{
		addedPatch("proto/user.proto", "PROTO-MARKER", 4)}}}}}
	h.deps.Config.GitLab.BaseURL, h.deps.Config.Secrets.GitLabToken = "https://gitlab.example", config.NewSecret("gitlab-token")
	h.deps.RepoContext = &askConsumer{toolRepo: tr}
	args := h.args()
	args.Related = []string{relatedURL, askConsumerURL}
	res, err := Run(context.Background(), h.deps, args)
	if err != nil {
		t.Fatal(err)
	}
	return h, res
}

// TestAskRelatedRepositoryInstruction: a placed related repository adds the
// instruction paragraph to the system prompt and the section names the
// stop-listed removals; a run with only a related pull request has neither
// (the prompt is unchanged).
func TestAskRelatedRepositoryInstruction(t *testing.T) {
	h, res := consumerHarness(t, "ok")
	if e := res.Coverage.Related[1]; e.Kind != "repository" || e.Status != "full" {
		t.Fatalf("the repository item %+v", e)
	}
	call := h.llm.calls[0]
	if !strings.Contains(call.system, RelatedRepositoryInstruction) {
		t.Errorf("the system prompt lacks the repository instruction:\n%s", call.system)
	}
	if !strings.Contains(call.user, "\nRemoved or renamed: userName\n"+related.StoppedSentence+"name, type, string\n") {
		t.Errorf("the section does not name the stop-listed removals:\n%s", call.user)
	}

	h2 := relatedHarness()
	args := h2.args()
	args.Related = []string{relatedURL}
	if _, err := Run(context.Background(), h2.deps, args); err != nil {
		t.Fatal(err)
	}
	if c := h2.llm.calls[0]; strings.Contains(c.system, "Related repositories") || strings.Contains(c.user, related.StoppedSentence) {
		t.Error("a run without a repository carries the repository text")
	}
}

// TestAskConsumerCitationsAreLinked: a citation of a seen file becomes a
// link to the file at the pinned commit; one of a file the run never saw
// keeps its text and is marked; a label that is no repository, or out of
// range, and every other word of the answer are left alone.
func TestAskConsumerCitationsAreLinked(t *testing.T) {
	answer := "The client reads it at `R2:src/client/profile.ts:2`; also R2:src/other.ts:9, R1:proto/user.proto:1 and R9:x.go:1."
	_, res := consumerHarness(t, answer)
	link := "https://gitlab.example/grp/web-ui/blob/" + askConsumerCommit + "/src/client/profile.ts#L2"
	want := "The client reads it at [`grp/web-ui src/client/profile.ts:2`](" + link + "); also " +
		"`src/other.ts:9` (location not verified), R1:proto/user.proto:1 and R9:x.go:1."
	if res.Answer != want {
		t.Errorf("answer\n%s\nwant\n%s", res.Answer, want)
	}
}

// TestLinkConsumersWithoutRepository: with no repository section the answer
// is returned as it is.
func TestLinkConsumersWithoutRepository(t *testing.T) {
	if got := linkConsumers("see R2:a.go:1", nil); got != "see R2:a.go:1" {
		t.Errorf("answer changed: %q", got)
	}
}

// TestAskConsumerCitationsWithSeveralLines: `L-M` and `L,M[,K...]` are one
// citation: one link to line L, the whole location as the link text, and
// nothing of it left outside the link; an unseen file keeps its whole text.
func TestAskConsumerCitationsWithSeveralLines(t *testing.T) {
	answer := "A R2:src/client/profile.ts:2-5, B `R2:src/client/profile.ts:1,6`; C R2:src/client/profile.ts:3,7,9. " +
		"D R2:src/other.ts:4-8 and E R2:src/other.ts:1,6, F R2:src/client/profile.ts:2."
	_, res := consumerHarness(t, answer)
	base := "https://gitlab.example/grp/web-ui/blob/" + askConsumerCommit + "/src/client/profile.ts#L"
	want := "A [`grp/web-ui src/client/profile.ts:2-5`](" + base + "2), B [`grp/web-ui src/client/profile.ts:1,6`](" + base + "1); " +
		"C [`grp/web-ui src/client/profile.ts:3,7,9`](" + base + "3). " +
		"D `src/other.ts:4-8` (location not verified) and E `src/other.ts:1,6` (location not verified), " +
		"F [`grp/web-ui src/client/profile.ts:2`](" + base + "2)."
	if res.Answer != want {
		t.Errorf("answer\n%s\nwant\n%s", res.Answer, want)
	}
}
