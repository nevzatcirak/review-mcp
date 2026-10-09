package describe

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/nevzatcirak/review-mcp/internal/config"
	"github.com/nevzatcirak/review-mcp/internal/llm"
	"github.com/nevzatcirak/review-mcp/internal/llmrun"
	"github.com/nevzatcirak/review-mcp/internal/provider"
)

// Markers that must reach the model but never the logs (X-8).
const (
	titleMarker  = "TITLE-MARKER-4d2e"
	descMarker   = "DESC-MARKER-8b1f"
	commitMarker = "COMMIT-MARKER-61aa"
	diffMarker   = "DIFF-MARKER-0c93"
	answerMarker = "ANSWER-MARKER-f7e5"
)

const testPRURL = "https://your-gitea.example/octo/demo/pulls/7"

// fakeProvider serves one synthetic PR. GetDiff applies the Include filter
// like the real providers. Any other provider method panics (nil embedded
// interface): the pipeline must not call it.
type fakeProvider struct {
	provider.Provider

	pr        provider.PullRequest
	files     []provider.FilePatch
	skipped   []provider.SkippedFile
	commits   []string
	commitErr error
	calls     []string
}

func (f *fakeProvider) GetPullRequest(context.Context, provider.PRRef) (*provider.PullRequest, error) {
	f.calls = append(f.calls, "pr")
	pr := f.pr
	return &pr, nil
}

func (f *fakeProvider) GetDiff(_ context.Context, _ provider.PRRef, _ *provider.PullRequest, opts provider.DiffOptions) (*provider.Diff, error) {
	f.calls = append(f.calls, "diff")
	d := &provider.Diff{Skipped: slices.Clone(f.skipped)}
	for _, fp := range f.files {
		if opts.Include != nil && !opts.Include(fp.Path) {
			d.Skipped = append(d.Skipped, provider.SkippedFile{Path: fp.Path, Reason: provider.SkipFiltered})
			continue
		}
		d.Files = append(d.Files, fp)
	}
	return d, nil
}

func (f *fakeProvider) GetCommitMessages(context.Context, provider.PRRef) ([]string, error) {
	f.calls = append(f.calls, "commits")
	return f.commits, f.commitErr
}

type fakeResolver struct{ p *fakeProvider }

func (r *fakeResolver) Resolve(u string) (provider.PRRef, provider.Provider, error) {
	return provider.PRRef{Kind: provider.KindGitea, Namespace: "octo", Repo: "demo", Number: 7, URL: u}, r.p, nil
}

type llmCall struct{ system, user string }

// scriptLLM answers each call by its kind: the reduce call (its user prompt
// has the files walkthrough), a part (read from the part line), or the one
// call (part 0). answers[k] is a list taken in order for kind k (the last
// one repeats); errs[k] fails every call of kind k. kindReduce is -1.
type scriptLLM struct {
	answers   map[int][]string
	errs      map[int]error
	truncated map[int]bool
	calls     []llmCall
	kinds     []int
	seen      map[int]int
}

const kindReduce = -1

var partRE = regexp.MustCompile(`This is part (\d+) of (\d+)\.`)

func callKind(user string) int {
	if strings.Contains(user, "\nFiles walkthrough:\n=====\n") {
		return kindReduce
	}
	if m := partRE.FindStringSubmatch(user); m != nil {
		n, _ := strconv.Atoi(m[1])
		return n
	}
	return 0
}

func (f *scriptLLM) Complete(_ context.Context, system, user string) (*llm.Response, error) {
	f.calls = append(f.calls, llmCall{system, user})
	k := callKind(user)
	f.kinds = append(f.kinds, k)
	if f.seen == nil {
		f.seen = map[int]int{}
	}
	i := f.seen[k]
	f.seen[k]++
	if err := f.errs[k]; err != nil {
		return nil, err
	}
	list := f.answers[k]
	if len(list) == 0 {
		return nil, &llm.Error{Class: llm.ClassProtocol, Detail: "no scripted answer"}
	}
	a := list[min(i, len(list)-1)]
	return &llm.Response{Content: a, Truncated: f.truncated[k], Usage: llm.Usage{PromptTokens: 100, CompletionTokens: 20}}, nil
}

func testConfig() *config.Config {
	cfg := config.Defaults()
	cfg.LLM.BaseURL = "https://llm.example.com/v1"
	cfg.LLM.Model = "test-model"
	cfg.LLM.ContextWindow = 32000
	cfg.Gitea.BaseURL = "https://your-gitea.example"
	return cfg
}

type harness struct {
	deps   Deps
	prov   *fakeProvider
	llm    *scriptLLM
	logs   *bytes.Buffer
	stages []string
}

// sampleFiles: two modified Go files and a vendored file the default ignore
// glob filters.
func sampleFiles() []provider.FilePatch {
	return []provider.FilePatch{
		{
			Path: "src/app.go", Type: provider.ChangeModified, Additions: 1, Deletions: 1,
			Patch:      "@@ -10,3 +10,3 @@\n line 10\n-retries := 1\n+retries := 3 // " + diffMarker + "\n line 12\n",
			BaseStatus: provider.ContentNotFetchedSizeCap, HeadStatus: provider.ContentNotFetchedSizeCap,
		},
		{
			Path: "src/app_test.go", Type: provider.ChangeAdded, Additions: 2,
			Patch:      "@@ -0,0 +1,2 @@\n+package app\n+func TestRetries(t *testing.T) {}\n",
			BaseStatus: provider.ContentNotApplicable, HeadStatus: provider.ContentNotFetchedSizeCap,
		},
		{
			Path: "vendor/lib.go", Type: provider.ChangeAdded, Additions: 1,
			Patch:      "@@ -0,0 +1 @@\n+x\n",
			BaseStatus: provider.ContentNotApplicable, HeadStatus: provider.ContentNotFetchedSizeCap,
		},
	}
}

func newHarness(answers map[int][]string) *harness {
	h := &harness{logs: &bytes.Buffer{}}
	h.prov = &fakeProvider{
		pr: provider.PullRequest{Title: "Retry more " + titleMarker, Description: "Raises the retry count. " + descMarker,
			SourceBranch: "feature/retry", TargetBranch: "main", HeadSHA: "abc"},
		files:   sampleFiles(),
		commits: []string{"Raise the retry count\n\nThree is safer. " + commitMarker, "Add a retry test\n"},
	}
	h.llm = &scriptLLM{answers: answers, errs: map[int]error{}, truncated: map[int]bool{}}
	h.deps = Deps{
		Config:   testConfig(),
		Logger:   slog.New(slog.NewTextHandler(h.logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
		Resolver: &fakeResolver{p: h.prov},
		LLM:      h.llm,
		Progress: func(s string) { h.stages = append(h.stages, s) },
	}
	return h
}

func (h *harness) run(t *testing.T, args Args) *Result {
	t.Helper()
	if args.PRURL == "" {
		args.PRURL = testPRURL
	}
	res, err := Run(context.Background(), h.deps, args)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	h.checkNoLeaks(t)
	return res
}

func (h *harness) checkNoLeaks(t *testing.T, errs ...error) {
	t.Helper()
	for _, m := range []string{titleMarker, descMarker, commitMarker, diffMarker, answerMarker} {
		if strings.Contains(h.logs.String(), m) {
			t.Errorf("marker %s leaked into the logs", m)
		}
		for _, err := range errs {
			if err != nil && strings.Contains(err.Error(), m) {
				t.Errorf("marker %s leaked into an error", m)
			}
		}
	}
}

// fileYAML is one pr_files entry.
func fileYAML(path, title, summary, label string) string {
	block := func(v string) string { return strings.ReplaceAll(strings.TrimSuffix(v, "\n"), "\n", "\n    ") }
	return "- filename: |\n    " + path + "\n" +
		"  changes_summary: |\n    " + block(summary) + "\n" +
		"  changes_title: |\n    " + block(title) + "\n" +
		"  label: |\n    " + block(label) + "\n"
}

// oneCallAnswer is a full answer for sampleFiles.
const oneCallAnswer = "```yaml\ntype:\n- Enhancement\n- Tests\n" +
	"description: |\n  - Raise the retry count to three\n  - Add a retry test " + answerMarker + "\n" +
	"title: |\n  Raise the retry count and test it\n" +
	"pr_files:\n" +
	"- filename: |\n    src/app.go\n  changes_summary: |\n    - Raise `retries` from 1 to 3\n  changes_title: |\n    Raise the retry count\n  label: |\n    enhancement\n" +
	"- filename: |\n    src/app_test.go\n  changes_summary: |\n    - Add `TestRetries`\n  changes_title: |\n    Add a retry test\n  label: |\n    tests\n" +
	"```\n"

// marshal is the indented JSON of v, as the goldens store it.
func marshal(t *testing.T, v any) string {
	t.Helper()
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

// TestOneCallRun: a pull request whose diff fits one call is described in
// one call with the full schema; the result, the prompts and the stages are
// pinned by goldens (testdata/runs/one_call).
func TestOneCallRun(t *testing.T) {
	h := newHarness(map[int][]string{0: {oneCallAnswer}})
	res := h.run(t, Args{})

	if len(h.llm.calls) != 1 || h.llm.kinds[0] != 0 {
		t.Fatalf("calls %v, want one call without a part line", h.llm.kinds)
	}
	c := h.llm.calls[0]
	for _, m := range []string{titleMarker, descMarker, commitMarker, diffMarker} {
		if !strings.Contains(c.user, m) {
			t.Errorf("marker %s did not reach the model", m)
		}
	}
	if strings.Contains(c.user, "vendor/lib.go") {
		t.Errorf("the filtered file reached the model")
	}
	if res.Title == nil || *res.Title != "Raise the retry count and test it" ||
		!slices.Equal(res.Type, []string{"Enhancement", "Tests"}) || res.Description == nil {
		t.Errorf("title %s type %v description %s", strOrNull(res.Title), res.Type, strOrNull(res.Description))
	}
	if len(res.Files) != 2 || res.Files[0].Path != "src/app.go" || res.Files[1].Label != "tests" {
		t.Errorf("files = %+v", res.Files)
	}
	cov := res.Coverage
	if cov.Partial || cov.ReviewedFiles != 2 || cov.TotalFiles != 2 || cov.ModelCalls != 1 || len(cov.Filtered) != 1 {
		t.Errorf("coverage = %+v", cov)
	}
	if m := res.Metadata; m.LLMCalls != 1 || m.CommitMessages != 2 || m.RepairTactic != "direct" || !m.FastPath {
		t.Errorf("metadata = %+v", m)
	}
	if len(res.Notes) != 0 {
		t.Errorf("notes = %v", res.Notes)
	}
	if want := []string{StageFetching, StagePreparingDiff, StageCallingModel}; !slices.Equal(h.stages, want) {
		t.Errorf("stages = %v", h.stages)
	}

	dir := "testdata/runs/one_call"
	checkGolden(t, dir+"/system.txt", c.system)
	checkGolden(t, dir+"/user.txt", c.user)
	checkGolden(t, dir+"/result.json", marshal(t, res))
}

// threePartHarness: six files of about 600 tokens each in the plain diff
// with diff.max_tokens 1500, so the description takes three parts of two
// files (pr_review's chunked tests use 2000 for the larger numbered diff).
func threePartHarness(t *testing.T) *harness {
	t.Helper()
	h := newHarness(map[int][]string{})
	h.prov.files = bigFiles(6)
	capTokens := 1500
	h.deps.Config.Diff.MaxTokens = &capTokens
	for i := 1; i <= 3; i++ {
		h.llm.answers[i] = []string{partAnswer(i, partFiles(i)...)}
	}
	h.llm.answers[kindReduce] = []string{reduceAnswer}
	return h
}

func bigFiles(n int) []provider.FilePatch {
	var files []provider.FilePatch
	for i := range n {
		files = append(files, provider.FilePatch{
			Path: fmt.Sprintf("src/f%02d.go", i), Type: provider.ChangeModified,
			Patch:      "@@ -1,2 +1,60 @@\n a\n" + strings.Repeat("+some added line of code here\n", 60),
			BaseStatus: provider.ContentNotFetchedSizeCap, HeadStatus: provider.ContentNotFetchedSizeCap,
		})
	}
	return files
}

// partFiles are the files of part i (1-based) of threePartHarness.
func partFiles(i int) []string {
	return []string{fmt.Sprintf("src/f%02d.go", 2*(i-1)), fmt.Sprintf("src/f%02d.go", 2*(i-1)+1)}
}

// partAnswer describes the given files. It also carries a title and a type,
// which a part's answer must not contribute: only the reduce call does.
func partAnswer(part int, files ...string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "```yaml\ntitle: |\n  PART-%d-TITLE\ntype:\n- Bug fix\npr_files:\n", part)
	for _, f := range files {
		base := strings.TrimSuffix(strings.TrimPrefix(f, "src/"), ".go")
		b.WriteString(fileYAML(f, "Add lines to "+base, "- Adds sixty lines to `"+base+"`", "enhancement"))
	}
	b.WriteString("```\n")
	return b.String()
}

const reduceAnswer = "```yaml\ntype:\n- Enhancement\ndescription: |\n  - Add lines to six files\ntitle: |\n  REDUCE-TITLE Add lines across the code base\n```\n"

// TestThreePartRun: a pull request that needs three parts is described in
// three part calls (files walkthrough only, each with its own files) and one
// reduce call without the diff; the title, type and summary come from the
// reduce call. Pinned by goldens (testdata/runs/three_parts).
func TestThreePartRun(t *testing.T) {
	h := threePartHarness(t)
	res := h.run(t, Args{})

	if !slices.Equal(h.llm.kinds, []int{1, 2, 3, kindReduce}) {
		t.Fatalf("calls %v, want parts 1 to 3 then the reduce call", h.llm.kinds)
	}
	for i := 1; i <= 3; i++ {
		c := h.llm.calls[i-1]
		if !strings.Contains(c.user, PartHeader(i, 3)+"\n\n\nThe PR Git Diff:\n") {
			t.Errorf("part %d: the part line is not right before the diff", i)
		}
		if !strings.Contains(c.system, "Your task is to provide a files walkthrough") || strings.Contains(c.system, "class PRType") {
			t.Errorf("part %d: not the part-mode system prompt", i)
		}
		for j := 1; j <= 3; j++ {
			for _, f := range partFiles(j) {
				if got := strings.Contains(c.user, "## File: '"+f+"'"); got != (i == j) {
					t.Errorf("part %d: file %s in the prompt = %v", i, f, got)
				}
			}
		}
	}
	red := h.llm.calls[3]
	if strings.Contains(red.user, "+some added line of code here") || strings.Contains(red.user, "The PR Git Diff") {
		t.Errorf("the reduce call carries the diff")
	}
	for _, m := range []string{titleMarker, descMarker, commitMarker, "## File: 'src/f05.go'\nTitle: Add lines to f05\nSummary:\n- Adds sixty lines to `f05`"} {
		if !strings.Contains(red.user, m) {
			t.Errorf("the reduce call lacks %q", m)
		}
	}
	if want := []string{StageFetching, StagePreparingDiff, CallingModelPart(1, 3), CallingModelPart(2, 3),
		CallingModelPart(3, 3), StageSummarizing}; !slices.Equal(h.stages, want) {
		t.Errorf("stages = %v", h.stages)
	}
	if res.Coverage.ModelCalls != 3 || res.Coverage.Partial || res.Coverage.ReviewedFiles != 6 || res.Metadata.LLMCalls != 4 {
		t.Errorf("coverage %+v llm_calls %d", res.Coverage, res.Metadata.LLMCalls)
	}

	dir := "testdata/runs/three_parts"
	for i, c := range h.llm.calls {
		name := "part" + strconv.Itoa(i+1)
		if i == 3 {
			name = "reduce"
		}
		checkGolden(t, dir+"/"+name+".system.txt", c.system)
		checkGolden(t, dir+"/"+name+".user.txt", c.user)
	}
	checkGolden(t, dir+"/result.json", marshal(t, res))
}

// TestReduceSetsTheSummary [canary target]: in parts, the title, the types
// and the summary come from the reduce call only; a part's title or type
// never reaches the result, even though the part answers carry them.
func TestReduceSetsTheSummary(t *testing.T) {
	h := threePartHarness(t)
	res := h.run(t, Args{})
	if res.Title == nil || *res.Title != "REDUCE-TITLE Add lines across the code base" {
		t.Errorf("title = %s, want the reduce call's", strOrNull(res.Title))
	}
	if !slices.Equal(res.Type, []string{"Enhancement"}) {
		t.Errorf("type = %v, want the reduce call's", res.Type)
	}
	if res.Description == nil || *res.Description != "- Add lines to six files" {
		t.Errorf("description = %s, want the reduce call's", strOrNull(res.Description))
	}
	if n := len(h.llm.calls); n != 4 || h.llm.kinds[3] != kindReduce {
		t.Errorf("%d calls (%v); want three parts and the reduce call", n, h.llm.kinds)
	}
	for _, f := range res.Files {
		if strings.Contains(f.Title, "PART-") {
			t.Errorf("a part's title leaked into the walkthrough: %+v", f)
		}
	}
}

// strOrNull quotes *p, or is "null".
func strOrNull(p *string) string {
	if p == nil {
		return "null"
	}
	return strconv.Quote(*p)
}

// TestReduceFailure: when the reduce call fails, type and title are null,
// the description is the described files' titles, and the fixed note is
// added; the walkthrough and the coverage are kept.
func TestReduceFailure(t *testing.T) {
	for name, set := range map[string]func(h *harness){
		"error":       func(h *harness) { h.llm.errs[kindReduce] = &llm.Error{Class: llm.ClassTimeout} },
		"unparseable": func(h *harness) { h.llm.answers[kindReduce] = []string{"not yaml at all: [", "still: ["} },
		"does not fit": func(h *harness) {
			// A window the parts fit (their diff is capped at 1500 tokens)
			// but the walkthrough does not, even with the titles only.
			h.deps.Config.LLM.ContextWindow = 8000
			for i := 1; i <= 3; i++ {
				var b strings.Builder
				b.WriteString("```yaml\npr_files:\n")
				for _, f := range partFiles(i) {
					b.WriteString(fileYAML(f, "Add lines "+strings.Repeat("word ", 1500), "- short", "enhancement"))
				}
				b.WriteString("```\n")
				h.llm.answers[i] = []string{b.String()}
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			h := threePartHarness(t)
			set(h)
			res := h.run(t, Args{})
			if res.Title != nil || res.Type != nil {
				t.Errorf("title %s type %v, want null", strOrNull(res.Title), res.Type)
			}
			if !slices.Contains(res.Notes, NoteReduceFailed) {
				t.Errorf("notes = %v", res.Notes)
			}
			if len(res.Files) != 6 || res.Coverage.Partial {
				t.Errorf("files %d coverage %+v", len(res.Files), res.Coverage)
			}
			var lines []string
			for _, f := range res.Files {
				lines = append(lines, "- "+f.Title)
			}
			if res.Description == nil || *res.Description != strings.Join(lines, "\n") {
				t.Errorf("description = %s", strOrNull(res.Description))
			}
			raw := marshal(t, res)
			if !strings.Contains(raw, `"title": null`) || !strings.Contains(raw, `"type": null`) {
				t.Errorf("JSON does not carry null title and type:\n%s", raw)
			}
			switch name {
			case "unparseable":
				if h.llm.seen[kindReduce] != 2 {
					t.Errorf("reduce calls = %d, want the call and its one re-ask", h.llm.seen[kindReduce])
				}
			case "does not fit":
				if h.llm.seen[kindReduce] != 0 {
					t.Errorf("a reduce request that cannot fit was sent")
				}
			}
		})
	}
}

// TestReduceWithoutSummaries: a walkthrough whose summaries do not fit the
// context window is sent with the titles only, with a note.
func TestReduceWithoutSummaries(t *testing.T) {
	h := threePartHarness(t)
	for i := 1; i <= 3; i++ {
		var b strings.Builder
		b.WriteString("```yaml\npr_files:\n")
		for _, f := range partFiles(i) {
			b.WriteString(fileYAML(f, "Add lines to "+f, strings.Repeat("- a long summary line about this file\n", 1000), "enhancement"))
		}
		b.WriteString("```\n")
		h.llm.answers[i] = []string{b.String()}
	}
	res := h.run(t, Args{})
	if h.llm.seen[kindReduce] != 1 {
		t.Fatalf("reduce calls = %d, want 1", h.llm.seen[kindReduce])
	}
	red := h.llm.calls[len(h.llm.calls)-1].user
	if strings.Contains(red, "Summary:") || !strings.Contains(red, "Title: Add lines to src/f00.go") {
		t.Errorf("the reduce call was not sent the titles only")
	}
	if !slices.Contains(res.Notes, NoteReduceWithoutSummaries) || res.Title == nil {
		t.Errorf("notes %v title %s", res.Notes, strOrNull(res.Title))
	}
}

// TestFailedPart: a part whose call fails makes its files not described
// (model_call_failed, X-19) with the fixed class note; the other parts and
// the reduce call still run, and the banner counts the lost files.
func TestFailedPart(t *testing.T) {
	h := threePartHarness(t)
	h.llm.errs[2] = &llm.Error{Class: llm.ClassTimeout}
	res := h.run(t, Args{})
	c := res.Coverage
	if c.FailedParts != 1 || c.ModelCalls != 3 || !c.Partial || c.ReviewedFiles != 4 || c.NotReviewedFiles != 2 {
		t.Errorf("coverage = %+v", c)
	}
	var lost []string
	for _, s := range c.Skipped {
		if s.Reason == llmrun.SkipModelCallFailed {
			lost = append(lost, s.Path)
		}
	}
	if !slices.Equal(lost, partFiles(2)) {
		t.Errorf("model_call_failed files = %v", lost)
	}
	if !slices.Contains(res.Notes, "Part 2 of 3 failed (llm_timeout); its files were not described.") {
		t.Errorf("notes = %v", res.Notes)
	}
	for _, f := range res.Files {
		if slices.Contains(partFiles(2), f.Path) {
			t.Errorf("a file of the failed part is described: %+v", f)
		}
	}
	if res.Title == nil || h.llm.seen[kindReduce] != 1 {
		t.Errorf("the reduce call did not run: title %s", strOrNull(res.Title))
	}
	if strings.Contains(h.llm.calls[len(h.llm.calls)-1].user, "## File: 'src/f02.go'") {
		t.Errorf("the reduce call lists a file of the failed part")
	}
}

// TestAllPartsFail: when every part fails, the run fails with the first
// part's classified error, as a description in one call does.
func TestAllPartsFail(t *testing.T) {
	h := threePartHarness(t)
	for i := 1; i <= 3; i++ {
		h.llm.errs[i] = &llm.Error{Class: llm.ClassTimeout}
	}
	_, err := Run(context.Background(), h.deps, Args{PRURL: testPRURL})
	var le *llm.Error
	if !errors.As(err, &le) || le.Class != llm.ClassTimeout {
		t.Fatalf("err = %v", err)
	}
	if h.llm.seen[kindReduce] != 0 {
		t.Errorf("the reduce call ran after every part failed")
	}
}

// TestPartPathValidation [canary target]: each part's walkthrough is
// validated against that part's own files. A part that describes a file of
// another part (or one outside the pull request) has the entry dropped and
// counted; the file keeps the description of the part that was shown it.
func TestPartPathValidation(t *testing.T) {
	h := threePartHarness(t)
	h.llm.answers[1] = []string{"```yaml\npr_files:\n" +
		fileYAML("src/f00.go", "Add lines to f00", "- one", "enhancement") +
		fileYAML("src/f01.go", "Add lines to f01", "- one", "enhancement") +
		fileYAML("src/f02.go", "WRONG-PART title for f02", "- not shown to part 1", "enhancement") +
		fileYAML("src/nowhere.go", "A file outside the pull request", "- invented", "enhancement") +
		"```\n"}
	res := h.run(t, Args{})
	var f02 []File
	for _, f := range res.Files {
		if f.Path == "src/f02.go" {
			f02 = append(f02, f)
		}
		if f.Path == "src/nowhere.go" {
			t.Errorf("a path outside the pull request is described: %+v", f)
		}
	}
	if len(f02) != 1 || f02[0].Title != "Add lines to f02" {
		t.Errorf("src/f02.go entries = %+v, want only part 2's", f02)
	}
	if len(res.Files) != 6 {
		t.Errorf("files = %d, want 6", len(res.Files))
	}
	if !slices.Contains(res.Notes, "2 walkthrough entries for a file that was not in the diff shown to the model were dropped.") {
		t.Errorf("notes = %v", res.Notes)
	}
}

// TestOneCallValidation: types outside the enum are dropped with a note
// (case and "_" are forgiven), an entry for a file that was not shown is
// dropped and counted, a duplicate keeps the first entry, a label is one
// line of at most 40 characters, and a shown file without an entry is not
// described (not_returned) with a note and the banner.
func TestOneCallValidation(t *testing.T) {
	answer := "```yaml\ntype:\n- bug_fix\n- Refactoring\n- tests\n- Tests\n- Feature\n" +
		"description: |\n  - Fix it\ntitle: |\n  Fix\n  the retries\n" +
		"pr_files:\n" +
		fileYAML("src/app.go", "Raise the retry count", "- first", "a label that is much longer than forty characters in total\nand has two lines") +
		fileYAML("src/app.go", "DUPLICATE", "- second", "x") +
		fileYAML("vendor/lib.go", "Filtered file", "- not shown", "x") +
		fileYAML("docs/other.md", "Invented file", "- not shown", "x") +
		"```\n"
	h := newHarness(map[int][]string{0: {answer}})
	res := h.run(t, Args{})

	if !slices.Equal(res.Type, []string{"Bug fix", "Tests"}) {
		t.Errorf("type = %v", res.Type)
	}
	if res.Title == nil || *res.Title != "Fix the retries" {
		t.Errorf("title = %s", strOrNull(res.Title))
	}
	if len(res.Files) != 1 || res.Files[0].Title != "Raise the retry count" ||
		res.Files[0].Label != "a label that is much longer than forty c" || len([]rune(res.Files[0].Label)) != MaxLabelRunes {
		t.Errorf("files = %+v", res.Files)
	}
	want := []string{
		"2 type values outside the allowed list (Bug fix, Tests, Enhancement, Documentation, Other) were dropped.",
		"2 walkthrough entries for a file that was not in the diff shown to the model were dropped.",
		"1 duplicate walkthrough entry was dropped; the first entry of each file is kept.",
		"1 file was shown to the model but got no walkthrough entry; listed as not described (see Coverage).",
	}
	if !slices.Equal(res.Notes, want) {
		t.Errorf("notes =\n%s\nwant\n%s", strings.Join(res.Notes, "\n"), strings.Join(want, "\n"))
	}
	c := res.Coverage
	if !c.Partial || c.ReviewedFiles != 1 || c.TotalFiles != 2 || len(c.Skipped) != 1 ||
		c.Skipped[0] != (llmrun.SkippedFile{Path: "src/app_test.go", Reason: SkipNotReturned}) {
		t.Errorf("coverage = %+v", c)
	}
}

// TestUnparseableOneCall: an answer with nothing usable gets one re-ask; a
// second unusable answer fails the run with the fixed sentence.
func TestUnparseableOneCall(t *testing.T) {
	h := newHarness(map[int][]string{0: {"just prose " + answerMarker, "pr_files: not a list " + answerMarker}})
	_, err := Run(context.Background(), h.deps, Args{PRURL: testPRURL})
	if !errors.Is(err, ErrUnparseable) || len(h.llm.calls) != 2 || !strings.Contains(h.llm.calls[1].user, ReaskNote+"\n"+ResponseLine) {
		t.Fatalf("err = %v, calls = %d", err, len(h.llm.calls))
	}
	h.checkNoLeaks(t, err)

	// A re-ask that succeeds is noted.
	h = newHarness(map[int][]string{0: {"just prose", oneCallAnswer}})
	res := h.run(t, Args{})
	if !res.Metadata.Reasked || res.Metadata.LLMCalls != 2 || !slices.Contains(res.Notes, NoteReasked) {
		t.Errorf("metadata %+v notes %v", res.Metadata, res.Notes)
	}
}

// TestCommitMessages: the commit messages are numbered, trimmed and clipped
// to diff.max_commits_tokens; a provider failure to read them is noted and
// does not fail the run.
func TestCommitMessages(t *testing.T) {
	h := newHarness(map[int][]string{0: {oneCallAnswer}})
	h.run(t, Args{})
	if !strings.Contains(h.llm.calls[0].user, "Commit messages:\n=====\n1. Raise the retry count\n\nThree is safer. "+commitMarker+"\n2. Add a retry test\n=====") {
		t.Errorf("commit block missing or malformed")
	}

	if got := CommitBlock([]string{strings.Repeat("word ", 2000)}, 50, 0.3); !strings.HasSuffix(got, "...(truncated)") || len(got) > 400 {
		t.Errorf("clipped block = %d bytes %q...", len(got), got[:min(40, len(got))])
	}
	if got := CommitBlock([]string{" ", ""}, 50, 0.3); got != "" {
		t.Errorf("empty messages = %q", got)
	}

	h = newHarness(map[int][]string{0: {oneCallAnswer}})
	h.prov.commitErr = &provider.Error{Class: provider.ClassAuth, Status: 403}
	res := h.run(t, Args{})
	if strings.Contains(h.llm.calls[0].user, "Commit messages:") || !slices.Contains(res.Notes, NoteCommitsUnavailable) ||
		res.Metadata.CommitMessages != 0 {
		t.Errorf("notes %v metadata %+v", res.Notes, res.Metadata)
	}
}

// TestEmptyDiff: nothing left after filtering makes no model call.
func TestEmptyDiff(t *testing.T) {
	h := newHarness(map[int][]string{0: {oneCallAnswer}})
	h.prov.files = h.prov.files[2:]
	res := h.run(t, Args{})
	if len(h.llm.calls) != 0 || res.Title != nil || res.Type != nil || res.Description != nil ||
		len(res.Files) != 0 || !slices.Contains(res.Notes, NoteNoReviewableChanges) {
		t.Errorf("result = %+v", res)
	}
}

// TestOutputLanguageEveryCall: output_language reaches every call, the
// parts and the reduce call.
func TestOutputLanguageEveryCall(t *testing.T) {
	h := threePartHarness(t)
	h.run(t, Args{OutputLanguage: "tr-TR"})
	for i, c := range h.llm.calls {
		if !strings.Contains(c.system, "locale code: 'tr-TR'") {
			t.Errorf("call %d (kind %d) lacks the language instruction", i+1, h.llm.kinds[i])
		}
	}
}

// TestConfigInvalid: the degraded mode sends nothing.
func TestConfigInvalid(t *testing.T) {
	h := newHarness(nil)
	h.deps.ConfigErr = errors.New("bad")
	if _, err := Run(context.Background(), h.deps, Args{PRURL: testPRURL}); !errors.Is(err, llmrun.ErrConfigInvalid) {
		t.Fatalf("err = %v", err)
	}
	if len(h.prov.calls) != 0 || len(h.llm.calls) != 0 {
		t.Errorf("provider %v llm %d", h.prov.calls, len(h.llm.calls))
	}
}
