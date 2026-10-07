package ask

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/nevzatcirak/review-mcp/internal/config"
	"github.com/nevzatcirak/review-mcp/internal/llm"
	"github.com/nevzatcirak/review-mcp/internal/llmrun"
	"github.com/nevzatcirak/review-mcp/internal/provider"
	"github.com/nevzatcirak/review-mcp/internal/tokens"
)

// Markers that must reach the model but never the logs or errors (X-8).
const (
	titleMarker    = "TITLE-MARKER-7f3a"
	descMarker     = "DESC-MARKER-91c2"
	branchMarker   = "BRANCH-MARKER-0b6e"
	diffMarker     = "DIFF-MARKER-55e0"
	questionMarker = "QUESTION-MARKER-3d8f"
	answerMarker   = "ANSWER-MARKER-c4d1"
	secretMarker   = "FAKE-SECRET-5a2b" //nolint:gosec // fake marker for the leak checks
)

var allMarkers = []string{titleMarker, descMarker, branchMarker, diffMarker, questionMarker, answerMarker, secretMarker}

const testPRURL = "https://your-gitea.example/octo/demo/pulls/7"

// fakeProvider serves one PR. GetDiff applies the Include filter like the
// real providers.
type fakeProvider struct {
	provider.Provider // nil: unexpected methods panic

	pr      provider.PullRequest
	files   []provider.FilePatch
	postErr error

	calls  int
	posted []string
}

func (f *fakeProvider) Capabilities() provider.Capabilities { return provider.Capabilities{GFM: true} }

func (f *fakeProvider) GetPullRequest(context.Context, provider.PRRef) (*provider.PullRequest, error) {
	f.calls++
	pr := f.pr
	return &pr, nil
}

func (f *fakeProvider) GetDiff(_ context.Context, _ provider.PRRef, _ *provider.PullRequest, opts provider.DiffOptions) (*provider.Diff, error) {
	f.calls++
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

func (f *fakeProvider) PostComment(_ context.Context, _ provider.PRRef, body string) (*provider.Comment, error) {
	f.calls++
	f.posted = append(f.posted, body)
	if f.postErr != nil {
		return nil, f.postErr
	}
	return &provider.Comment{ID: "42", URL: "https://your-gitea.example/octo/demo/pulls/7#issuecomment-42"}, nil
}

type fakeResolver struct {
	p     *fakeProvider
	calls int
}

func (r *fakeResolver) Resolve(u string) (provider.PRRef, provider.Provider, error) {
	r.calls++
	return provider.PRRef{Kind: provider.KindGitea, Namespace: "octo", Repo: "demo", Number: 7, URL: u}, r.p, nil
}

type llmCall struct{ system, user string }

// fakeLLM answers from a script; each call takes the next answer.
type fakeLLM struct {
	answers   []string
	truncated []bool
	err       error
	calls     []llmCall
}

func (f *fakeLLM) Complete(_ context.Context, system, user string) (*llm.Response, error) {
	f.calls = append(f.calls, llmCall{system, user})
	if f.err != nil {
		return nil, f.err
	}
	i := min(len(f.calls)-1, len(f.answers)-1)
	r := &llm.Response{Content: f.answers[i], Usage: llm.Usage{PromptTokens: 100, CompletionTokens: 20}}
	if i < len(f.truncated) {
		r.Truncated = f.truncated[i]
	}
	return r, nil
}

// sampleFiles: src/app.go and src/util.go are reviewable (Go), vendor/lib.go
// is filtered by the default ignore glob.
func sampleFiles() []provider.FilePatch {
	return []provider.FilePatch{
		{
			Path: "src/app.go", Type: provider.ChangeModified, Additions: 1,
			Patch:      "@@ -10,3 +10,4 @@\n line 10\n+line 11 " + diffMarker + "\n line 12\n line 13\n",
			BaseStatus: provider.ContentNotFetchedSizeCap, HeadStatus: provider.ContentNotFetchedSizeCap,
		},
		{
			Path: "src/util.go", Type: provider.ChangeModified, Additions: 1,
			Patch:      "@@ -1,2 +1,3 @@\n a\n+b\n c\n",
			BaseStatus: provider.ContentNotFetchedSizeCap, HeadStatus: provider.ContentNotFetchedSizeCap,
		},
		{
			Path: "vendor/lib.go", Type: provider.ChangeAdded, Additions: 1,
			Patch:      "@@ -0,0 +1 @@\n+x\n",
			BaseStatus: provider.ContentNotApplicable, HeadStatus: provider.ContentNotFetchedSizeCap,
		},
	}
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
	deps     Deps
	prov     *fakeProvider
	resolver *fakeResolver
	llm      *fakeLLM
	logs     *bytes.Buffer
	rendered []*Result
}

func newHarness(answers ...string) *harness {
	h := &harness{logs: &bytes.Buffer{}}
	h.prov = &fakeProvider{
		pr: provider.PullRequest{Title: "Retry " + titleMarker, Description: "Adds a retry. " + descMarker,
			SourceBranch: "feature/" + branchMarker, HeadSHA: "abc"},
		files: sampleFiles(),
	}
	h.resolver = &fakeResolver{p: h.prov}
	h.llm = &fakeLLM{answers: answers}
	cfg := testConfig()
	cfg.Secrets.LLMAPIKey = config.NewSecret(secretMarker)
	cfg.Secrets.GiteaToken = config.NewSecret(secretMarker)
	h.deps = Deps{
		Config:   cfg,
		Logger:   slog.New(slog.NewTextHandler(h.logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
		Resolver: h.resolver,
		LLM:      h.llm,
		RenderProvider: func(r *Result, caps provider.Capabilities) string {
			h.rendered = append(h.rendered, r)
			return fmt.Sprintf("rendered answer=%q gfm=%v", r.Answer, caps.GFM)
		},
	}
	return h
}

func (h *harness) requests() int { return h.resolver.calls + h.prov.calls + len(h.llm.calls) }

func (h *harness) args() Args {
	return Args{PRURL: testPRURL, Question: "Is it bounded? " + questionMarker}
}

func (h *harness) checkNoLeaks(t *testing.T, errs ...error) {
	t.Helper()
	for _, m := range allMarkers {
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

func TestRunAnswers(t *testing.T) {
	h := newHarness("  It is bounded. " + answerMarker + "\n\n")
	res, err := Run(context.Background(), h.deps, h.args())
	if err != nil {
		t.Fatal(err)
	}
	if len(h.llm.calls) != 1 {
		t.Fatalf("LLM calls = %d", len(h.llm.calls))
	}
	call := h.llm.calls[0]
	for _, m := range []string{titleMarker, descMarker, branchMarker, diffMarker, questionMarker} {
		if !strings.Contains(call.user, m) {
			t.Errorf("marker %s did not reach the model", m)
		}
	}
	if !strings.Contains(call.user, "Main PR language: 'Go'") || !strings.HasSuffix(call.user, "Response to the PR Questions:") {
		t.Errorf("unexpected user prompt")
	}
	if !strings.Contains(call.system, "Do not guess.") {
		t.Errorf("grounding sentence missing from the system prompt")
	}

	if res.Answer != "It is bounded. "+answerMarker {
		t.Errorf("answer = %q (not trimmed)", res.Answer)
	}
	if res.Question != "Is it bounded? "+questionMarker {
		t.Errorf("question = %q", res.Question)
	}
	if !slices.Equal(res.Coverage.Included, []string{"src/app.go", "src/util.go"}) ||
		len(res.Coverage.Filtered) != 1 || res.Coverage.Filtered[0].Path != "vendor/lib.go" {
		t.Errorf("coverage = %+v", res.Coverage)
	}
	m := res.Metadata
	if m.Model != "test-model" || m.ContextWindow != 32000 || m.PromptTokens <= 0 || m.DiffTokens <= 0 ||
		m.RequestTokens <= m.PromptTokens || !m.FastPath || m.LLMCalls != 1 || m.Truncated || m.DiffTrimmed {
		t.Errorf("metadata = %+v", m)
	}
	if len(res.Notes) != 0 || res.Publish != nil || h.prov.posted != nil {
		t.Errorf("notes %v publish %+v", res.Notes, res.Publish)
	}
	h.checkNoLeaks(t)
}

// TestRunUsesPlainDiff [canary]: pr_ask feeds the model the plain diff
// (ModePlain), never the numbered decoupled hunks of pr_review.
func TestRunUsesPlainDiff(t *testing.T) {
	h := newHarness("ok")
	if _, err := Run(context.Background(), h.deps, h.args()); err != nil {
		t.Fatal(err)
	}
	user := h.llm.calls[0].user
	if strings.Contains(user, "__new hunk__") || strings.Contains(user, "__old hunk__") {
		t.Errorf("the numbered diff reached the model:\n%s", user)
	}
	for _, want := range []string{"## File: 'src/app.go'", "\n+line 11 " + diffMarker + "\n", "\n line 10\n"} {
		if !strings.Contains(user, want) {
			t.Errorf("plain diff lacks %q:\n%s", want, user)
		}
	}
}

func TestRunArgsOverrideConfig(t *testing.T) {
	h := newHarness("ok")
	h.deps.Config.Ask.ExtraInstructions = "FROM-CONFIG"
	h.deps.Config.Output.Language = "de-DE"
	a := h.args()
	if _, err := Run(context.Background(), h.deps, a); err != nil {
		t.Fatal(err)
	}
	sys := h.llm.calls[0].system
	if !strings.Contains(sys, "FROM-CONFIG") || !strings.Contains(sys, "'de-DE'") {
		t.Errorf("config values missing from the system prompt")
	}

	h = newHarness("ok")
	h.deps.Config.Ask.ExtraInstructions = "FROM-CONFIG"
	a.ExtraInstructions, a.OutputLanguage = "FROM-ARGS", "tr-TR"
	if _, err := Run(context.Background(), h.deps, a); err != nil {
		t.Fatal(err)
	}
	sys = h.llm.calls[0].system
	if strings.Contains(sys, "FROM-CONFIG") || !strings.Contains(sys, "FROM-ARGS") || !strings.Contains(sys, "'tr-TR'") {
		t.Errorf("arguments did not override the configuration")
	}
}

// TestRunDegradedConfigMakesNoRequest [canary]: an invalid configuration is
// reported before the resolver, the provider or the model is touched.
func TestRunDegradedConfigMakesNoRequest(t *testing.T) {
	for name, mutate := range map[string]func(d *Deps){
		"config error": func(d *Deps) { d.ConfigErr = errors.New("bad") },
		"no config":    func(d *Deps) { d.Config = nil },
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness("ok")
			mutate(&h.deps)
			for _, call := range []func() error{
				func() error { _, err := Run(context.Background(), h.deps, h.args()); return err },
				func() error { _, err := Prepare(context.Background(), h.deps, h.args()); return err },
			} {
				err := call()
				if !errors.Is(err, llmrun.ErrConfigInvalid) {
					t.Errorf("err = %v", err)
				}
			}
			if h.requests() != 0 {
				t.Errorf("a degraded config made %d requests", h.requests())
			}
		})
	}
}

// TestQuestionRejectedBeforeAnyRequest [canary]: an over-long or empty
// question is rejected with a fixed sentence before the provider or the model
// is contacted; it is never truncated.
func TestQuestionRejectedBeforeAnyRequest(t *testing.T) {
	long := questionMarker + strings.Repeat("x", MaxQuestionRunes)
	for name, tc := range map[string]struct {
		q    string
		want *QuestionError
	}{
		"too long":        {long, ErrQuestionTooLong},
		"one rune over":   {strings.Repeat("é", MaxQuestionRunes+1), ErrQuestionTooLong},
		"empty":           {"", ErrQuestionEmpty},
		"whitespace only": {" \t\r\n ", ErrQuestionEmpty},
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness("ok")
			a := h.args()
			a.Question = tc.q
			for _, call := range []func() error{
				func() error { _, err := Run(context.Background(), h.deps, a); return err },
				func() error { _, err := Prepare(context.Background(), h.deps, a); return err },
			} {
				err := call()
				if !errors.Is(err, tc.want) {
					t.Fatalf("err = %v, want %v", err, tc.want)
				}
				if strings.Contains(err.Error(), questionMarker) || strings.Contains(err.Error(), "xxx") {
					t.Errorf("the question leaked into the error: %q", err)
				}
			}
			if h.requests() != 0 {
				t.Errorf("a rejected question made %d requests", h.requests())
			}
		})
	}
}

func TestQuestionLimitCountsRunes(t *testing.T) {
	// 8000 two-byte runes are 16000 bytes and must be accepted whole.
	q := strings.Repeat("é", MaxQuestionRunes)
	h := newHarness("ok")
	a := h.args()
	a.Question = q
	res, err := Run(context.Background(), h.deps, a)
	if err != nil {
		t.Fatal(err)
	}
	if res.Question != q || !strings.Contains(h.llm.calls[0].user, "======\n"+q+"\n======") {
		t.Error("a question of exactly 8000 runes was altered")
	}
}

func TestValidateQuestionSanitizes(t *testing.T) {
	got, err := ValidateQuestion("  a\xffb \n")
	if err != nil || got != "a\ufffdb" {
		t.Errorf("ValidateQuestion = %q, %v", got, err)
	}
	h := newHarness("ok")
	a := h.args()
	a.Question = "bad \xc3\x28 byte"
	if _, err := Run(context.Background(), h.deps, a); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(h.llm.calls[0].user, "\xc3\x28") || !strings.Contains(h.llm.calls[0].user, "bad \ufffd( byte") {
		t.Error("invalid UTF-8 reached the prompt")
	}
}

func TestPrepareMakesNoModelCall(t *testing.T) {
	h := newHarness("ok")
	h.deps.LLM = nil // a dry run needs no LLM client
	pl, err := Prepare(context.Background(), h.deps, h.args())
	if err != nil {
		t.Fatal(err)
	}
	if len(h.llm.calls) != 0 {
		t.Fatalf("Prepare called the model %d times", len(h.llm.calls))
	}
	if pl.Empty || pl.Prompts.System == "" || !strings.Contains(pl.Prompts.User, questionMarker) {
		t.Fatalf("plan has no prompts: empty=%v", pl.Empty)
	}
	m := pl.Result.Metadata
	if m.PromptTokens <= 0 || m.DiffTokens <= 0 || m.RequestTokens <= m.PromptTokens || m.LLMCalls != 0 {
		t.Errorf("metadata = %+v", m)
	}
	if pl.Budget.PromptTokens != m.PromptTokens || pl.Budget.ContextWindow != 32000 {
		t.Errorf("budget = %+v", pl.Budget)
	}
	if h.prov.posted != nil {
		t.Error("Prepare posted a comment")
	}

	// Run on the same inputs sends exactly the planned prompts.
	h2 := newHarness("ok")
	res, err := Run(context.Background(), h2.deps, h2.args())
	if err != nil {
		t.Fatal(err)
	}
	if len(h2.llm.calls) != 1 || h2.llm.calls[0].system != pl.Prompts.System || h2.llm.calls[0].user != pl.Prompts.User {
		t.Error("Run did not send the prompts Prepare returned")
	}
	if res.Metadata.RequestTokens != m.RequestTokens || res.Metadata.PromptTokens != m.PromptTokens {
		t.Errorf("Run metadata %+v differs from the plan %+v", res.Metadata, m)
	}
}

// TestPromptTokensIncludeTheQuestion: the question is part of the
// scaffolding the budget is built from (step 4).
func TestPromptTokensIncludeTheQuestion(t *testing.T) {
	prompt := func(q string) int {
		h := newHarness("ok")
		a := h.args()
		a.Question = q
		pl, err := Prepare(context.Background(), h.deps, a)
		if err != nil {
			t.Fatal(err)
		}
		return pl.Result.Metadata.PromptTokens
	}
	if short, long := prompt("short?"), prompt(strings.Repeat("a rather long question ", 100)); long <= short+100 {
		t.Errorf("prompt tokens: short %d, long %d", short, long)
	}
}

func TestRunEmptyDiffMakesNoModelCall(t *testing.T) {
	h := newHarness("ok")
	h.prov.files = h.prov.files[2:] // only the filtered vendor file
	res, err := Run(context.Background(), h.deps, Args{PRURL: testPRURL, Question: "Anything?", Publish: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(h.llm.calls) != 0 || res.Metadata.LLMCalls != 0 || res.Metadata.RequestTokens != 0 {
		t.Fatalf("LLM calls = %d", len(h.llm.calls))
	}
	if !slices.Equal(res.Notes, []string{"No reviewable changes after filtering."}) || res.Answer != "" {
		t.Errorf("notes %v answer %q", res.Notes, res.Answer)
	}
	if len(res.Coverage.Filtered) != 1 || len(res.Coverage.Included) != 0 {
		t.Errorf("coverage = %+v", res.Coverage)
	}
	if res.Publish == nil || !res.Publish.Published {
		t.Errorf("publish = %+v", res.Publish)
	}
}

func TestRunDoesNotFit(t *testing.T) {
	t.Run("no room for the diff", func(t *testing.T) {
		h := newHarness("ok")
		h.deps.Config.LLM.ContextWindow = 1600
		_, err := Run(context.Background(), h.deps, h.args())
		var re *llmrun.Error
		if !errors.Is(err, llmrun.ErrDoesNotFit) || !errors.As(err, &re) || !errors.Is(err, tokens.ErrDoesNotFit) ||
			len(h.llm.calls) != 0 {
			t.Errorf("err = %v, calls %d", err, len(h.llm.calls))
		}
		if err != nil && err.Error() != llmrun.DoesNotFitSentence {
			t.Errorf("not the fixed sentence: %q", err.Error())
		}
	})
	t.Run("no file fits under skip", func(t *testing.T) {
		h := newHarness("ok")
		big := "@@ -1 +1,4000 @@\n-x\n" + strings.Repeat("+some fairly long added line of code\n", 4000)
		h.prov.files = []provider.FilePatch{{Path: "big.go", Type: provider.ChangeModified, Patch: big,
			HeadStatus: provider.ContentNotFetchedSizeCap}}
		h.deps.Config.Diff.LargePatchPolicy = "skip"
		h.deps.Config.LLM.ContextWindow = 8000
		_, err := Run(context.Background(), h.deps, h.args())
		if !errors.Is(err, llmrun.ErrDoesNotFit) || len(h.llm.calls) != 0 {
			t.Errorf("err = %v, calls %d", err, len(h.llm.calls))
		}
	})
}

// TestRunOmittedFilesAreInCoverage: when the budget leaves files out, the
// coverage names them and the model still gets one call (X-3).
func TestRunOmittedFilesAreInCoverage(t *testing.T) {
	h := newHarness("ok")
	var files []provider.FilePatch
	for i := range 30 {
		files = append(files, provider.FilePatch{
			Path: fmt.Sprintf("src/f%02d.go", i), Type: provider.ChangeModified,
			Patch:      "@@ -1,2 +1," + fmt.Sprint(60) + " @@\n a\n" + strings.Repeat("+some added line of code here\n", 60),
			BaseStatus: provider.ContentNotFetchedSizeCap, HeadStatus: provider.ContentNotFetchedSizeCap,
		})
	}
	h.prov.files = files
	h.deps.Config.LLM.ContextWindow = 6000
	res, err := Run(context.Background(), h.deps, h.args())
	if err != nil {
		t.Fatal(err)
	}
	c := res.Coverage
	omitted := len(c.Omitted.Added) + len(c.Omitted.Modified) + len(c.Omitted.Deleted)
	if omitted == 0 || res.Metadata.FastPath || len(h.llm.calls) != 1 {
		t.Fatalf("omitted %d, fast path %v, calls %d", omitted, res.Metadata.FastPath, len(h.llm.calls))
	}
	if got := len(c.Included) + len(c.Clipped) + omitted + len(c.Skipped) + len(c.Filtered); got != len(files) {
		t.Errorf("coverage accounts for %d of %d files", got, len(files))
	}
}

// TestRunTruncatedAnswerIsKept: finish_reason length keeps the answer, adds
// the note and makes no second call.
func TestRunTruncatedAnswerIsKept(t *testing.T) {
	h := newHarness("The first half of an answ")
	h.llm.truncated = []bool{true}
	res, err := Run(context.Background(), h.deps, h.args())
	if err != nil {
		t.Fatal(err)
	}
	if len(h.llm.calls) != 1 {
		t.Errorf("LLM calls = %d, want exactly 1 (no re-ask)", len(h.llm.calls))
	}
	if res.Answer != "The first half of an answ" || !res.Metadata.Truncated ||
		!slices.Equal(res.Notes, []string{"The answer was cut off by the model's output limit."}) {
		t.Errorf("result = %+v", res)
	}
}

func TestRunLLMErrorsAreReturnedAsIs(t *testing.T) {
	h := newHarness("ok")
	h.llm.err = &llm.Error{Class: llm.ClassAuth, Status: 401}
	_, err := Run(context.Background(), h.deps, Args{PRURL: testPRURL, Question: "q?", Publish: true})
	if !errors.Is(err, llm.ErrAuth) || len(h.llm.calls) != 1 || len(h.prov.posted) != 0 {
		t.Errorf("err = %v, calls %d, posted %d", err, len(h.llm.calls), len(h.prov.posted))
	}
}

func TestRunEmptyAnswerIsAProtocolError(t *testing.T) {
	h := newHarness("  \n ")
	_, err := Run(context.Background(), h.deps, h.args())
	if !errors.Is(err, llm.ErrProtocol) || len(h.llm.calls) != 1 {
		t.Errorf("err = %v, calls %d", err, len(h.llm.calls))
	}
}

// TestRunWithTheRealClient exercises the pieces the fake skips: the client
// owns the retries and rejects empty content (llm_protocol), and the answer
// of a length-finished response is kept.
func TestRunWithTheRealClient(t *testing.T) {
	var hits atomic.Int32
	var body atomic.Value
	content, finish := "A complete answer.", "length"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		var buf bytes.Buffer
		_, _ = buf.ReadFrom(r.Body)
		body.Store(buf.String())
		out, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{
			"message": map[string]any{"role": "assistant", "content": content}, "finish_reason": finish}}})
		_, _ = w.Write(out)
	}))
	defer srv.Close()

	h := newHarness()
	h.deps.Config.LLM.BaseURL = srv.URL
	client, err := llm.New(h.deps.Config.LLM, h.deps.Config.Secrets.LLMAPIKey, h.deps.Logger)
	if err != nil {
		t.Fatal(err)
	}
	h.deps.LLM = client

	res, err := Run(context.Background(), h.deps, h.args())
	if err != nil {
		t.Fatal(err)
	}
	if res.Answer != "A complete answer." || !res.Metadata.Truncated || len(res.Notes) != 1 || hits.Load() != 1 {
		t.Errorf("result = %+v, hits %d", res, hits.Load())
	}
	sent, _ := body.Load().(string)
	for _, m := range []string{titleMarker, descMarker, branchMarker, diffMarker, questionMarker} {
		if !strings.Contains(sent, m) {
			t.Errorf("marker %s is not in the request body", m)
		}
	}
	h.checkNoLeaks(t)

	content, finish = "", "stop"
	_, err = Run(context.Background(), h.deps, h.args())
	if !errors.Is(err, llm.ErrProtocol) {
		t.Errorf("empty content: err = %v", err)
	}
	h.checkNoLeaks(t, err)
}

func TestRunPublish(t *testing.T) {
	t.Run("posts the rendering", func(t *testing.T) {
		h := newHarness("The answer.")
		a := h.args()
		a.Publish = true
		res, err := Run(context.Background(), h.deps, a)
		if err != nil {
			t.Fatal(err)
		}
		if res.Publish == nil || !res.Publish.Published || res.Publish.CommentID != "42" || res.Publish.Error != "" {
			t.Fatalf("publish = %+v", res.Publish)
		}
		if len(h.prov.posted) != 1 || h.prov.posted[0] != `rendered answer="The answer." gfm=true` {
			t.Errorf("posted = %q", h.prov.posted)
		}
		// The renderer sees the answer, and the result already carries the
		// publish record.
		if len(h.rendered) != 1 || h.rendered[0] != res || res.Answer != "The answer." {
			t.Errorf("renderer did not get the result")
		}
	})
	t.Run("failure keeps the answer", func(t *testing.T) {
		for name, perr := range map[string]error{
			"classified": &provider.Error{Class: provider.ClassAuth, Status: 401},
			"other":      errors.New("https://your-gitea.example/x?token=" + secretMarker),
		} {
			h := newHarness("The answer.")
			h.prov.postErr = perr
			a := h.args()
			a.Publish = true
			res, err := Run(context.Background(), h.deps, a)
			if err != nil {
				t.Fatalf("%s: a publish failure became an error: %v", name, err)
			}
			if res.Answer != "The answer." || res.Publish == nil || res.Publish.Published || res.Publish.Error == "" {
				t.Fatalf("%s: result = %+v publish %+v", name, res, res.Publish)
			}
			var pe *provider.Error
			if errors.As(perr, &pe) && res.Publish.Error != pe.Error() {
				t.Errorf("%s: publish error = %q", name, res.Publish.Error)
			}
			if !errors.As(perr, &pe) && res.Publish.Error != publishFailedMessage {
				t.Errorf("%s: publish error = %q", name, res.Publish.Error)
			}
			h.checkNoLeaks(t)
			if strings.Contains(res.Publish.Error, secretMarker) {
				t.Errorf("%s: the publish error leaks", name)
			}
		}
	})
	t.Run("without a renderer", func(t *testing.T) {
		h := newHarness("The answer.")
		h.deps.RenderProvider = nil
		a := h.args()
		a.Publish = true
		res, err := Run(context.Background(), h.deps, a)
		if err != nil || res.Answer == "" || res.Publish == nil || res.Publish.Published || len(h.prov.posted) != 0 {
			t.Errorf("err %v, publish %+v", err, res.Publish)
		}
	})
	t.Run("not requested", func(t *testing.T) {
		h := newHarness("The answer.")
		if _, err := Run(context.Background(), h.deps, h.args()); err != nil || len(h.prov.posted) != 0 || len(h.rendered) != 0 {
			t.Errorf("err %v, posted %d", err, len(h.prov.posted))
		}
	})
}

func TestRunReportsProgressStages(t *testing.T) {
	h := newHarness("ok")
	var stages []string
	h.deps.Progress = func(s string) { stages = append(stages, s) }
	if _, err := Run(context.Background(), h.deps, h.args()); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(stages, "|"); got != "fetching|preparing diff|calling model" {
		t.Errorf("stages = %q", got)
	}
}

func TestRunNeedsWiring(t *testing.T) {
	h := newHarness("ok")
	h.deps.LLM = nil
	if _, err := Run(context.Background(), h.deps, h.args()); err == nil || h.requests() != 0 {
		t.Errorf("err = %v, requests %d", err, h.requests())
	}
}

// TestMarkersReachTheModelButNeverLogsOrErrors [canary] (X-8): markers in
// the title, description, branch, a diff line and the question reach the
// model; neither they, the model's answer nor the secrets appear in the
// debug logs or in any error.
func TestMarkersReachTheModelButNeverLogsOrErrors(t *testing.T) {
	h := newHarness("The answer mentions " + answerMarker)
	a := h.args()
	a.Publish = true
	res, err := Run(context.Background(), h.deps, a)
	if err != nil {
		t.Fatal(err)
	}
	call := h.llm.calls[0]
	for _, m := range []string{titleMarker, descMarker, branchMarker, diffMarker, questionMarker} {
		if !strings.Contains(call.user, m) {
			t.Errorf("marker %s did not reach the model", m)
		}
	}
	if !strings.Contains(res.Answer, answerMarker) {
		t.Error("the answer is missing from the result")
	}
	if h.logs.Len() == 0 {
		t.Fatal("no debug logs were written; the leak check proves nothing")
	}
	h.checkNoLeaks(t)

	// Error paths: nothing in the error text, whatever the failure.
	for name, setup := range map[string]func(h *harness){
		"llm error":    func(h *harness) { h.llm.err = &llm.Error{Class: llm.ClassUpstream, Status: 500} },
		"does not fit": func(h *harness) { h.deps.Config.LLM.ContextWindow = 1600 },
		"degraded":     func(h *harness) { h.deps.ConfigErr = errors.New("bad config") },
	} {
		h := newHarness("x")
		setup(h)
		_, err := Run(context.Background(), h.deps, h.args())
		if err == nil {
			t.Errorf("%s: no error", name)
		}
		h.checkNoLeaks(t, err)
	}
	h2 := newHarness("x")
	a2 := h2.args()
	a2.Question = questionMarker + strings.Repeat("y", MaxQuestionRunes)
	_, err = Run(context.Background(), h2.deps, a2)
	h2.checkNoLeaks(t, err)
}

// TestRunDiffMaxTokensAppliesToAsk: pr_ask prepares its diff against the same
// budget, so diff.max_tokens (X-17) omits files there too.
func TestRunDiffMaxTokensAppliesToAsk(t *testing.T) {
	h := newHarness("ok")
	var files []provider.FilePatch
	for i := range 30 {
		files = append(files, provider.FilePatch{
			Path: fmt.Sprintf("src/f%02d.go", i), Type: provider.ChangeModified,
			Patch:      "@@ -1,2 +1,60 @@\n a\n" + strings.Repeat("+some added line of code here\n", 60),
			BaseStatus: provider.ContentNotFetchedSizeCap, HeadStatus: provider.ContentNotFetchedSizeCap,
		})
	}
	h.prov.files = files
	capTokens := 2000
	h.deps.Config.Diff.MaxTokens = &capTokens
	res, err := Run(context.Background(), h.deps, h.args())
	if err != nil {
		t.Fatal(err)
	}
	c := res.Coverage
	if len(c.Omitted.Added)+len(c.Omitted.Modified)+len(c.Omitted.Deleted) == 0 || res.Metadata.FastPath {
		t.Errorf("a cap of %d omitted nothing: %+v", capTokens, c)
	}
}
