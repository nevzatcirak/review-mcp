package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// variant is a named set of review-mcp flags and non-secret environment
// settings (V-9 spec §2.2, amendment 2026-11-16 item 2). It is recorded in
// run.json as it was applied.
type variant struct {
	Name  string   `json:"name"`
	Flags []string `json:"flags"`
	// Env holds KEY=VALUE settings; never a secret.
	Env []string `json:"env"`
	// RepoContext is whether the variant turns repository context on; the
	// result's coverage.repo_context.status must agree (off ⇔ "off").
	RepoContext bool `json:"repo_context"`
	// Impact is whether the variant turns the impact trace on (X-50); the
	// result's coverage.impact.status must say it ran (V-18 spec §6
	// item 6).
	Impact bool `json:"impact"`
}

const (
	maxTokensEnv       = "REVIEW_MCP_CONTEXT_REPO_MAX_TOKENS"   //nolint:gosec // G101: a token budget, not a credential
	impactMaxTokensEnv = "REVIEW_MCP_CONTEXT_IMPACT_MAX_TOKENS" //nolint:gosec // G101: a token budget, not a credential
)

// variantNames lists the built-in variants for messages, in order.
const variantNames = "off, on, on-10k, impact and impact-10k"

// builtinVariants are the variants of each mode. In gitea mode they are
// flags of `diag review`. `local review` has no --repo-context flag, so in
// local mode the same switches are the configuration variables
// REVIEW_MCP_CONTEXT_REPO_ENABLED and REVIEW_MCP_CONTEXT_IMPACT_ENABLED
// (V-18 spec §6 item 6).
func builtinVariants(mode string) map[string]variant {
	if mode == "local" {
		const enabled, impact = "REVIEW_MCP_CONTEXT_REPO_ENABLED", "REVIEW_MCP_CONTEXT_IMPACT_ENABLED"
		return map[string]variant{
			"off":    {Name: "off", Env: []string{enabled + "=false"}},
			"on":     {Name: "on", Env: []string{enabled + "=true"}, RepoContext: true},
			"on-10k": {Name: "on-10k", Env: []string{enabled + "=true", maxTokensEnv + "=10000"}, RepoContext: true},
			"impact": {Name: "impact", Env: []string{enabled + "=true", impact + "=true"}, RepoContext: true, Impact: true},
			"impact-10k": {Name: "impact-10k", Env: []string{enabled + "=true", impact + "=true", maxTokensEnv + "=10000"},
				RepoContext: true, Impact: true},
		}
	}
	return map[string]variant{
		"off":    {Name: "off", Flags: []string{"--repo-context=off"}},
		"on":     {Name: "on", Flags: []string{"--repo-context=on"}, RepoContext: true},
		"on-10k": {Name: "on-10k", Flags: []string{"--repo-context=on"}, Env: []string{maxTokensEnv + "=10000"}, RepoContext: true},
		"impact": {Name: "impact", Flags: []string{"--repo-context=on", "--impact=on"}, RepoContext: true, Impact: true},
		"impact-10k": {Name: "impact-10k", Flags: []string{"--repo-context=on", "--impact=on"}, Env: []string{maxTokensEnv + "=10000"},
			RepoContext: true, Impact: true},
	}
}

// runFile is DIR2/run.json: what was run, how, and each run's outcome.
type runFile struct {
	Version       int       `json:"version"`
	Mode          string    `json:"mode"`
	BinaryVersion string    `json:"binary_version"`
	Variants      []variant `json:"variants"`
	Repeat        int       `json:"repeat"`
	CacheDir      string    `json:"cache_dir"`
	// Cases maps each case id to its repository and pull request (or
	// clone), as in prs.json.
	Cases []prEntry   `json:"cases"`
	Runs  []runRecord `json:"runs"`
}

// runRecord is one review of one case in one variant.
type runRecord struct {
	Case    string `json:"case"`
	Variant string `json:"variant"`
	Repeat  int    `json:"repeat"`
	// File is the stored result, relative to DIR2; "" when there is none.
	File string `json:"file"`
	OK   bool   `json:"ok"`
	// Failure is a fixed sentence when OK is false.
	Failure  string `json:"failure,omitempty"`
	ExitCode int    `json:"exit_code"`
	WallMS   int64  `json:"wall_ms"`
	// From the result, where it carries them.
	Model         string   `json:"model,omitempty"`
	ModelsUsed    []string `json:"models_used,omitempty"`
	LLMCalls      int      `json:"llm_calls"`
	ModelCalls    int      `json:"model_calls"`
	RequestTokens int      `json:"request_tokens"`
	ToolRounds    int      `json:"tool_rounds"`
	RepoContext   struct {
		Status     string `json:"status"`
		Symbols    int    `json:"symbols"`
		References int    `json:"references"`
		Files      int    `json:"files"`
		MaxTokens  *int   `json:"max_tokens,omitempty"`
	} `json:"repo_context"`
	// Impact is coverage.impact of the result; Status is "" for a binary
	// without the impact trace.
	Impact impactCoverage `json:"impact"`
	// BudgetVerified is set on a successful run of a variant that sets a
	// budget: false when
	// the result does not report a budget the variant sets (a binary
	// before 2.11), with the reason in BudgetNote. A run with an
	// unverified budget is scored; a reported budget that differs fails
	// the run.
	BudgetVerified *bool  `json:"budget_verified,omitempty"`
	BudgetNote     string `json:"budget_note,omitempty"`
	Findings       int    `json:"findings"`
}

// impactCoverage is the part of coverage.impact evalset reads.
type impactCoverage struct {
	Status    string `json:"status"`
	Seeds     int    `json:"seeds"`
	Links     int    `json:"links"`
	MaxTokens *int   `json:"max_tokens,omitempty"`
}

// finding is one key issue of a result.
type finding struct {
	File    string `json:"relevant_file"`
	Header  string `json:"issue_header"`
	Content string `json:"issue_content"`
	Start   int    `json:"start_line"`
	End     int    `json:"end_line"`
}

// reviewResult is the part of the pr_review structured result evalset
// reads (the shape of `diag review --json` and `local review --json`).
type reviewResult struct {
	Review *struct {
		Issues []finding `json:"key_issues_to_review"`
	} `json:"review"`
	Coverage struct {
		ModelCalls  int `json:"model_calls"`
		RepoContext struct {
			Status     string `json:"status"`
			Symbols    int    `json:"symbols"`
			References int    `json:"references"`
			Files      int    `json:"files"`
			MaxTokens  *int   `json:"max_tokens"`
		} `json:"repo_context"`
		// Impact is nil when the result has no coverage.impact (a binary
		// before V-18).
		Impact *impactCoverage `json:"impact"`
	} `json:"coverage"`
	Metadata struct {
		Model         string   `json:"model"`
		ModelsUsed    []string `json:"models_used"`
		LLMCalls      int      `json:"llm_calls"`
		RequestTokens int      `json:"request_tokens"`
		ToolRounds    int      `json:"tool_rounds"`
	} `json:"metadata"`
}

func cmdRun(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("evalset run", flag.ContinueOnError)
	fs.SetOutput(stderr)
	bin := fs.String("bin", "review-mcp", "the review-mcp `binary`")
	prsPath := fs.String("prs", "", "the prs.json `file` of evalset setup")
	variantsFlag := fs.String("variants", "off,on", "comma-separated `variants`: off, on, on-10k, impact, impact-10k")
	repeat := fs.Int("repeat", 1, "runs per case and variant")
	mode := fs.String("mode", "", "gitea or local (default: the mode of prs.json)")
	out := fs.String("out", "", "`directory` for the results (required; new or empty)")
	cacheFlag := fs.String("cache-dir", "", "the repository-context cache `directory` (default: the one `review-mcp diag cache` reports)")
	timeout := fs.Duration("timeout", 30*time.Minute, "bound of one review")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	fail := func(code int, format string, a ...any) int {
		_, _ = fmt.Fprintf(stderr, "evalset run: "+format+"\n", a...)
		return code
	}
	if fs.NArg() > 0 {
		return fail(2, "unexpected argument")
	}
	if *prsPath == "" {
		return fail(2, "-prs is required")
	}
	if *repeat < 1 || *repeat > 20 {
		return fail(2, "-repeat must be from 1 to 20")
	}
	var prs prsFile
	if err := readJSON(*prsPath, &prs); err != nil || len(prs.Cases) == 0 {
		return fail(2, "cannot read -prs")
	}
	if *mode == "" {
		*mode = prs.Mode
	}
	if *mode != "gitea" && *mode != "local" {
		return fail(2, "-mode must be gitea or local")
	}
	if *mode != prs.Mode {
		return fail(2, "-mode %s does not match prs.json, which was set up for %s", *mode, prs.Mode)
	}
	variants, err := pickVariants(*mode, *variantsFlag)
	if err != nil {
		return fail(2, "%s", err.Error())
	}
	// Every variant is checked against the binary before any review.
	if err := probe(ctx, *bin, *mode, variants); err != nil {
		return fail(2, "%s", err.Error())
	}
	cache, err := resolveCache(ctx, *cacheFlag, *bin)
	if err != nil {
		return fail(1, "%s", err.Error())
	}
	if err := checkOut("-out", *out, cache); err != nil {
		return fail(2, "%s", err.Error())
	}
	version, _, err := execBin(ctx, *bin, nil, "version")
	if err != nil {
		return fail(1, "the binary does not answer `version`")
	}
	file := runFile{Version: 1, Mode: *mode, BinaryVersion: firstLine(string(version)), Variants: variants,
		Repeat: *repeat, CacheDir: cache, Cases: prs.Cases}

	total := *repeat * len(prs.Cases) * len(variants)
	failed, done := 0, 0
	// Variants alternate within each case and repeat, so that a change of
	// the endpoint over time touches every variant alike.
	for n := 1; n <= *repeat; n++ {
		for _, e := range prs.Cases {
			for _, v := range variants {
				rec := reviewOnce(ctx, *bin, *mode, e, v, n, *out, *timeout, stderr)
				file.Runs = append(file.Runs, rec)
				done++
				if !rec.OK {
					failed++
				}
				state := "ok"
				if !rec.OK {
					state = "failed"
				}
				_, _ = fmt.Fprintf(stderr, "evalset run: %d/%d %s\n", done, total, state)
			}
		}
	}
	if err := writeJSON(filepath.Join(*out, "run.json"), file); err != nil {
		return fail(1, "cannot write run.json")
	}
	_, _ = fmt.Fprintf(stdout, "evalset run: %d runs, %d failed; see %s\n", total, failed, filepath.Join(*out, "run.json"))
	if failed > 0 {
		return 1
	}
	return 0
}

// pickVariants returns the named built-in variants in the order given.
func pickVariants(mode, list string) ([]variant, error) {
	known := builtinVariants(mode)
	var out []variant
	seen := map[string]bool{}
	for _, name := range strings.Split(list, ",") {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		v, ok := known[name]
		if !ok {
			return nil, fmt.Errorf("unknown variant %q; the built-in variants are %s", name, variantNames)
		}
		if !seen[name] {
			seen[name] = true
			out = append(out, v)
		}
	}
	if len(out) == 0 {
		return nil, errors.New("-variants names no variant")
	}
	return out, nil
}

// probe refuses a variant the binary does not support: every flag it
// passes, and the command's own flags, must appear in the command's help.
// Environment settings cannot be probed this way; run checks them against
// each result's coverage instead (repository context, the impact trace and
// the budgets: variantProblem).
func probe(ctx context.Context, bin, mode string, variants []variant) error {
	cmd, need := []string{"diag", "review", "--help"}, []string{"--json", "--repo-context"}
	if mode == "local" {
		cmd, need = []string{"local", "review", "--help"}, []string{"--json", "--base", "--path"}
	}
	o, e, err := execBin(ctx, bin, nil, cmd...)
	var exitErr *exec.ExitError
	if err != nil && !errors.As(err, &exitErr) {
		return errors.New("cannot start the binary given in -bin")
	}
	help := string(o) + string(e)
	for _, f := range need {
		if !strings.Contains(help, f) {
			return fmt.Errorf("the binary does not support %s (%s is not in `%s`)", strings.Join(cmd[:2], " "), f, strings.Join(cmd, " "))
		}
	}
	for _, v := range variants {
		for _, f := range v.Flags {
			name, _, _ := strings.Cut(f, "=")
			if !strings.Contains(help, name) {
				return fmt.Errorf("the binary does not support the variant %s (%s is not in `%s`)", v.Name, name, strings.Join(cmd, " "))
			}
		}
	}
	return nil
}

// reviewOnce runs one review and stores its result. Nothing is published:
// `diag review` posts only with --publish, and `local review` never posts.
func reviewOnce(ctx context.Context, bin, mode string, e prEntry, v variant, n int, out string, timeout time.Duration, stderr io.Writer) runRecord {
	rec := runRecord{Case: e.ID, Variant: v.Name, Repeat: n}
	args := append([]string{"diag", "review", "--json"}, v.Flags...)
	args = append(args, e.URL)
	if mode == "local" {
		args = append([]string{"local", "review", "--json", "--base", baseBranch, "--path", e.Path}, v.Flags...)
	}
	rctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	start := time.Now()
	raw, errb, err := execBin(rctx, bin, v.Env, args...)
	rec.WallMS = time.Since(start).Milliseconds()
	// The binary's standard error holds fixed sentences and counts, never
	// review text (as for tools/evalrepo).
	_, _ = stderr.Write(errb)
	if err != nil {
		var exitErr *exec.ExitError
		switch {
		case rctx.Err() != nil:
			rec.Failure = "timed out"
		case errors.As(err, &exitErr):
			rec.ExitCode = exitErr.ExitCode()
			rec.Failure = fmt.Sprintf("exit status %d", rec.ExitCode)
		default:
			rec.Failure = "the binary could not be started"
		}
		return rec
	}
	var res reviewResult
	if err := json.Unmarshal(raw, &res); err != nil {
		rec.Failure = "the result is not JSON"
		return rec
	}
	dir := filepath.Join(out, e.ID)
	name := fmt.Sprintf("%s-%d.json", v.Name, n)
	if err := os.MkdirAll(dir, 0o700); err != nil || os.WriteFile(filepath.Join(dir, name), raw, 0o600) != nil {
		rec.Failure = "the result could not be stored"
		return rec
	}
	rec.File = e.ID + "/" + name
	rec.Model, rec.ModelsUsed = res.Metadata.Model, res.Metadata.ModelsUsed
	rec.LLMCalls, rec.ModelCalls = res.Metadata.LLMCalls, res.Coverage.ModelCalls
	rec.RequestTokens, rec.ToolRounds = res.Metadata.RequestTokens, res.Metadata.ToolRounds
	rc := res.Coverage.RepoContext
	rec.RepoContext.Status, rec.RepoContext.Symbols, rec.RepoContext.References, rec.RepoContext.Files = rc.Status, rc.Symbols, rc.References, rc.Files
	rec.RepoContext.MaxTokens = rc.MaxTokens
	if res.Coverage.Impact != nil {
		rec.Impact = *res.Coverage.Impact
	}
	p, unverified := variantProblem(v, res)
	switch {
	case res.Review == nil:
		rec.Failure = "the result has no review"
	case p != "":
		rec.Failure = p
	default:
		rec.OK = true
		rec.Findings = len(res.Review.Issues)
	}
	if rec.OK && setsBudget(v) {
		verified := len(unverified) == 0
		rec.BudgetVerified = &verified
		if !verified {
			rec.BudgetNote = "the binary does not report " + strings.Join(unverified, " or ")
		}
	}
	return rec
}

// failNoTrace is the failure of a variant with the impact trace when the
// result has no coverage.impact at all: the binary predates the trace.
const failNoTrace = "trace not available in this binary"

// budgets are the budget settings a variant can set, with the coverage
// field that echoes each.
var budgets = []struct{ env, field string }{
	{maxTokensEnv, "coverage.repo_context.max_tokens"},
	{impactMaxTokensEnv, "coverage.impact.max_tokens"},
}

// setsBudget reports whether the variant sets any budget.
func setsBudget(v variant) bool {
	for _, b := range budgets {
		if _, ok := envInt(v.Env, b.env); ok {
			return true
		}
	}
	return false
}

// variantProblem checks that a result shows its variant applied, and
// returns a fixed sentence when it does not ("" when it does):
//   - repository context is on exactly when the variant turns it on;
//   - in a variant with the impact trace, coverage.impact.status says the
//     trace ran (on, or skipped_no_symbols when the diff gave nothing to
//     trace); in a variant without it, the trace did not run (a binary
//     before V-18 has no status at all);
//   - a variant with the impact trace fails with failNoTrace when the
//     result has no coverage.impact (a binary before V-18);
//   - every budget the variant sets in its environment is the effective
//     budget the result echoes (coverage.repo_context.max_tokens,
//     coverage.impact.max_tokens; V-18 spec §6 item 5, WP-21b DQ-2). A
//     reported budget that differs is a problem; an absent field is not:
//     it is returned in unverified (the WP-20c review, DQ-4), so one
//     version of the tool scores binaries before and after 2.11.
func variantProblem(v variant, res reviewResult) (problem string, unverified []string) {
	rc := res.Coverage.RepoContext
	var im impactCoverage
	if res.Coverage.Impact != nil {
		im = *res.Coverage.Impact
	}
	switch {
	case (rc.Status == "off") == v.RepoContext:
		return "the variant was not applied: repository context is " + rc.Status, nil
	case v.Impact && res.Coverage.Impact == nil:
		return failNoTrace, nil
	case v.Impact && im.Status != "on" && im.Status != "skipped_no_symbols":
		status := im.Status
		if status == "" {
			status = "missing"
		}
		return "the variant was not applied: the impact trace is " + status, nil
	case !v.Impact && im.Status == "on":
		return "the variant was not applied: the impact trace ran", nil
	}
	got := []*int{rc.MaxTokens, im.MaxTokens}
	for i, b := range budgets {
		want, ok := envInt(v.Env, b.env)
		switch {
		case !ok:
		case got[i] == nil:
			unverified = append(unverified, b.field)
		case *got[i] != want:
			return fmt.Sprintf("the variant was not applied: %s is %d, not %d", b.field, *got[i], want), nil
		}
	}
	return "", unverified
}

// envInt is the integer value of key in KEY=VALUE settings.
func envInt(env []string, key string) (int, bool) {
	for _, kv := range env {
		if k, val, _ := strings.Cut(kv, "="); k == key {
			n, err := strconv.Atoi(val)
			return n, err == nil
		}
	}
	return 0, false
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}

// readJSON reads path into v.
func readJSON(path string, v any) error {
	b, err := os.ReadFile(path) //nolint:gosec // G304: the operator's own file
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}
