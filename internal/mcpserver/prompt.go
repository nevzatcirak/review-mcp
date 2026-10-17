package mcpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"

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
	promptFocusesMessage = "focuses must be a comma-separated list of security, architecture, performance, ux and tests"
)

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
				"performance, ux and tests (default: review.focuses of the repository config, else " + defaultFocuses + ")"},
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
		var focuses []string
		if strings.TrimSpace(args["focuses"]) == "" {
			// No focuses given: the repository config of the pull request
			// may set the default list (X-40).
			focuses = defaultFocusesFor(ctx, deps, req, prURL)
		} else if focuses, ok = promptFocuses(args["focuses"]); !ok {
			return nil, &jsonrpc.Error{Code: jsonrpc.CodeInvalidParams, Message: promptFocusesMessage}
		}
		return &mcp.GetPromptResult{
			Description: "Multi-perspective review of one pull request",
			Messages: []*mcp.PromptMessage{{
				Role:    "user",
				Content: &mcp.TextContent{Text: multiPerspectiveText(prURL, focuses, queued)},
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

// defaultFocusesFor returns the focuses of a prompt call that gave none:
// review.focuses of the pull request's repository config when it is present
// and valid, the built-in default otherwise (X-40). It resolves the URL,
// reads the pull request and reads the file at its base revision, and
// nothing else. Any failure (a degraded configuration, a URL that matches
// no provider, a missing credential, a read error) falls back to the
// default without an error, and with repo_config.enabled false nothing is
// read. In serve mode the credentials are those of the request.
func defaultFocusesFor(ctx context.Context, deps Deps, req *mcp.GetPromptRequest, prURL string) []string {
	pc := promptContext{}
	if repo := repoFocuses(ctx, deps, req, prURL); repo != nil {
		pc.repoFocuses = repo
	}
	return builtinFocuses(pc)
}

// promptContext is what the default focus list is chosen from.
type promptContext struct {
	// repoFocuses are the validated review.focuses of the repository
	// config; nil when there are none or they are not valid.
	repoFocuses []string
}

// builtinFocuses is the default list of a prompt call that gave no
// focuses: the repository's list when it has one, defaultFocuses otherwise.
func builtinFocuses(pc promptContext) []string {
	if pc.repoFocuses != nil {
		return pc.repoFocuses
	}
	return strings.Split(defaultFocuses, ",")
}

// repoFocuses reads review.focuses of the repository config of prURL; nil
// when there is no usable list.
func repoFocuses(ctx context.Context, deps Deps, req *mcp.GetPromptRequest, prURL string) []string {
	if deps.LoadErr != nil || deps.Config == nil || deps.NewResolver == nil || !deps.Config.RepoConfig.Enabled {
		return nil
	}
	var header http.Header
	if req != nil && req.Extra != nil {
		header = req.Extra.Header
	}
	cfg, err := deps.configForHeader(header)
	if err != nil {
		return nil
	}
	sc := newScope(deps, cfg, false)
	defer sc.release()
	ref, p, err := sc.resolver.Resolve(prURL)
	if err != nil {
		return nil
	}
	pr, err := p.GetPullRequest(ctx, ref)
	if err != nil {
		return nil
	}
	out := repocfg.ForRun(ctx, logger(deps), cfg, p, ref, pr)
	return validRepoFocuses(out.Focuses)
}

// validRepoFocuses returns list as known focus values (without regard to
// case or surrounding space, repeats once), or nil when it is empty or holds
// a value that is not a known focus.
func validRepoFocuses(list []string) []string {
	var out []string
	for _, item := range list {
		v := strings.ToLower(strings.TrimSpace(item))
		if !slices.Contains(review.FocusValues, v) {
			return nil
		}
		if !slices.Contains(out, v) {
			out = append(out, v)
		}
	}
	return out
}

// promptFocuses parses the focuses argument: known focus values
// (review.FocusValues), separated by commas, without regard to case or
// surrounding space; empty items are ignored and repeats kept once. An
// absent or blank argument is defaultFocuses. ok is false for an unknown
// value or a list with no value.
func promptFocuses(raw string) ([]string, bool) {
	if strings.TrimSpace(raw) == "" {
		raw = defaultFocuses
	}
	var out []string
	for _, item := range strings.Split(raw, ",") {
		v := strings.ToLower(strings.TrimSpace(item))
		if v == "" {
			continue
		}
		if !slices.Contains(review.FocusValues, v) {
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
func multiPerspectiveText(prURL string, focuses []string, queued bool) string {
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
	for _, f := range focuses {
		b.WriteString("   - " + jsonText(call{PRURL: prURL, Focus: f}) + "\n")
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
