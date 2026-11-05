package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/nevzatcirak/review-mcp/internal/config"
	"github.com/nevzatcirak/review-mcp/internal/filter"
	"github.com/nevzatcirak/review-mcp/internal/provider"
	"github.com/nevzatcirak/review-mcp/internal/repocfg"
	"github.com/nevzatcirak/review-mcp/internal/review"
)

// promptMultiPerspective is the MCP prompt of the focused-review pattern
// (X-33, spec 2F §4.3).
const promptMultiPerspective = "multi_perspective_review"

// defaultFocuses are the built-in focuses of multi_perspective_review when
// the focuses argument is absent or empty and the repository config sets
// none (defaultFocusesFor).
const defaultFocuses = "security,architecture,performance,tests"

// promptReadTimeout bounds the repository reads of one prompt call in total
// (the pull request and the repository config): prompts/get is interactive,
// so a slow host must not stall the client. On expiry the prompt falls back
// to the built-in default like any other read failure. A variable so that
// tests can shorten it.
var promptReadTimeout = 10 * time.Second

// maxPromptURLBytes caps the pr_url argument of the prompt.
const maxPromptURLBytes = 2048

// Fixed errors of the prompt's arguments. They never echo a value.
const (
	promptURLMessage = "pr_url must be the http or https URL of a pull request, without credentials"
	// DESIGN-QUESTION: an unknown value in focuses — treat it as a free
	// text focus or refuse it? — chose to refuse: a comma-separated list
	// cannot carry a free text that holds a comma, and a typo of a known
	// value would silently become a weak free-text focus. A free-text focus
	// stays available through pr_review's focus argument.
	promptFocusesMessage = "focuses must be a comma-separated list of security, architecture, performance, ux, tests " +
		"and docs, or the name of a custom focus defined for the repository"
)

// maxPromptFocuses is the most focuses of a default list (X-51, v2.9 spec
// V-19 §0 item 7): the list is cut in its order and the message names what
// was cut.
const maxPromptFocuses = 8

// focusesRefusal is the refusal of an unknown value in focuses: the fixed
// sentence, then the custom focuses that are defined (their names are valid
// focus names, never free text), or that none is. It never echoes a value
// of the call.
func focusesRefusal(customs []config.CustomFocus) string {
	if len(customs) == 0 {
		return promptFocusesMessage + "; no custom focus is defined"
	}
	return promptFocusesMessage + "; custom focuses defined: " + strings.Join(config.CustomFocusNames(customs), ", ")
}

// registerMultiPerspectivePrompt registers the prompt of X-33; queued adds
// rateLimitGuidance to its first step (X-37).
func registerMultiPerspectivePrompt(s *mcp.Server, deps Deps) {
	queued := rateLimitConfigured(deps.Config)
	s.AddPrompt(&mcp.Prompt{
		Name:  promptMultiPerspective,
		Title: "Multi-perspective review",
		Description: "Review a pull request from several focused perspectives with pr_review, merge the findings, " +
			"and publish them once.",
		Arguments: []*mcp.PromptArgument{
			{Name: "pr_url", Description: "URL of the pull request", Required: true},
			{Name: "focuses", Description: "comma-separated focus areas out of security, architecture, " +
				"performance, ux, tests and docs, or the name of a custom focus defined for the repository (default: " +
				"review.focuses of the repository config, else " + defaultFocuses + ", with docs added when the pull " +
				"request changes a documentation file; then the focuses of the repository's focus rules that match a " +
				"changed file, at most 8 in all)"},
		},
	}, func(ctx context.Context, req *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
		var args map[string]string
		if req != nil && req.Params != nil {
			args = req.Params.Arguments
		}
		prURL, ok := promptPRURL(args["pr_url"])
		if !ok {
			return nil, &jsonrpc.Error{Code: jsonrpc.CodeInvalidParams, Message: promptURLMessage}
		}
		var plan focusPlan
		if strings.TrimSpace(args["focuses"]) == "" {
			// No focuses given: the repository config of the pull request
			// may set the default list and the rules (X-40, X-51).
			plan = planFocuses(readPromptContext(ctx, deps, req, prURL, true))
		} else if plan, ok = requestedFocuses(ctx, deps, req, prURL, args["focuses"]); !ok {
			return nil, &jsonrpc.Error{Code: jsonrpc.CodeInvalidParams, Message: plan.refusal}
		}
		return &mcp.GetPromptResult{
			Description: "Multi-perspective review of one pull request",
			Messages: []*mcp.PromptMessage{{
				Role:    "user",
				Content: &mcp.TextContent{Text: multiPerspectiveText(prURL, plan, queued)},
			}},
		}, nil
	})
}

// promptPRURL checks the pr_url argument and returns the form the message
// embeds: an absolute http(s) URL with a host and no credentials, without
// its query and fragment (a pull request URL needs neither), as net/url
// writes it (escaped, on one line). It is user input: the message carries
// it only inside JSON strings.
func promptPRURL(raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > maxPromptURLBytes {
		return "", false
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || u.Opaque != "" {
		return "", false
	}
	if s := strings.ToLower(u.Scheme); s != "http" && s != "https" {
		return "", false
	}
	u.RawQuery, u.ForceQuery, u.Fragment, u.RawFragment = "", false, "", ""
	return u.String(), true
}

// promptContext is what the default focus list is chosen from, read for one
// pull request.
type promptContext struct {
	// repoFocuses are the validated review.focuses of the repository
	// config (built-in values and the custom focuses of the run); nil when
	// there are none or they are not valid.
	repoFocuses []string
	// customs are the custom focuses of the run: the operator's and those
	// of the repository config.
	customs []config.CustomFocus
	// rules are the effective focus rules (the repository's when it has
	// any, else the operator's).
	rules []config.FocusRule
	// changed are the changed files that the ignore rules keep, once each;
	// nil when they were not listed.
	changed []string
	// docsChanged: a changed file is a documentation file
	// (filter.IsDocumentation).
	docsChanged bool
	// failed: a read failed (the pull request, the repository config or the
	// file list). The list is then the built-in default, without rules.
	failed bool
}

// focusEntry is one focus of a prompt with the reason it was chosen.
type focusEntry struct{ name, why string }

// focusPlan is the focuses of a prompt call, in order, with what the
// message says about them.
type focusPlan struct {
	entries []focusEntry
	// cut are the focuses dropped by the cap, in order.
	cut []string
	// readFailed adds the sentence of a failed read.
	readFailed bool
	// refusal is the InvalidParams message of a refused focuses argument.
	refusal string
}

// requestedPlan is the plan of focuses the client gave: each is "requested".
func requestedPlan(names []string) focusPlan {
	var p focusPlan
	for _, n := range names {
		p.entries = append(p.entries, focusEntry{n, "requested"})
	}
	return p
}

// planFocuses is the default list of a prompt call that gave no focuses
// (X-51, v2.9 spec V-19 §2.1):
//  1. the base list: the repository's review.focuses, else defaultFocuses,
//     with docs added when a documentation file changed;
//  2. for each rule in order, its focus is appended when a changed file
//     matches one of its globs and the focus is not in the list yet;
//  3. at most maxPromptFocuses, cut from the end.
//
// After a failed read it is the built-in default without rules.
func planFocuses(pc promptContext) focusPlan {
	var p focusPlan
	add := func(name, why string) {
		if !slices.ContainsFunc(p.entries, func(e focusEntry) bool { return e.name == name }) {
			p.entries = append(p.entries, focusEntry{name, why})
		}
	}
	if pc.failed {
		pc = promptContext{failed: true}
		p.readFailed = true
	}
	switch {
	case pc.repoFocuses != nil:
		for _, f := range pc.repoFocuses {
			add(f, "review.focuses")
		}
	default:
		for _, f := range strings.Split(defaultFocuses, ",") {
			add(f, "default")
		}
		if pc.docsChanged {
			add(review.FocusDocs, "documentation files changed")
		}
	}
	for i, r := range pc.rules {
		n := 0
		for _, f := range pc.changed {
			if slices.ContainsFunc(r.Paths, func(g string) bool { return filter.MatchGlob(g, f) }) {
				n++
			}
		}
		if n > 0 {
			add(r.Focus, fmt.Sprintf("rule %d matched %d %s", i+1, n, plural(n, "file", "files")))
		}
	}
	if len(p.entries) > maxPromptFocuses {
		for _, e := range p.entries[maxPromptFocuses:] {
			p.cut = append(p.cut, e.name)
		}
		p.entries = p.entries[:maxPromptFocuses]
	}
	return p
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// readPromptContext reads what planFocuses needs for prURL; the zero value
// when nothing could be read. With list false the changed files are not
// listed (the caller only needs the custom focuses).
func readPromptContext(ctx context.Context, deps Deps, req *mcp.GetPromptRequest, prURL string, list bool) promptContext {
	var pc promptContext
	// DESIGN-QUESTION (WP-23b DQ-2): with repo_config.enabled false nothing
	// is read, so the operator's focus rules are not applied either (the
	// prompt would need the pull request and its file list for them alone).
	// Recommendation: keep; read for the operator's rules in a later
	// version if operators ask.
	if deps.LoadErr != nil || deps.Config == nil || deps.NewResolver == nil || !deps.Config.RepoConfig.Enabled {
		// Nothing is read: the built-in default, without a failure.
		return pc
	}
	pc.failed = true
	var header http.Header
	if req != nil && req.Extra != nil {
		header = req.Extra.Header
	}
	cfg, err := deps.configForHeader(header)
	if err != nil {
		return pc
	}
	sc := newScope(deps, cfg, false)
	defer sc.release()
	ctx, cancel := context.WithTimeout(ctx, promptReadTimeout)
	defer cancel()
	ref, p, err := sc.resolver.Resolve(prURL)
	if err != nil {
		return pc
	}
	pr, err := p.GetPullRequest(ctx, ref)
	if err != nil {
		return pc
	}
	out := repocfg.ForRun(ctx, logger(deps), cfg, p, ref, pr)
	if out.Result.Status == repocfg.StatusUnreadable {
		return pc
	}
	pc.customs = out.Config.Review.CustomFocuses
	pc.repoFocuses = validRepoFocuses(out.Focuses, pc.customs)
	pc.rules = out.Config.Review.FocusRules
	if list && (pc.repoFocuses == nil || len(pc.rules) > 0) {
		if pc.changed, err = changedFiles(ctx, out.Config, p, ref, pr); err != nil {
			pc.repoFocuses, pc.rules, pc.changed = nil, nil, nil
			return pc
		}
		pc.docsChanged = slices.ContainsFunc(pc.changed, filter.IsDocumentation)
	}
	pc.failed = false
	return pc
}

// changedFiles lists the changed files of the pull request that the ignore
// rules of cfg keep, once each. It uses GetDiff with an Include that records
// each path and keeps none, so the provider fetches no file content.
func changedFiles(ctx context.Context, cfg *config.Config, p provider.Provider, ref provider.PRRef, pr *provider.PullRequest) ([]string, error) {
	flt, err := filter.New(cfg)
	if err != nil {
		return nil, err
	}
	var out []string
	_, err = p.GetDiff(ctx, ref, pr, provider.DiffOptions{Include: func(path string) bool {
		if flt.Include(path) && !slices.Contains(out, path) {
			out = append(out, path)
		}
		return false
	}})
	return out, err
}

// validRepoFocuses returns list as focus values known to the run (the
// built-in values and the names of customs; without regard to case or
// surrounding space, repeats once), or nil when it is empty or holds a value
// that is not known.
func validRepoFocuses(list []string, customs []config.CustomFocus) []string {
	var out []string
	for _, item := range list {
		v := strings.ToLower(strings.TrimSpace(item))
		if !knownFocus(v, customs) {
			return nil
		}
		if !slices.Contains(out, v) {
			out = append(out, v)
		}
	}
	return out
}

// knownFocus reports whether v (lower case) is a built-in focus value or the
// name of one of customs.
func knownFocus(v string, customs []config.CustomFocus) bool {
	return slices.Contains(review.FocusValues, v) ||
		slices.ContainsFunc(customs, func(c config.CustomFocus) bool { return c.Name == v })
}

// requestedFocuses parses the focuses argument against the built-in values
// and the custom focuses of the run. The operator's are known without a
// read; when a value is not known by them the repository config of the pull
// request is read for its focuses (the one read of a prompt call), so a name
// the repository defines is accepted.
//
// DESIGN-QUESTION (WP-23b DQ-1): the spec says focuses given by the client
// accept custom names, but the repository's definitions need a read, and a
// call with built-in values read nothing so far. Chose to read only when a
// value is not known from the built-in values and the operator's focuses,
// so a call with known values still makes no request. Recommendation: keep.
func requestedFocuses(ctx context.Context, deps Deps, req *mcp.GetPromptRequest, prURL, raw string) (focusPlan, bool) {
	var customs []config.CustomFocus
	if deps.Config != nil {
		customs = deps.Config.Review.CustomFocuses
	}
	names, ok := promptFocuses(raw, customs)
	if !ok && promptFocusesWellFormed(raw) {
		customs = readPromptContext(ctx, deps, req, prURL, false).customs
		names, ok = promptFocuses(raw, customs)
	}
	if !ok {
		return focusPlan{refusal: focusesRefusal(customs)}, false
	}
	return requestedPlan(names), true
}

// promptFocusesWellFormed reports whether raw holds at least one value and
// every value could be a focus name (a repository might define it).
func promptFocusesWellFormed(raw string) bool {
	n := 0
	for _, item := range strings.Split(raw, ",") {
		v := strings.ToLower(strings.TrimSpace(item))
		if v == "" {
			continue
		}
		if config.CustomFocusNameProblem(v) != "" && !slices.Contains(review.FocusValues, v) {
			return false
		}
		n++
	}
	return n > 0
}

// promptFocuses parses the focuses argument: built-in focus values
// (review.FocusValues) and the names of customs, separated by commas,
// without regard to case or surrounding space; empty items are ignored and
// repeats kept once. An absent or blank argument is defaultFocuses. ok is
// false for an unknown value or a list with no value.
func promptFocuses(raw string, customs []config.CustomFocus) ([]string, bool) {
	if strings.TrimSpace(raw) == "" {
		raw = defaultFocuses
	}
	var out []string
	for _, item := range strings.Split(raw, ",") {
		v := strings.ToLower(strings.TrimSpace(item))
		if v == "" {
			continue
		}
		if !knownFocus(v, customs) {
			return nil, false
		}
		if !slices.Contains(out, v) {
			out = append(out, v)
		}
	}
	return out, len(out) > 0
}

// jsonText is v as compact JSON; v is built from strings only, so the
// encoding cannot fail.
func jsonText(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// multiPerspectiveText is the prompt's user message: the recipe of X-33 for
// one pull request. prURL appears only inside JSON strings. queued adds
// rateLimitGuidance after "in parallel if you can" (X-37).
func multiPerspectiveText(prURL string, plan focusPlan, queued bool) string {
	type call struct {
		PRURL string `json:"pr_url"`
		Focus string `json:"focus"`
	}
	var b strings.Builder
	b.WriteString("Review the pull request " + jsonText(prURL) + " from several perspectives with the review-mcp " +
		"tools, then publish one merged review.\n\n")
	b.WriteString("1. Run one focused review per area with pr_review, in parallel if you can (for example in " +
		"subagents). ")
	if queued {
		b.WriteString(rateLimitGuidance + " ")
	}
	b.WriteString("Use these arguments, and do not set publish: a focused review never publishes.\n")
	for _, e := range plan.entries {
		b.WriteString("   - " + jsonText(call{PRURL: prURL, Focus: e.name}) + "\n")
	}
	b.WriteString("   Why these areas:\n")
	for _, e := range plan.entries {
		b.WriteString("   - " + e.name + " \u2014 " + e.why + "\n")
	}
	if len(plan.cut) > 0 {
		fmt.Fprintf(&b, "   The list is cut at %d areas; not included: %s.\n", maxPromptFocuses, strings.Join(plan.cut, ", "))
	}
	if plan.readFailed {
		b.WriteString("   The repository config could not be read; the default areas are used.\n")
	}
	b.WriteString("2. Merge the findings of all runs into one list. Keep each finding's relevant_file, start_line, " +
		"end_line, issue_header and issue_content as the run returned them, and set its focus to the run's focus. " +
		"Drop duplicates: when two runs report the same problem at the same place, keep the clearer finding.\n")
	b.WriteString("3. Call pr_review once with pr_url " + jsonText(prURL) + ", publish=true and the merged list as " +
		"findings. When a security or performance run was made, pass its review.security_concerns as " +
		"security_concerns or its review.performance_concerns as performance_concerns. This call makes no model " +
		"call: it posts the overview comment (an earlier one is edited in place) and the findings on changed lines " +
		"as inline comments, and does not repeat a finding already posted on the pull request.\n")
	b.WriteString("4. Tell the user what was published. If a run said its review is partial, say how many files " +
		"were not reviewed and never state that those files have no issues.\n")
	return b.String()
}
