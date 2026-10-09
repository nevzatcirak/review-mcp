package improve

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
	"time"

	"github.com/nevzatcirak/review-mcp/internal/config"
	"github.com/nevzatcirak/review-mcp/internal/gitctx"
	"github.com/nevzatcirak/review-mcp/internal/llm"
	"github.com/nevzatcirak/review-mcp/internal/llmrun"
	"github.com/nevzatcirak/review-mcp/internal/provider"
)

// Markers that must reach the model but never the logs (X-8).
const (
	titleMarker   = "TITLE-MARKER-5a1c"
	descMarker    = "DESC-MARKER-9e2d"
	diffMarker    = "DIFF-MARKER-3f70"
	answerMarker  = "ANSWER-MARKER-b81e"
	commentMarker = "COMMENT-MARKER-77c4"
)

const testPRURL = "https://your-gitea.example/octo/demo/pulls/7"

var botUser = provider.User{ID: "5", Name: "review-bot"}

// fakeProvider serves one synthetic PR. GetDiff applies the Include filter
// like the real providers. Any other provider method panics (nil embedded
// interface): the pipeline must not call it.
type fakeProvider struct {
	provider.Provider

	pr         provider.PullRequest
	files      []provider.FilePatch
	threads    []provider.Thread
	threadsErr error
	calls      []string
}

func (f *fakeProvider) GetPullRequest(context.Context, provider.PRRef) (*provider.PullRequest, error) {
	f.calls = append(f.calls, "pr")
	pr := f.pr
	return &pr, nil
}

func (f *fakeProvider) GetDiff(_ context.Context, _ provider.PRRef, _ *provider.PullRequest, opts provider.DiffOptions) (*provider.Diff, error) {
	f.calls = append(f.calls, "diff")
	d := &provider.Diff{}
	for _, fp := range f.files {
		if opts.Include != nil && !opts.Include(fp.Path) {
			d.Skipped = append(d.Skipped, provider.SkippedFile{Path: fp.Path, Reason: provider.SkipFiltered})
			continue
		}
		d.Files = append(d.Files, fp)
	}
	return d, nil
}

func (f *fakeProvider) ListThreads(context.Context, provider.PRRef) ([]provider.Thread, error) {
	f.calls = append(f.calls, "threads")
	return f.threads, f.threadsErr
}

func (f *fakeProvider) CurrentUser(context.Context) (provider.User, error) {
	f.calls = append(f.calls, "user")
	return botUser, nil
}

type fakeResolver struct{ p *fakeProvider }

func (r *fakeResolver) Resolve(u string) (provider.PRRef, provider.Provider, error) {
	return provider.PRRef{Kind: provider.KindGitea, Namespace: "octo", Repo: "demo", Number: 7, URL: u}, r.p, nil
}

type fixedClock struct{}

func (fixedClock) Now() time.Time { return time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC) }

type llmCall struct{ system, user string }

// Call kinds of scriptLLM: the suggestion call of the one call (0) or of
// part k (k), and the self-review of the same (reflectKind(0),
// reflectKind(k)).
func reflectKind(part int) int { return 100 + part }

// scriptLLM answers each call by its kind. A self-review prompt is told
// apart by its first line; its part is the part of the suggestion call
// before it (the calls are sequential). answers[k] is a list taken in order
// for kind k (the last one repeats); errs[k] fails every call of kind k.
type scriptLLM struct {
	answers   map[int][]string
	errs      map[int]error
	truncated map[int]bool
	calls     []llmCall
	kinds     []int
	seen      map[int]int
	lastPart  int
}

var partRE = regexp.MustCompile(`This is part (\d+) of (\d+)\.`)

func (f *scriptLLM) kind(user string) int {
	if strings.HasPrefix(user, "You are given a Pull Request (PR) code diff:") {
		return reflectKind(f.lastPart)
	}
	f.lastPart = 0
	if m := partRE.FindStringSubmatch(user); m != nil {
		f.lastPart, _ = strconv.Atoi(m[1])
	}
	return f.lastPart
}

func (f *scriptLLM) Complete(_ context.Context, system, user string) (*llm.Response, error) {
	f.calls = append(f.calls, llmCall{system, user})
	k := f.kind(user)
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

// sampleFiles: a modified Go file, an added test file and a vendored file
// the default ignore glob filters.
func sampleFiles() []provider.FilePatch {
	return []provider.FilePatch{
		{
			Path: "src/app.go", Type: provider.ChangeModified, Additions: 2, Deletions: 1,
			Patch: "@@ -10,3 +10,4 @@\n line 10\n-retries := 1\n+retries := 3 // " + diffMarker +
				"\n+delay := retries * 100\n line 12\n",
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

func comment(author provider.User, minutes int, body string) provider.CommentItem {
	at := time.Date(2026, 10, 8, 9, minutes, 0, 0, time.UTC)
	return provider.CommentItem{ID: "c" + strconv.Itoa(minutes), Author: author.Name, Body: body, CreatedAt: at,
		UpdatedAt: at, AuthorID: author.ID, AuthorLogin: author.Name}
}

// sampleThreads: a human inline thread, which reaches the prompt, and a
// comment of ours with a tool marker, which does not.
func sampleThreads() []provider.Thread {
	alice := provider.User{ID: "11", Name: "alice"}
	resolved := false
	return []provider.Thread{
		{ID: "1", Kind: provider.ThreadInline, Path: "src/app.go", Line: 11, Resolved: &resolved,
			Comments: []provider.CommentItem{comment(alice, 0, "Please do not hardcode the retry count. "+commentMarker)}},
		{ID: "2", Kind: provider.ThreadGeneral,
			Comments: []provider.CommentItem{comment(botUser, 5, "## PR Review\n\nOUR-OWN-OVERVIEW\n\n[//]: # (review-mcp:overview:v1)")}},
	}
}

func newHarness(answers map[int][]string) *harness {
	h := &harness{logs: &bytes.Buffer{}}
	h.prov = &fakeProvider{
		pr: provider.PullRequest{Title: "Retry more " + titleMarker, Description: "Raises the retry count. " + descMarker,
			SourceBranch: "feature/retry", TargetBranch: "main", HeadSHA: "abc"},
		files:   sampleFiles(),
		threads: sampleThreads(),
	}
	if answers == nil {
		answers = map[int][]string{}
	}
	h.llm = &scriptLLM{answers: answers, errs: map[int]error{}, truncated: map[int]bool{}}
	h.deps = Deps{
		Config:   testConfig(),
		Logger:   slog.New(slog.NewTextHandler(h.logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
		Resolver: &fakeResolver{p: h.prov},
		LLM:      h.llm,
		Clock:    fixedClock{},
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
	for _, m := range []string{titleMarker, descMarker, diffMarker, answerMarker, commentMarker} {
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

// sug is one suggestion of a scripted answer.
type sug struct {
	file, lang, existing, content, improved, summary, label string
}

func block(v string) string {
	return strings.ReplaceAll(strings.TrimSuffix(v, "\n"), "\n", "\n    ")
}

// suggestionsAnswer is a suggestion answer with the given suggestions.
func suggestionsAnswer(ss ...sug) string {
	if len(ss) == 0 {
		return "```yaml\ncode_suggestions: []\n```\n"
	}
	var b strings.Builder
	b.WriteString("```yaml\ncode_suggestions:\n")
	for _, s := range ss {
		b.WriteString("- relevant_file: |\n    " + s.file + "\n" +
			"  language: |\n    " + s.lang + "\n" +
			"  existing_code: |\n    " + block(s.existing) + "\n" +
			"  suggestion_content: |\n    " + block(s.content) + "\n" +
			"  improved_code: |\n    " + block(s.improved) + "\n" +
			"  one_sentence_summary: |\n    " + s.summary + "\n" +
			"  label: |\n    " + s.label + "\n")
	}
	b.WriteString("```\n")
	return b.String()
}

// fb is one self-review entry; number 0 leaves suggestion_number out and
// score -1 leaves suggestion_score out.
type fb struct {
	number      int
	summary     string
	file        string
	start, end  int
	score       int
	why         string
	omitSummary bool
}

func reflectionAnswer(fs ...fb) string {
	if len(fs) == 0 {
		return "```yaml\ncode_suggestions: []\n```\n"
	}
	var b strings.Builder
	b.WriteString("```yaml\ncode_suggestions:\n")
	for _, f := range fs {
		b.WriteString("- ")
		if f.number != 0 {
			b.WriteString("suggestion_number: " + strconv.Itoa(f.number) + "\n  ")
		}
		if !f.omitSummary {
			b.WriteString("suggestion_summary: |\n    " + f.summary + "\n  ")
		}
		b.WriteString("relevant_file: \"" + f.file + "\"\n")
		b.WriteString("  relevant_lines_start: " + strconv.Itoa(f.start) + "\n")
		b.WriteString("  relevant_lines_end: " + strconv.Itoa(f.end) + "\n")
		if f.score >= 0 {
			b.WriteString("  suggestion_score: " + strconv.Itoa(f.score) + "\n")
		}
		b.WriteString("  why: |\n    " + f.why + "\n")
	}
	b.WriteString("```\n")
	return b.String()
}

// The three suggestions of the one-call run.
var (
	sugDelay = sug{"src/app.go", "go", "delay := retries * 100", "Cap the delay so that a large retry count cannot stall the caller.",
		"delay := min(retries*100, 1000)", "Cap the retry delay", "possible issue"}
	sugName = sug{"src/app.go", "go", "retries := 3 // " + diffMarker, "Name the retry count " + answerMarker + ".",
		"const maxRetries = 3", "Name the retry count", "general"}
	sugTest = sug{"src/app_test.go", "go", "func TestRetries(t *testing.T) {}", "The test asserts nothing; check the retry count.",
		"func TestRetries(t *testing.T) {\n\tif retries != 3 {\n\t\tt.Fatal(\"retries\")\n\t}\n}", "Assert the retry count", "critical bug"}
)

// oneCallAnswers: three suggestions; the self-review scores the second
// below improve.min_score (7).
func oneCallAnswers() map[int][]string {
	return map[int][]string{
		0: {suggestionsAnswer(sugDelay, sugName, sugTest)},
		reflectKind(0): {reflectionAnswer(
			fb{number: 1, summary: "Cap the retry delay", file: "src/app.go", start: 12, end: 12, score: 8, why: "An unbounded `delay` can stall callers."},
			fb{number: 2, summary: "Name the retry count", file: "src/app.go", start: 11, end: 11, score: 4, why: "Style only."},
			fb{number: 3, summary: "Assert the retry count", file: "src/app_test.go", start: 2, end: 2, score: 9, why: "The test cannot fail."},
		)},
	}
}

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

func scoreOfSuggestion(s Suggestion) string {
	if s.Score == nil {
		return "null"
	}
	return strconv.Itoa(*s.Score)
}

// summaries lists "summary=score" of the suggestions, in order.
func summaries(res *Result) []string {
	var out []string
	for _, s := range res.Suggestions {
		out = append(out, s.Summary+"="+scoreOfSuggestion(s))
	}
	return out
}

// TestOneCallRun: a pull request whose diff fits one call gets one
// suggestion call and one self-review call on the same numbered diff; the
// result, the prompts and the stages are pinned by goldens
// (testdata/runs/one_call).
func TestOneCallRun(t *testing.T) {
	h := newHarness(oneCallAnswers())
	res := h.run(t, Args{})

	if !slices.Equal(h.llm.kinds, []int{0, reflectKind(0)}) {
		t.Fatalf("calls %v, want the suggestion call then its self-review", h.llm.kinds)
	}
	sc, rc := h.llm.calls[0], h.llm.calls[1]
	for _, m := range []string{titleMarker, descMarker, diffMarker, commentMarker, "Today's Date: 2026-10-09",
		"Branch: 'feature/retry'", "Target branch: 'main'", "Provide up to 4 distinct"} {
		if !strings.Contains(sc.user+sc.system, m) {
			t.Errorf("the suggestion call lacks %q", m)
		}
	}
	if strings.Contains(sc.user, "OUR-OWN-OVERVIEW") || strings.Contains(sc.user, "vendor/lib.go") {
		t.Errorf("our own comment or the filtered file reached the model")
	}
	// The self-review gets the same numbered diff and the three suggestions.
	diff := between(t, sc.user, "The PR Diff:\n======\n", "\n======")
	if !strings.Contains(diff, "__new hunk__\n10  line 10\n11 +retries := 3") ||
		between(t, rc.user, "code diff:\n======\n", "\n======") != diff {
		t.Errorf("the self-review diff is not the suggestion call's numbered diff")
	}
	if !strings.Contains(rc.user, "Below are 3 AI-generated code suggestions") ||
		!strings.Contains(rc.user, `suggestion 3: {"relevant_file":"src/app_test.go","language":"go",`) ||
		!strings.Contains(rc.user, `"label":"possible issue"}`) {
		t.Errorf("the self-review suggestion list is wrong:\n%s", rc.user)
	}
	if got := summaries(res); !slices.Equal(got, []string{"Assert the retry count=9", "Cap the retry delay=8"}) {
		t.Errorf("suggestions = %v", got)
	}
	s := res.Suggestions[1]
	if s.StartLine == nil || *s.StartLine != 12 || *s.EndLine != 12 || s.Verified || s.Anchor != nil ||
		s.Why != "An unbounded `delay` can stall callers." || s.Label != "possible issue" {
		t.Errorf("suggestion = %+v", s)
	}
	if res.Suggestions[0].Label != "possible issue" {
		t.Errorf("a critical label was not softened: %q", res.Suggestions[0].Label)
	}
	cov := res.Coverage
	if cov.Partial || cov.ReviewedFiles != 2 || cov.TotalFiles != 2 || cov.ModelCalls != 1 || len(cov.Filtered) != 1 {
		t.Errorf("coverage = %+v", cov)
	}
	if m := res.Metadata; m.LLMCalls != 2 || m.SelfReviewCalls != 1 || m.RepairTactic != "direct" || !m.FastPath ||
		m.AlreadyDiscussed != 1 {
		t.Errorf("metadata = %+v", m)
	}
	if want := []string{"1 suggestion was dropped by the self-review score (below 7)."}; !slices.Equal(res.Notes, want) {
		t.Errorf("notes = %q", res.Notes)
	}
	if want := []string{StageFetching, StagePreparingDiff, StageCallingModel, StageScoring}; !slices.Equal(h.stages, want) {
		t.Errorf("stages = %v", h.stages)
	}

	dir := "testdata/runs/one_call"
	checkGolden(t, dir+"/system.txt", sc.system)
	checkGolden(t, dir+"/user.txt", sc.user)
	checkGolden(t, dir+"/reflect.system.txt", rc.system)
	checkGolden(t, dir+"/reflect.user.txt", rc.user)
	checkGolden(t, dir+"/result.json", marshal(t, res))
}

// between returns the text of s between the first from and the next to.
func between(t *testing.T, s, from, to string) string {
	t.Helper()
	_, rest, ok := strings.Cut(s, from)
	if !ok {
		t.Fatalf("%q not found", from)
	}
	in, _, ok := strings.Cut(rest, to)
	if !ok {
		t.Fatalf("%q not found after %q", to, from)
	}
	return in
}

// threePartHarness: six files of about 800 tokens each in the numbered
// diff with diff.max_tokens 2000, so the run takes three parts of two files
// (pr_review's chunked tests use the same sizes).
func threePartHarness(t *testing.T) *harness {
	t.Helper()
	h := newHarness(nil)
	h.prov.files = bigFiles(6)
	h.prov.threads = nil
	capTokens := 2000
	h.deps.Config.Diff.MaxTokens = &capTokens
	for i := 1; i <= 3; i++ {
		var ss []sug
		var fs []fb
		for j, f := range partFiles(i) {
			s := partSuggestion(f)
			ss = append(ss, s)
			// Part 2 scores its second suggestion below the threshold.
			score := 9 - j
			if i == 2 && j == 1 {
				score = 3
			}
			fs = append(fs, fb{number: j + 1, summary: s.summary, file: f, start: 2, end: 2, score: score, why: "Checked."})
		}
		h.llm.answers[i] = []string{suggestionsAnswer(ss...)}
		h.llm.answers[reflectKind(i)] = []string{reflectionAnswer(fs...)}
	}
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

func partSuggestion(file string) sug {
	base := strings.TrimSuffix(strings.TrimPrefix(file, "src/"), ".go")
	return sug{file, "go", "some added line of code here", "Explain the line in `" + base + "`.",
		"some added line of code here // " + base, "Annotate the line in " + base, "general"}
}

// TestThreePartRun: a pull request that needs three parts gets three
// suggestion calls, each followed by the self-review of that part's diff and
// suggestions only; the merge keeps part order, drops the low score and
// counts it. Pinned by goldens (testdata/runs/three_parts).
func TestThreePartRun(t *testing.T) {
	h := threePartHarness(t)
	res := h.run(t, Args{})

	want := []int{1, reflectKind(1), 2, reflectKind(2), 3, reflectKind(3)}
	if !slices.Equal(h.llm.kinds, want) {
		t.Fatalf("calls %v, want %v", h.llm.kinds, want)
	}
	for i := 1; i <= 3; i++ {
		sc, rc := h.llm.calls[2*(i-1)], h.llm.calls[2*(i-1)+1]
		if !strings.Contains(sc.user, PartHeader(i, 3)+"\n\nThe PR Diff:\n") {
			t.Errorf("part %d: the part line is not right before the diff", i)
		}
		if between(t, rc.user, "code diff:\n======\n", "\n======") != between(t, sc.user, "The PR Diff:\n======\n", "\n======") {
			t.Errorf("part %d: the self-review diff is not the part's diff", i)
		}
		for j := 1; j <= 3; j++ {
			for _, f := range partFiles(j) {
				if got := strings.Contains(sc.user, "## File: '"+f+"'"); got != (i == j) {
					t.Errorf("part %d: file %s in the prompt = %v", i, f, got)
				}
				if got := strings.Contains(rc.user, `"relevant_file":"`+f+`"`); got != (i == j) {
					t.Errorf("part %d: suggestion for %s in the self-review = %v", i, f, got)
				}
			}
		}
	}
	if want := []string{StageFetching, StagePreparingDiff, CallingModelPart(1, 3), ScoringPart(1, 3),
		CallingModelPart(2, 3), ScoringPart(2, 3), CallingModelPart(3, 3), ScoringPart(3, 3)}; !slices.Equal(h.stages, want) {
		t.Errorf("stages = %v", h.stages)
	}
	if got := summaries(res); !slices.Equal(got, []string{"Annotate the line in f00=9", "Annotate the line in f01=8",
		"Annotate the line in f02=9", "Annotate the line in f04=9", "Annotate the line in f05=8"}) {
		t.Errorf("suggestions = %v", got)
	}
	if c := res.Coverage; c.ModelCalls != 3 || c.Partial || c.ReviewedFiles != 6 || res.Metadata.LLMCalls != 6 ||
		res.Metadata.SelfReviewCalls != 3 {
		t.Errorf("coverage %+v metadata %+v", c, res.Metadata)
	}
	if !slices.Contains(res.Notes, "1 suggestion was dropped by the self-review score (below 7).") {
		t.Errorf("notes = %q", res.Notes)
	}

	dir := "testdata/runs/three_parts"
	for i, c := range h.llm.calls {
		name := "part" + strconv.Itoa(i/2+1)
		if i%2 == 1 {
			name += ".reflect"
		}
		checkGolden(t, dir+"/"+name+".system.txt", c.system)
		checkGolden(t, dir+"/"+name+".user.txt", c.user)
	}
	checkGolden(t, dir+"/result.json", marshal(t, res))
}

// TestReflectionDropsBelowMinScore [canary target]: a suggestion whose
// self-review score is below improve.min_score is dropped and counted; a
// score equal to it is kept. min_score 0 keeps every scored suggestion.
func TestReflectionDropsBelowMinScore(t *testing.T) {
	answers := func(scores ...int) map[int][]string {
		var fs []fb
		for i, s := range []sug{sugDelay, sugName, sugTest} {
			fs = append(fs, fb{number: i + 1, summary: s.summary, file: s.file, start: 1, end: 1, score: scores[i], why: "w"})
		}
		return map[int][]string{0: {suggestionsAnswer(sugDelay, sugName, sugTest)}, reflectKind(0): {reflectionAnswer(fs...)}}
	}
	h := newHarness(answers(7, 6, 0))
	res := h.run(t, Args{})
	if got := summaries(res); !slices.Equal(got, []string{"Cap the retry delay=7"}) {
		t.Errorf("suggestions = %v, want only the one scored 7", got)
	}
	if !slices.Contains(res.Notes, "2 suggestions were dropped by the self-review score (below 7).") {
		t.Errorf("notes = %q", res.Notes)
	}

	h = newHarness(answers(7, 6, 0))
	h.deps.Config.Improve.MinScore = 0
	res = h.run(t, Args{})
	if got := summaries(res); !slices.Equal(got, []string{"Cap the retry delay=7", "Name the retry count=6", "Assert the retry count=0"}) {
		t.Errorf("min_score 0: suggestions = %v", got)
	}
	for _, n := range res.Notes {
		if strings.Contains(n, "dropped by the self-review score") {
			t.Errorf("min_score 0: note %q", n)
		}
	}

	h = newHarness(answers(9, 2, 9))
	h.deps.Config.Improve.MinScore = 3
	res = h.run(t, Args{})
	if !slices.Contains(res.Notes, "1 suggestion was dropped by the self-review score (below 3).") || len(res.Suggestions) != 2 {
		t.Errorf("min_score 3: suggestions %v notes %q", summaries(res), res.Notes)
	}
}

// TestReflectionFailureKeepsUnscored [canary target, honesty]: when the
// self-review call fails (an error, an answer that is unusable after the
// re-ask, or a request that does not fit), every suggestion of the call is
// kept with a null score and the fixed note; nothing is dropped by score.
func TestReflectionFailureKeepsUnscored(t *testing.T) {
	for name, set := range map[string]func(h *harness){
		"error":       func(h *harness) { h.llm.errs[reflectKind(0)] = &llm.Error{Class: llm.ClassTimeout} },
		"unparseable": func(h *harness) { h.llm.answers[reflectKind(0)] = []string{"no yaml here", "still: [none"} },
		"does not fit": func(h *harness) {
			// Suggestions far larger than the room the diff budget keeps
			// for them.
			big := sugDelay
			big.improved = strings.Repeat("x := 1 // padding text\n", 4000)
			h.llm.answers[0] = []string{suggestionsAnswer(big, sugName, sugTest)}
		},
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(oneCallAnswers())
			set(h)
			res := h.run(t, Args{})
			if len(res.Suggestions) != 3 {
				t.Fatalf("suggestions = %v, want all three kept", summaries(res))
			}
			for _, s := range res.Suggestions {
				if s.Score != nil || s.Why != "" || s.StartLine != nil {
					t.Errorf("suggestion %q is not unscored: %+v", s.Summary, s)
				}
			}
			if !slices.Contains(res.Notes, NoteNotScoredOneCall) {
				t.Errorf("notes = %q", res.Notes)
			}
			raw := marshal(t, res)
			if strings.Count(raw, `"score": null`) != 3 {
				t.Errorf("JSON does not carry null scores:\n%s", raw)
			}
			switch name {
			case "unparseable":
				if h.llm.seen[reflectKind(0)] != 2 || res.Metadata.SelfReviewCalls != 2 || res.Metadata.LLMCalls != 3 {
					t.Errorf("self-review calls = %d (metadata %+v), want the call and its re-ask", h.llm.seen[reflectKind(0)], res.Metadata)
				}
			case "does not fit":
				if h.llm.seen[reflectKind(0)] != 0 || res.Metadata.SelfReviewCalls != 0 {
					t.Errorf("a self-review request that cannot fit was sent")
				}
			}
		})
	}

	// In parts, the note names the part, and the other parts are scored.
	h := threePartHarness(t)
	h.llm.errs[reflectKind(2)] = &llm.Error{Class: llm.ClassTimeout}
	res := h.run(t, Args{})
	if !slices.Contains(res.Notes, "Part 2's suggestions were not scored (the self-review call failed).") {
		t.Errorf("notes = %q", res.Notes)
	}
	if got := summaries(res); !slices.Equal(got, []string{"Annotate the line in f00=9", "Annotate the line in f01=8",
		"Annotate the line in f02=null", "Annotate the line in f03=null", "Annotate the line in f04=9", "Annotate the line in f05=8"}) {
		t.Errorf("suggestions = %v", got)
	}
}

// TestReflectionSkippedSuggestion: a suggestion the self-review answer does
// not mention is kept unscored, never dropped, and counted in a note.
func TestReflectionSkippedSuggestion(t *testing.T) {
	h := newHarness(oneCallAnswers())
	h.llm.answers[reflectKind(0)] = []string{reflectionAnswer(
		fb{number: 1, summary: "Cap the retry delay", file: "src/app.go", start: 12, end: 12, score: 8, why: "w"},
		fb{number: 3, summary: "Assert the retry count", file: "src/app_test.go", start: 2, end: 2, score: 9, why: "w"},
	)}
	res := h.run(t, Args{})
	if got := summaries(res); !slices.Equal(got, []string{"Assert the retry count=9", "Cap the retry delay=8", "Name the retry count=null"}) {
		t.Errorf("suggestions = %v", got)
	}
	if !slices.Contains(res.Notes, "1 suggestion got no usable self-review score and is kept unscored.") {
		t.Errorf("notes = %q", res.Notes)
	}
}

// TestReflectionMatching: entries are matched by suggestion_number and
// checked against the file and the summary; conflicting, out-of-range and
// mismatched entries match nothing; without numbers, an answer with as many
// entries as suggestions is matched by position.
func TestReflectionMatching(t *testing.T) {
	cands := []Candidate{
		{File: "a.go", Summary: "Check the error"},
		{File: "b.go", Summary: "Close the file"},
		{File: "a.go", Summary: "Guard the index"},
	}
	entry := func(num any, file, summary string, score int) map[string]any {
		e := map[string]any{"relevant_file": file, "suggestion_summary": summary, "suggestion_score": score,
			"why": "w", "relevant_lines_start": 3, "relevant_lines_end": 4}
		if num != nil {
			e["suggestion_number"] = num
		}
		return e
	}
	scores := func(r *reflection) []string {
		var out []string
		for _, f := range r.fb {
			if f == nil || f.score == nil {
				out = append(out, "-")
			} else {
				out = append(out, strconv.Itoa(*f.score))
			}
		}
		return out
	}
	for name, tc := range map[string]struct {
		entries   []any
		want      []string
		unmatched int
	}{
		"by number, any order": {[]any{entry(3, "a.go", "Guard the index", 9), entry("1", "a.go", "check the error.", 8),
			entry(2.0, "b.go", "Close  the file", 7)}, []string{"8", "7", "9"}, 0},
		"conflicting numbers": {[]any{entry(1, "a.go", "Check the error", 8), entry(1, "a.go", "Check the error", 2),
			entry(2, "b.go", "Close the file", 7)}, []string{"-", "7", "-"}, 2},
		"out of range": {[]any{entry(4, "a.go", "Check the error", 8), entry(0, "a.go", "Check the error", 8)},
			[]string{"-", "-", "-"}, 2},
		"file mismatch": {[]any{entry(2, "a.go", "Close the file", 8)}, []string{"-", "-", "-"}, 1},
		"summary mismatch": {[]any{entry(1, "a.go", "Guard the index", 8), entry(3, "a.go", "Guard the index", 6)},
			[]string{"-", "-", "6"}, 1},
		"positional fallback": {[]any{entry(nil, "a.go", "Check the error", 8), entry(nil, "b.go", "Close the file", 7),
			entry(nil, "a.go", "Guard the index", 9)}, []string{"8", "7", "9"}, 0},
		"no fallback when the counts differ": {[]any{entry(nil, "a.go", "Check the error", 8)}, []string{"-", "-", "-"}, 1},
		"score out of range": {[]any{entry(1, "a.go", "Check the error", 11), entry(2, "b.go", "Close the file", -1)},
			[]string{"-", "-", "-"}, 0},
		"not a mapping": {[]any{"suggestion 1 is fine"}, []string{"-", "-", "-"}, 1},
	} {
		r, err := convertReflection(map[string]any{"code_suggestions": tc.entries}, cands)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got := scores(r); !slices.Equal(got, tc.want) || r.unmatched != tc.unmatched {
			t.Errorf("%s: scores %v unmatched %d, want %v and %d", name, got, r.unmatched, tc.want, tc.unmatched)
		}
	}
	// Lines: a range needs 1 <= start <= end.
	r, _ := convertReflection(map[string]any{"code_suggestions": []any{
		map[string]any{"suggestion_number": 1, "suggestion_score": 8, "relevant_lines_start": 5, "relevant_lines_end": 4},
		map[string]any{"suggestion_number": 2, "suggestion_score": 8, "relevant_lines_start": 4, "relevant_lines_end": 4},
	}}, cands)
	if r.fb[0].start != nil || r.fb[1].start == nil || *r.fb[1].start != 4 {
		t.Errorf("line ranges = %+v %+v", r.fb[0], r.fb[1])
	}
	if _, err := convertReflection(map[string]any{"feedback": []any{}}, cands); err == nil {
		t.Errorf("an answer without code_suggestions was accepted")
	}
}

// TestDedup: a suggestion whose fingerprint (file, summary, existing code)
// an earlier one has is dropped and counted: within one answer, and in the
// merge across parts. Across parts of a real run the per-part file check
// already drops a suggestion for another part's file.
func TestDedup(t *testing.T) {
	dup := sugDelay
	dup.summary = "CAP the retry   delay"
	dup.content = "Another wording of the same point."
	h := newHarness(map[int][]string{0: {suggestionsAnswer(sugDelay, dup, sugTest)}, reflectKind(0): {reflectionAnswer(
		fb{number: 1, summary: sugDelay.summary, file: sugDelay.file, start: 12, end: 12, score: 8, why: "w"},
		fb{number: 2, summary: dup.summary, file: dup.file, start: 12, end: 12, score: 9, why: "w"},
		fb{number: 3, summary: sugTest.summary, file: sugTest.file, start: 2, end: 2, score: 7, why: "w"},
	)}})
	res := h.run(t, Args{})
	// The duplicate scored higher, so it comes first and the other is
	// dropped.
	if got := summaries(res); !slices.Equal(got, []string{"CAP the retry delay=9", "Assert the retry count=7"}) {
		t.Errorf("suggestions = %v", got)
	}
	if !slices.Contains(res.Notes, "1 duplicate suggestion was dropped; the first of each is kept.") {
		t.Errorf("notes = %q", res.Notes)
	}

	// The merge across parts: the same suggestion in parts 1 and 3 is kept
	// once, in part 1's place.
	pl := &Plan{Result: &Result{Notes: []string{}}, maxTotal: 8, minScore: 7, log: slog.New(slog.DiscardHandler)}
	nine, eight := 9, 8
	c := Candidate{File: "src/x.go", Summary: "Fix it", ExistingCode: "a := b"}
	other := Candidate{File: "src/y.go", Summary: "Fix that", ExistingCode: "c := d"}
	pl.merge([]*partOutcome{
		{kept: []scored{{part: 0, c: c, fb: &feedback{score: &eight}}}},
		{kept: []scored{{part: 1, c: other, fb: &feedback{score: &nine}}}},
		{kept: []scored{{part: 2, c: c, fb: &feedback{score: &nine}}}},
	})
	if got := summaries(pl.Result); !slices.Equal(got, []string{"Fix it=8", "Fix that=9"}) ||
		!slices.Contains(pl.Result.Notes, "1 duplicate suggestion was dropped; the first of each is kept.") {
		t.Errorf("merged = %v notes %q", got, pl.Result.Notes)
	}

	// In a real run, part 1 suggesting part 2's file is dropped as unknown.
	h = threePartHarness(t)
	h.llm.answers[1] = []string{suggestionsAnswer(partSuggestion("src/f00.go"), partSuggestion("src/f01.go"), partSuggestion("src/f02.go"))}
	res = h.run(t, Args{})
	n := 0
	for _, s := range res.Suggestions {
		if s.File == "src/f02.go" {
			n++
		}
	}
	if n != 1 || !slices.Contains(res.Notes, "1 suggestion for a file that was not in the diff shown to the model was dropped.") {
		t.Errorf("src/f02.go suggestions = %d, notes %q", n, res.Notes)
	}
}

// TestCap: the merged suggestions are capped at improve.max_suggestions,
// with a note for the rest.
func TestCap(t *testing.T) {
	h := threePartHarness(t)
	h.deps.Config.Improve.MaxSuggestions = 2
	res := h.run(t, Args{})
	if got := summaries(res); !slices.Equal(got, []string{"Annotate the line in f00=9", "Annotate the line in f01=8"}) {
		t.Errorf("suggestions = %v", got)
	}
	if !slices.Contains(res.Notes, "3 further suggestions were not shown because of improve.max_suggestions.") {
		t.Errorf("notes = %q", res.Notes)
	}
	h = threePartHarness(t)
	h.deps.Config.Improve.MaxSuggestions = 4
	res = h.run(t, Args{})
	if len(res.Suggestions) != 4 || !slices.Contains(res.Notes, "1 further suggestion was not shown because of improve.max_suggestions.") {
		t.Errorf("suggestions %v notes %q", summaries(res), res.Notes)
	}
}

// TestValidationDrops: a suggestion for a file the call was not shown, one
// whose improved code equals the existing code after whitespace
// normalisation, and one without its required fields are dropped and
// counted; a label is one line of at most 40 characters.
func TestValidationDrops(t *testing.T) {
	unknown := sugDelay
	unknown.file = "vendor/lib.go"
	unknown.summary = "Fix the vendored file"
	noChange := sugName
	noChange.improved = "retries  :=   3 //\t" + diffMarker + "\n"
	longLabel := sugTest
	longLabel.label = "a label that is much longer than forty characters in total"
	// The last entry has no code: it is incomplete.
	answer := strings.Replace(suggestionsAnswer(unknown, noChange, longLabel), "```\n",
		"- relevant_file: src/app.go\n  one_sentence_summary: No code\n```\n", 1)
	h := newHarness(map[int][]string{0: {answer}, reflectKind(0): {reflectionAnswer(
		fb{number: 1, summary: longLabel.summary, file: longLabel.file, start: 2, end: 2, score: 8, why: "w"})}})
	res := h.run(t, Args{})
	if len(res.Suggestions) != 1 {
		t.Fatalf("suggestions = %v", summaries(res))
	}
	if l := res.Suggestions[0].Label; l != "a label that is much longer than forty c" || len([]rune(l)) != MaxLabelRunes {
		t.Errorf("label = %q", l)
	}
	want := []string{
		"1 suggestion for a file that was not in the diff shown to the model was dropped.",
		"1 suggestion without a file, a summary, or the existing or improved code was dropped.",
		"1 suggestion whose improved code is the same as the existing code was dropped (no change).",
	}
	if !slices.Equal(res.Notes, want) {
		t.Errorf("notes =\n%s\nwant\n%s", strings.Join(res.Notes, "\n"), strings.Join(want, "\n"))
	}
	// The self-review saw only the valid suggestion.
	if !strings.Contains(h.llm.calls[1].user, "Below are 1 AI-generated code suggestions") {
		t.Errorf("the self-review was sent dropped suggestions")
	}
	// A multi-line label is folded to one line.
	if got := label(oneLine("security\nissue")); got != "security issue" {
		t.Errorf("label = %q", got)
	}
}

// TestNoSuggestions: an empty list is an answer; no self-review call is
// made and the result has no suggestion and no note.
func TestNoSuggestions(t *testing.T) {
	for _, answer := range []string{suggestionsAnswer(), "```yaml\ncode_suggestions:\n```\n"} {
		h := newHarness(map[int][]string{0: {answer}})
		res := h.run(t, Args{})
		if len(res.Suggestions) != 0 || len(h.llm.calls) != 1 || len(res.Notes) != 0 || res.Metadata.SelfReviewCalls != 0 {
			t.Errorf("%q: suggestions %v calls %d notes %q", answer, summaries(res), len(h.llm.calls), res.Notes)
		}
		if want := []string{StageFetching, StagePreparingDiff, StageCallingModel}; !slices.Equal(h.stages, want) {
			t.Errorf("stages = %v", h.stages)
		}
	}
}

// TestUnparseableOneCall: an answer without the code_suggestions list gets
// one re-ask; a second unusable answer fails the run with the fixed
// sentence.
func TestUnparseableOneCall(t *testing.T) {
	h := newHarness(map[int][]string{0: {"just prose " + answerMarker, "suggestions: none " + answerMarker}})
	_, err := Run(context.Background(), h.deps, Args{PRURL: testPRURL})
	if !errors.Is(err, ErrUnparseable) || len(h.llm.calls) != 2 || !strings.Contains(h.llm.calls[1].user, ReaskNote+"\n"+ResponseLine) {
		t.Fatalf("err = %v, calls = %d", err, len(h.llm.calls))
	}
	h.checkNoLeaks(t, err)

	answers := oneCallAnswers()
	answers[0] = append([]string{"just prose"}, answers[0]...)
	h = newHarness(answers)
	res := h.run(t, Args{})
	if !res.Metadata.Reasked || res.Metadata.LLMCalls != 3 || !slices.Contains(res.Notes, NoteReasked) {
		t.Errorf("metadata %+v notes %v", res.Metadata, res.Notes)
	}
}

// TestFailedPart: a part whose suggestion call fails makes its files not
// reviewed (model_call_failed) with the fixed class note; the other parts
// and their self-reviews still run.
func TestFailedPart(t *testing.T) {
	h := threePartHarness(t)
	h.llm.errs[2] = &llm.Error{Class: llm.ClassTimeout}
	res := h.run(t, Args{})
	c := res.Coverage
	if c.FailedParts != 1 || c.ModelCalls != 3 || !c.Partial || c.ReviewedFiles != 4 || c.NotReviewedFiles != 2 {
		t.Errorf("coverage = %+v", c)
	}
	if !slices.Contains(res.Notes, "Part 2 of 3 failed (llm_timeout); its files were not reviewed.") {
		t.Errorf("notes = %v", res.Notes)
	}
	if h.llm.seen[reflectKind(2)] != 0 || h.llm.seen[reflectKind(3)] != 1 {
		t.Errorf("self-review calls = %v", h.llm.seen)
	}
	if got := summaries(res); !slices.Equal(got, []string{"Annotate the line in f00=9", "Annotate the line in f01=8",
		"Annotate the line in f04=9", "Annotate the line in f05=8"}) {
		t.Errorf("suggestions = %v", got)
	}

	// Every part failing fails the run with the first part's error.
	h = threePartHarness(t)
	for i := 1; i <= 3; i++ {
		h.llm.errs[i] = &llm.Error{Class: llm.ClassTimeout}
	}
	_, err := Run(context.Background(), h.deps, Args{PRURL: testPRURL})
	var le *llm.Error
	if !errors.As(err, &le) || le.Class != llm.ClassTimeout {
		t.Fatalf("err = %v", err)
	}
}

// TestDiscussion: the human threads reach the suggestion prompt after the
// diff under the improve header; comments of ours with a tool marker do
// not, and the self-review prompt carries no discussion. An unreadable
// discussion is a note, not a failure.
func TestDiscussion(t *testing.T) {
	h := newHarness(oneCallAnswers())
	h.run(t, Args{})
	sc, rc := h.llm.calls[0].user, h.llm.calls[1].user
	i, j := strings.Index(sc, "The PR Diff:"), strings.Index(sc, DiscussionHeader)
	if j < 0 || j < i || !strings.Contains(sc, "[inline src/app.go:11]\nPlease do not hardcode") {
		t.Errorf("the discussion is missing or before the diff")
	}
	if strings.Contains(rc, commentMarker) || strings.Contains(rc, DiscussionHeader) {
		t.Errorf("the self-review prompt carries the discussion")
	}

	h = newHarness(oneCallAnswers())
	h.prov.threadsErr = &provider.Error{Class: provider.ClassAuth, Status: 403}
	res := h.run(t, Args{})
	if strings.Contains(h.llm.calls[0].user, DiscussionHeader) || !slices.Contains(res.Notes, NoteDiscussionUnreadable) {
		t.Errorf("notes = %q", res.Notes)
	}

	h = newHarness(oneCallAnswers())
	h.deps.Config.Review.MaxDiscussionTokens = 0
	h.run(t, Args{})
	if slices.Contains(h.prov.calls, "threads") {
		t.Errorf("the discussion was read with review.max_discussion_tokens 0")
	}
}

// TestRepoContext: with context.repo.enabled, the uses of the changed
// symbols reach the suggestion prompt (never the self-review prompt), and
// the coverage reports them.
func TestRepoContext(t *testing.T) {
	h := newHarness(map[int][]string{0: {suggestionsAnswer()}})
	h.prov.files = []provider.FilePatch{{Path: "pkg/a.go", Type: provider.ChangeModified,
		Patch:      "@@ -1,2 +1,3 @@\n a\n+func Alpha() {}\n b\n",
		BaseStatus: provider.ContentNotFetchedSizeCap, HeadStatus: provider.ContentNotFetchedSizeCap}}
	h.deps.Config.Context.Repo.Enabled = true
	h.deps.RepoContext = &fakeRepo{hits: map[string][]gitctx.Hit{"Alpha": {{Symbol: "Alpha", Path: "pkg/use.go", Line: 7,
		StartLine: 6, Snippet: "before\nAlpha() // REPO-SNIPPET\nafter"}}}}
	res := h.run(t, Args{})
	if !strings.Contains(h.llm.calls[0].user, "REPO-SNIPPET") {
		t.Errorf("the repository context did not reach the prompt")
	}
	if rc := res.Coverage.RepoContext; rc.Status != llmrun.RepoUsed || rc.References != 1 {
		t.Errorf("repo context = %+v", rc)
	}
	if want := []string{StageFetching, StageRepoContext, StagePreparingDiff, StageCallingModel}; !slices.Equal(h.stages, want) {
		t.Errorf("stages = %v", h.stages)
	}
}

// fakeRepo is the git backend of repository context (repoctx.Backend).
type fakeRepo struct {
	hits map[string][]gitctx.Hit
}

func (f *fakeRepo) Ensure(context.Context, gitctx.Repo, gitctx.PR) (gitctx.Checkout, error) {
	return gitctx.Checkout{GitDir: "/cache/x.git", HeadSHA: strings.Repeat("a", 40)}, nil
}

func (f *fakeRepo) Grep(_ context.Context, _ gitctx.Checkout, q gitctx.Query) (gitctx.Result, error) {
	res := gitctx.Result{Symbols: len(q.Symbols)}
	for _, s := range q.Symbols {
		res.Hits = append(res.Hits, f.hits[s.Name]...)
	}
	res.Files = len(res.Hits)
	return res, nil
}

// TestEmptyDiff: nothing left after filtering makes no model call.
func TestEmptyDiff(t *testing.T) {
	h := newHarness(oneCallAnswers())
	h.prov.files = h.prov.files[2:]
	res := h.run(t, Args{})
	if len(h.llm.calls) != 0 || len(res.Suggestions) != 0 || !slices.Contains(res.Notes, NoteNoReviewableChanges) {
		t.Errorf("result = %+v", res)
	}
}

// TestOutputLanguageEveryCall: output_language reaches every call, the
// self-review calls included.
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
