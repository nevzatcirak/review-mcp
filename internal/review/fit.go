package review

import (
	"errors"
	"slices"
	"strings"

	"github.com/nevzatcirak/review-mcp/internal/diffpipe"
	"github.com/nevzatcirak/review-mcp/internal/patch"
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
// estimator drift (§4.3 step 6): RequestTokens + HardReserve must not
// exceed ContextWindow. When it does, the diff is trimmed to the longest
// line prefix that fits, found with diffpipe's verified-prefix search, and
// tokens.TruncationMarker is appended.
//
// DESIGN-QUESTION: which request does the guard check, and at what
// granularity does it trim? — chose to check the re-ask request (the user
// prompt with ReaskNote, a few tokens longer than the first) so a re-ask
// can never overrun the window the first request fitted, and to trim at
// line granularity with diffpipe.VerifiedPrefix (as spec §4.3 step 6 says
// "trim through the verified prefix"), recording the cut files in the
// coverage (trimCoverage) and a note; re-running diffpipe.Prepare with a
// smaller budget was the alternative, but it is not a verified-prefix trim
// and can loop on the fast path. The guard only trips on estimator drift
// larger than the 500-token soft reserve margin, so this path is rare.
func fitPrompts(in PromptInput, diff string, b tokens.Budget) (*fitted, error) {
	limit := b.ContextWindow - b.HardReserve()
	try := func(d string) (*fitted, bool, error) {
		in.Diff = d
		p, err := RenderPrompts(in)
		if err != nil {
			return nil, false, err
		}
		ru, err := withReaskNote(p.User)
		if err != nil {
			return nil, false, err
		}
		f := &fitted{prompts: p, reaskUser: ru, requestTokens: tokens.RequestTokens(p.System, p.User, b.Factor),
			diff: d, keptLines: -1}
		return f, tokens.RequestTokens(p.System, ru, b.Factor) <= limit, nil
	}
	f, ok, err := try(diff)
	if err != nil || ok {
		return f, err
	}

	lines := strings.Split(diff, "\n")
	trimmed := func(n int) string { return strings.Join(lines[:n], "\n") + tokens.TruncationMarker }
	var renderErr error
	fits := func(n int) bool {
		if n <= 0 || strings.TrimSpace(strings.Join(lines[:n], "\n")) == "" {
			return false
		}
		_, ok, err := try(trimmed(n))
		if err != nil {
			renderErr = err
		}
		return ok
	}
	n := diffpipe.VerifiedPrefix(len(lines)-1, fits)
	if renderErr != nil {
		return nil, renderErr
	}
	if n == 0 {
		return nil, doesNotFit(tokens.ErrDoesNotFit)
	}
	f, _, err = try(trimmed(n))
	if err != nil {
		return nil, err
	}
	f.keptLines = n
	return f, nil
}

// trimCoverage moves the files the guard cut out of the diff: a file whose
// content is entirely in the first kept lines stays where it was, a file
// that was cut becomes Clipped, a file that is gone moves to Omitted by its
// change type. A file whose header cannot be found is reported as Clipped
// (its content may be incomplete), never as complete.
func trimCoverage(c *Coverage, diff string, kept int, types map[string]provider.ChangeType) {
	lines := strings.Split(diff, "\n")
	order := append(slices.Clone(c.Included), c.Clipped...)
	wasClipped := map[string]bool{}
	for _, p := range c.Clipped {
		wasClipped[p] = true
	}
	starts := make([]int, len(order))
	cur, lost := 0, len(order)
	for i, p := range order {
		starts[i] = -1
		hdrs := patch.FileHeaderLines(p)
		for j := cur; j < len(lines); j++ {
			if slices.Contains(hdrs, lines[j]) {
				starts[i], cur = j, j+1
				break
			}
		}
		if starts[i] < 0 {
			lost = i
			break
		}
	}
	bodyEnd := len(lines)
	sections := []string{
		strings.TrimSuffix(patch.AddedFilesHeader, "\n"),
		strings.TrimSuffix(patch.ModifiedFilesHeader, "\n"),
		strings.TrimSuffix(patch.DeletedFilesHeader, "\n"),
	}
	if lost > 0 {
		for j := starts[lost-1] + 1; j < len(lines); j++ {
			if slices.Contains(sections, lines[j]) {
				bodyEnd = j
				break
			}
		}
	}

	c.Included, c.Clipped = []string{}, []string{}
	for i, p := range order {
		if i >= lost {
			c.Clipped = append(c.Clipped, p)
			continue
		}
		end := bodyEnd
		if i+1 < lost {
			end = starts[i+1]
		}
		// The file's content ends at its last non-blank line.
		for end > starts[i]+1 && strings.TrimSpace(lines[end-1]) == "" {
			end--
		}
		switch {
		case kept >= end:
			if wasClipped[p] {
				c.Clipped = append(c.Clipped, p)
			} else {
				c.Included = append(c.Included, p)
			}
		case kept > starts[i]:
			c.Clipped = append(c.Clipped, p)
		default:
			switch types[p] {
			case provider.ChangeAdded:
				c.Omitted.Added = append(c.Omitted.Added, p)
			case provider.ChangeDeleted:
				c.Omitted.Deleted = append(c.Omitted.Deleted, p)
			default:
				c.Omitted.Modified = append(c.Omitted.Modified, p)
			}
		}
	}
}
