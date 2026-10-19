package review

import (
	"context"
	"encoding/base64"
	"slices"
	"strings"
	"testing"

	"github.com/nevzatcirak/review-mcp/internal/config"
	"github.com/nevzatcirak/review-mcp/internal/gitctx"
	"github.com/nevzatcirak/review-mcp/internal/provider"
	"github.com/nevzatcirak/review-mcp/internal/provider/bitbucketserver"
	"github.com/nevzatcirak/review-mcp/internal/provider/gitea"
	"github.com/nevzatcirak/review-mcp/internal/provider/github"
	"github.com/nevzatcirak/review-mcp/internal/provider/gitlab"
	"github.com/nevzatcirak/review-mcp/internal/related"
)

// Consumer impact in the review (v2.5 spec V-13b §2.4 and §2.5): findings
// located in a related repository, and the incremental record of related
// repositories.

// TestConsumerFindingLinkPerProvider [canary 3]: a finding tagged with a
// repository item and located in a file the run saw there gets a link built
// by that item's own provider to the file at the pinned commit, never at
// the branch, for each of the four providers; it is a related finding (no
// snippet), listed in the overview only.
func TestConsumerFindingLinkPerProvider(t *testing.T) {
	const commit = "0fedcba9876543210fedcba9876543210fedcba9"
	const path = "src/client/profile.ts"
	cases := []struct {
		name string
		kind provider.Kind
		ns   string
		mk   func(*config.Config) (provider.Provider, error)
		want string
	}{
		{"github", provider.KindGitHub, "grp", func(c *config.Config) (provider.Provider, error) {
			c.GitHub.BaseURL = "https://git.example/"
			return github.NewFactory().New(c, nil)
		}, "https://git.example/grp/web-ui/blob/" + commit + "/src/client/profile.ts#L42"},
		{"gitea", provider.KindGitea, "grp", func(c *config.Config) (provider.Provider, error) {
			c.Gitea.BaseURL = "https://your-gitea.example"
			return gitea.NewFactory().New(c, nil)
		}, "https://your-gitea.example/grp/web-ui/src/commit/" + commit + "/src/client/profile.ts#L42"},
		{"gitlab", provider.KindGitLab, "grp/sub", func(c *config.Config) (provider.Provider, error) {
			c.GitLab.BaseURL = "https://gitlab.example"
			return gitlab.NewFactory().New(c, nil)
		}, "https://gitlab.example/grp/sub/web-ui/-/blob/" + commit + "/src/client/profile.ts#L42"},
		{"bitbucket_server", provider.KindBitbucketServer, "GRP", func(c *config.Config) (provider.Provider, error) {
			c.BitbucketServer.BaseURL = "https://git.example/bitbucket"
			return bitbucketserver.NewFactory().New(c, nil)
		}, "https://git.example/bitbucket/projects/GRP/repos/web-ui/browse/src/client/profile.ts?at=" + commit + "#42"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p, err := c.mk(config.Defaults())
			if err != nil {
				t.Fatal(err)
			}
			it := &related.Item{URL: "https://git.example/consumer", Kind: provider.ItemRepository, Provider: p,
				Repo:    provider.RepoRef{Kind: c.kind, Namespace: c.ns, Repo: "web-ui", Branch: "feature/x"},
				Fetched: true, Checkout: gitctx.Checkout{HeadSHA: commit}, Searched: true,
				Hits: []gitctx.Hit{{Symbol: "userName", Path: path, Line: 42}}}
			pl := &Plan{relatedShown: []related.Section{{Item: it}}}
			ki := &KeyIssue{RelevantFile: path, StartLine: 42, EndLine: 42, relatedTag: "R1"}
			unknown := false
			if !pl.attachRelated(ki, map[string]*provider.FilePatch{}, &unknown) || unknown {
				t.Fatal("the finding is not related")
			}
			if ki.Link != c.want {
				t.Errorf("link %q\nwant %q", ki.Link, c.want)
			}
			if strings.Contains(ki.Link, "feature") || !strings.Contains(ki.Link, commit) {
				t.Errorf("link %q is not at the commit", ki.Link)
			}
			if ki.Related != it.URL || !ki.RelatedRepository || ki.LocationUnverified || ki.Snippet != "" ||
				ki.RelatedName != c.ns+"/web-ui" {
				t.Errorf("finding %+v", ki)
			}
		})
	}
}

// consumerFindingsAnswer has a finding in R2 (the repository) on the hit
// file, one in R2 on a file the run never saw, one in R2 on a primary file
// the run did not see in the repository, and one on the reviewed pull
// request.
const consumerFindingsAnswer = "```yaml\nreview:\n  key_issues_to_review:\n" +
	"    - relevant_file: src/client/profile.ts\n      issue_header: Consumer reads the old name\n" +
	"      issue_content: It reads userName, which the pull request renames.\n      start_line: 42\n      end_line: 42\n      related: R2\n" +
	"    - relevant_file: src/client/never-seen.ts\n      issue_header: Maybe here too\n" +
	"      issue_content: Needs verification.\n      start_line: 7\n      end_line: 7\n      related: R2\n" +
	"    - relevant_file: src/app.go\n      issue_header: Tagged primary\n      issue_content: On the reviewed file.\n" +
	"      start_line: 10\n      end_line: 12\n      related: R2\n" +
	"    - relevant_file: src/app.go\n      issue_header: Primary\n      issue_content: On the reviewed pull request.\n" +
	"      start_line: 10\n      end_line: 12\n```\n"

// TestConsumerFindingsInTheReview: in a published review, the finding on
// the consumer's hit file is linked at the commit and never anchored
// inline; the one on a file the run never saw has no link and is marked
// unverified; a primary file the run did not see in the repository makes a
// finding on the reviewed pull request, placed inline as before; the result
// validates.
func TestConsumerFindingsInTheReview(t *testing.T) {
	h, b := consumerHarness(true, consumerFindingsAnswer)
	h.prov.files = append(h.prov.files, contractFile())
	b.hits = map[string][]gitctx.Hit{"web-ui@feature/x": {consumerHit}}
	h.deps.Config.Review.MaxFindings = 5
	res := runRelated(t, h, Args{Related: []string{relatedURL, consumerBranchURL}, Publish: true})
	f := res.Review.KeyIssuesToReview
	if len(f) != 4 {
		t.Fatalf("%d findings", len(f))
	}
	seen, unseen, tagged, primary := f[0], f[1], f[2], f[3]
	// The harness's provider builds every link of its repositories as
	// relProvider does; the per-provider form is TestConsumerFindingLinkPerProvider's.
	if seen.Related != consumerBranchURL || !seen.RelatedRepository || seen.RelatedName != "grp/web-ui" ||
		seen.Link != "https://gitlab.example/grp/svc/-/blob/"+consumerCommit+"/src/client/profile.ts#L42" || seen.InlineStatus != "" {
		t.Errorf("the consumer finding %+v", seen)
	}
	if unseen.Related != consumerBranchURL || unseen.Link != "" || !unseen.LocationUnverified || unseen.InlineStatus != "" {
		t.Errorf("the unverified finding %+v", unseen)
	}
	if tagged.Related != "" || tagged.RelatedRepository || tagged.InlineStatus == "" || primary.InlineStatus == "" {
		t.Errorf("the primary findings %+v %+v", tagged, primary)
	}
	for _, items := range h.prov.inline {
		for _, it := range items {
			if strings.HasPrefix(it.Path, "src/client/") {
				t.Errorf("a consumer finding was posted inline: %+v", it)
			}
		}
	}
	if slices.Contains(res.Notes, NoteRelatedUnknown) {
		t.Errorf("notes %q", res.Notes)
	}
}

// repoRecordLine is the related line of a record of the given repository
// URLs and branches ("" for the default branch), after the pull request
// entries prs.
func repoRecordLine(t *testing.T, prs []relatedRecordItem, repos ...[2]string) string {
	t.Helper()
	pl := &Plan{}
	for _, it := range prs {
		pl.related = append(pl.related, &related.Item{URL: it.U, Read: true, PR: &provider.PullRequest{HeadSHA: it.H}})
	}
	for _, r := range repos {
		pl.related = append(pl.related, &related.Item{URL: r[0], Kind: provider.ItemRepository, Repo: provider.RepoRef{Branch: r[1]}})
	}
	line := pl.relatedRecordLine()
	if line == "" {
		t.Fatal("no related line")
	}
	return line
}

// TestRelatedRecordRepositories: repository entries are written by URL and
// branch ("-" for the default branch), never with a commit, after the pull
// requests; failed repository items are part of the set, ignored ones are
// not; a record of pull requests only is byte for byte as before; invalid
// repository entries make the record unreadable.
func TestRelatedRecordRepositories(t *testing.T) {
	pl := &Plan{related: []*related.Item{
		{URL: consumerBranchURL, Kind: provider.ItemRepository, Repo: provider.RepoRef{Branch: "feature/x"}, Fetched: true,
			Checkout: gitctx.Checkout{HeadSHA: consumerCommit}, Status: related.StatusFull},
		{URL: relatedURL, Read: true, PR: &provider.PullRequest{HeadSHA: relHead}, Status: related.StatusFull},
		{URL: consumerURL, Kind: provider.ItemRepository, Status: related.StatusFailed, Reason: related.ReasonRepoContextOff},
		{URL: ownRepoURL, Kind: provider.ItemRepository, Status: related.StatusIgnored, Reason: related.ReasonPrimaryRepository},
	}}
	line := pl.relatedRecordLine()
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimSuffix(strings.TrimPrefix(line, relatedLinePrefix), relatedLineSuffix))
	if err != nil {
		t.Fatal(err)
	}
	want := `{"v":1,"items":[{"u":"` + relatedURL + `","h":"` + relHead + `"},{"k":"repo","u":"` + consumerBranchURL +
		`","b":"feature/x"},{"k":"repo","u":"` + consumerURL + `","b":"-"}]}`
	if string(raw) != want {
		t.Errorf("record %s\nwant   %s", raw, want)
	}
	if strings.Contains(string(raw), consumerCommit) {
		t.Error("the record carries the commit")
	}
	body := withTrailer("Body.", "", line, nil, shaOld)
	if rec := relatedRecordOf(body); rec == nil || len(rec.Items) != 3 || rec.Items[2] != (relatedRecordItem{K: "repo", U: consumerURL, B: "-"}) {
		t.Errorf("read back %+v", rec)
	}

	// A record of pull requests only is the 2.4 record.
	old := recordLine(t, relatedRecordItem{U: relatedURL, H: relHead})
	if got, _ := base64.RawURLEncoding.DecodeString(strings.TrimSuffix(strings.TrimPrefix(old, relatedLinePrefix), relatedLineSuffix)); string(got) !=
		`{"v":1,"items":[{"u":"`+relatedURL+`","h":"`+relHead+`"}]}` {
		t.Errorf("pull request record %s", got)
	}
	bbs := "https://git.example/projects/P/repos/web?at=refs/heads/feature/x"
	if rec := relatedRecordOf(withTrailer("B.", "", repoRecordLine(t, nil, [2]string{bbs, "feature/x"}), nil, shaOld)); rec == nil {
		t.Error("a Bitbucket Server repository URL with its branch query is not read back")
	}

	reviewed := reviewedLineOf(shaOld)
	bad := func(js string) string {
		return "Body.\n" + relatedLinePrefix + base64.RawURLEncoding.EncodeToString([]byte(js)) + relatedLineSuffix + "\n" +
			reviewed + "\n" + OverviewMarker
	}
	for name, js := range map[string]string{
		"a commit on a repository":   `{"v":1,"items":[{"k":"repo","u":"` + consumerURL + `","b":"-","h":"` + relHead + `"}]}`,
		"no branch":                  `{"v":1,"items":[{"k":"repo","u":"` + consumerURL + `"}]}`,
		"a bad branch":               `{"v":1,"items":[{"k":"repo","u":"` + consumerURL + `","b":"a..b"}]}`,
		"an unknown kind":            `{"v":1,"items":[{"k":"tag","u":"` + consumerURL + `","b":"-"}]}`,
		"a branch on a pull request": `{"v":1,"items":[{"u":"` + relatedURL + `","h":"` + relHead + `","b":"-"}]}`,
		"a repository URL twice": `{"v":1,"items":[{"k":"repo","u":"` + consumerURL + `","b":"-"},{"k":"repo","u":"` + consumerURL +
			`","b":"main"}]}`,
	} {
		if got := relatedRecordOf(bad(js)); got != nil {
			t.Errorf("%s: read %+v", name, got)
		}
	}
}

// Files of the incremental consumer runs: since the last review only
// incPathEdited changed, so the run reviews it alone; the unchanged file
// removes a definition that a full review would search for too.
const (
	consumerSameBase   = "package x\n\nfunc RetiredName() {}\n"
	consumerSameHead   = "package x\n\nfunc Same() {}\n"
	consumerEditedBase = "package x\n\nfunc Before() {}\n"
	consumerEditedLast = "package x\n\nfunc Between() {}\n"
	consumerEditedHead = "package x\n\nfunc Edited() {}\n"
)

// incConsumerHarness is an incremental harness with repository items whose
// earlier overview recorded the commit last and the trailer lines; the
// consumer's branches are pinned at commit.
func incConsumerHarness(last, commit string, enabled bool, lines ...string) (*harness, *consumerRepo) {
	h, b := consumerHarness(enabled, answerWith(onEdited))
	b.commit = commit
	h.prov.pr.HeadSHA = shaHead
	h.prov.files = []provider.FilePatch{
		incFile(incPathSame, consumerSameBase, consumerSameHead),
		incFile(incPathEdited, consumerEditedBase, consumerEditedHead),
	}
	h.prov.revs = map[string]map[string]string{
		"base0": {incPathSame: consumerSameBase, incPathEdited: consumerEditedBase},
		shaOld:  {incPathSame: consumerSameHead, incPathEdited: consumerEditedLast},
	}
	body := "Earlier overview.\n\n"
	for _, l := range lines {
		body += l + "\n"
	}
	h.prov.addComment(h.prov.me, body+reviewedLineOf(last)+"\n"+OverviewMarker)
	return h, b
}

// TestRelatedRepositoryIncremental: a repository is recorded by URL and
// branch, not by commit (owner default 8): a moved commit keeps the run
// incremental, which searches the repository again at its new commit with
// the identifiers of the files it reviews and reports that commit; a
// different branch, an added or a removed repository makes a full review
// with the note; a repository item that cannot be read now
// (repo_context_off) is still part of the recorded set. Every result
// validates.
func TestRelatedRepositoryIncremental(t *testing.T) {
	const moved = "1111111111111111111111111111111111111111"
	recorded := repoRecordLine(t, nil, [2]string{consumerBranchURL, "feature/x"})
	cases := []struct {
		name    string
		lines   []string
		related []string
		enabled bool
		mode    string
		note    bool
	}{
		{"moved commit", []string{recorded}, []string{consumerBranchURL}, true, IncrementalChanged, false},
		{"another branch", []string{repoRecordLine(t, nil, [2]string{consumerBranchURL, "feature/y"})},
			[]string{consumerBranchURL}, true, IncrementalFull, true},
		{"the default branch before", []string{repoRecordLine(t, nil, [2]string{consumerURL, ""})},
			[]string{consumerBranchURL}, true, IncrementalFull, true},
		{"a repository added", []string{recordLine(t, relatedRecordItem{U: relatedURL, H: relHead})},
			[]string{relatedURL, consumerBranchURL}, true, IncrementalFull, true},
		{"a repository removed", []string{recorded}, nil, true, IncrementalFull, true},
		{"no record before", nil, []string{consumerBranchURL}, true, IncrementalFull, true},
		{"repository context off now", []string{recorded}, []string{consumerBranchURL}, false, IncrementalChanged, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h, b := incConsumerHarness(shaOld, moved, c.enabled, c.lines...)
			b.hits = map[string][]gitctx.Hit{"web-ui@feature/x": {{Symbol: "Before", Path: "src/use.go", Line: 3, Snippet: "Before()"}}}
			res, err := Run(context.Background(), h.deps, incRelatedArgs(c.related...))
			if err != nil {
				t.Fatal(err)
			}
			validateResult(t, res)
			if res.Incremental.Mode != c.mode {
				t.Errorf("mode %s, want %s", res.Incremental.Mode, c.mode)
			}
			if got := slices.Contains(res.Notes, NoteRelatedChanged); got != c.note {
				t.Errorf("note %v, want %v: %q", got, c.note, res.Notes)
			}
			if c.name != "moved commit" {
				return
			}
			e := res.Coverage.Related[0]
			if e.Commit != moved || e.Status != "full" || *e.Identifiers != 1 {
				t.Errorf("entry %+v", e)
			}
			if len(b.searches) != 1 || len(b.searches[0].Terms) != 1 || b.searches[0].Terms[0].Text != "Before" {
				t.Errorf("searches %+v, want the identifiers of the reviewed file only", b.searches)
			}
		})
	}

	// A full review of the same change searches both files' identifiers.
	h, b := incConsumerHarness(shaOld, moved, true)
	if _, err := Run(context.Background(), h.deps, Args{PRURL: testPRURL, Related: []string{consumerBranchURL}}); err != nil {
		t.Fatal(err)
	}
	if len(b.searches) != 1 || len(b.searches[0].Terms) != 2 {
		t.Errorf("full review searches %+v", b.searches)
	}
}
