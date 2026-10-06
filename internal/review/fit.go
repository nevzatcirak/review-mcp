package review

import (
	"errors"
	"strings"

	"github.com/nevzatcirak/review-mcp/internal/llmrun"
	"github.com/nevzatcirak/review-mcp/internal/provider"
	"github.com/nevzatcirak/review-mcp/internal/tokens"
)

// ReaskNote is the sentence of the one re-ask (DQ-9 step 2, §4.3 step 8),
// inserted on its own line before ResponseLine.
const ReaskNote = "Note: your previous answer could not be parsed as YAML. Answer again with valid YAML only, following the schema exactly."

// withReaskNote returns user with ReaskNote on its own line right before the
// last ResponseLine.
func withReaskNote(user string) (string, error) {
	i := strings.LastIndex(user, "\n"+ResponseLine)
	if i < 0 {
		return "", errors.New("review: the user prompt has no response line")
	}
	return user[:i+1] + ReaskNote + "\n" + user[i+1:], nil
}

// fitted is the outcome of the request-size guard.
type fitted struct {
	prompts Prompts
	// reaskUser is the user prompt of the re-ask.
	reaskUser string
	// requestTokens is tokens.RequestTokens of prompts.
	requestTokens int
	// diff is the diff in the prompts; keptLines is its line count when the
	// guard trimmed it, -1 otherwise.
	diff      string
	keptLines int
}

// fitPrompts renders the final prompts and applies the guard against
// estimator drift (§4.3 step 6) with llmrun.Fit, checking the re-ask
// request (the user prompt with ReaskNote, a few tokens longer than the
// first) so a re-ask can never overrun the window the first request
// fitted. See llmrun.Fit for the trimming rules.
func fitPrompts(in PromptInput, diff string, b tokens.Budget) (*fitted, error) {
	f, err := llmrun.Fit(diff, b, func(d string) (llmrun.Rendered, error) {
		in.Diff = d
		p, err := RenderPrompts(in)
		if err != nil {
			return llmrun.Rendered{}, err
		}
		ru, err := withReaskNote(p.User)
		if err != nil {
			return llmrun.Rendered{}, err
		}
		return llmrun.Rendered{System: p.System, User: p.User, GuardUser: ru}, nil
	})
	if err != nil {
		return nil, err
	}
	return &fitted{
		prompts:       Prompts{System: f.Rendered.System, User: f.Rendered.User},
		reaskUser:     f.Rendered.GuardUser,
		requestTokens: f.RequestTokens,
		diff:          f.Diff,
		keptLines:     f.KeptLines,
	}, nil
}

// trimCoverage moves the files the guard cut out of the diff
// (llmrun.TrimCoverage).
func trimCoverage(c *Coverage, diff string, kept int, types map[string]provider.ChangeType) {
	llmrun.TrimCoverage(c, diff, kept, types)
}
