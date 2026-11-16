package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/nevzatcirak/review-mcp/tools/evalset/cases"
)

// Judge verdicts (V-9 spec §2.3). verdictError and verdictUnparsed mark an
// item the judge could not settle; it stays a false positive.
const (
	verdictMatches  = "matches:"
	verdictValid    = "valid_unlisted"
	verdictInvalid  = "invalid"
	verdictError    = "error"
	verdictUnparsed = "unparsed"
)

// judgeConfig holds the judge's settings, from the environment only.
type judgeConfig struct {
	BaseURL, Model, key string
	client              *http.Client
}

func judgeFromEnv(getenv func(string) string) (judgeConfig, error) {
	c := judgeConfig{BaseURL: strings.TrimRight(getenv(envJudgeURL), "/"), Model: strings.TrimSpace(getenv(envJudgeModel)), key: getenv(envJudgeKey)}
	if c.BaseURL == "" || c.Model == "" || c.key == "" {
		return c, fmt.Errorf("-judge needs %s, %s and %s in the environment", envJudgeURL, envJudgeModel, envJudgeKey)
	}
	u, err := url.Parse(c.BaseURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
		return c, fmt.Errorf("%s must be an http or https URL without credentials", envJudgeURL)
	}
	c.client = &http.Client{Timeout: 120 * time.Second}
	return c, nil
}

// checkJudgeModel refuses a judge that is the reviewer (V-9 spec §0 item 7,
// canary c3): the judge model must differ from every model recorded in the
// runs. Names are compared without case, and also by their last path
// segment ("provider/model-a" is "model-a"). With no reviewer model
// recorded, the check cannot be made, and the judge is refused too.
func checkJudgeModel(judge string, reviewers []string) error {
	if len(reviewers) == 0 {
		return errors.New("-judge is refused: no reviewer model is recorded in run.json, so the judge cannot be shown to differ from it")
	}
	for _, r := range reviewers {
		if sameModel(judge, r) {
			return errors.New("-judge is refused: the judge model is the reviewer model recorded in the runs; use a different model")
		}
	}
	return nil
}

func sameModel(a, b string) bool {
	norm := func(s string) string {
		s = strings.ToLower(strings.TrimSpace(s))
		if i := strings.LastIndexByte(s, '/'); i >= 0 {
			s = s[i+1:]
		}
		return s
	}
	return norm(a) != "" && norm(a) == norm(b)
}

const judgeSystem = `You check one finding of an automated code review of a synthetic pull request against the known issues of that pull request.
Answer with one JSON object and nothing else: {"verdict": "...", "reason": "..."}.
verdict is "matches:<issue-id>" when the finding describes that listed issue, "valid_unlisted" when it describes a real problem of the change that is not listed, and "invalid" otherwise.
reason is one line.`

// runJudge asks the judge about every false-positive candidate and applies
// the verdicts. It returns the number of items judged and of items the
// judge could not settle.
func runJudge(ctx context.Context, jc judgeConfig, runs []*runScore) (int, int) {
	diffs := map[string]string{}
	judged, failed := 0, 0
	for _, rs := range runs {
		for i := range rs.findings {
			s := &rs.findings[i]
			if s.issue >= 0 {
				continue
			}
			d, ok := diffs[rs.c.ID]
			if !ok {
				d = caseDiff(rs.c)
				diffs[rs.c.ID] = d
			}
			judged++
			s.verdict, s.reason = askJudge(ctx, jc, rs.c, d, s.f)
			if s.verdict == verdictError || s.verdict == verdictUnparsed {
				failed++
				continue
			}
			if id, ok := strings.CutPrefix(s.verdict, verdictMatches); ok {
				for ii, is := range rs.c.Issues {
					if is.ID == id {
						s.issue, s.rule = ii, ruleJudge
						if rs.matched[ii] == "" {
							rs.matched[ii] = ruleJudge
						}
					}
				}
			}
		}
	}
	return judged, failed
}

// verdictClass folds matches:<id> into "matches".
func verdictClass(v string) string {
	if strings.HasPrefix(v, verdictMatches) {
		return "matches"
	}
	return v
}

// askJudge sends one item. The key goes only into the Authorization header;
// a failure is the verdict "error", never an error text.
func askJudge(ctx context.Context, jc judgeConfig, c *cases.Case, diff string, f finding) (string, string) {
	var u strings.Builder
	fmt.Fprintf(&u, "Pull request title: %s\nDescription: %s\n\nDiff:\n%s\n\nKnown issues:\n", c.Title, c.Description, diff)
	if len(c.Issues) == 0 {
		u.WriteString("none: the change is clean\n")
	}
	for _, is := range c.Issues {
		fmt.Fprintf(&u, "- %s (%s) %s lines %d-%d: %s\n", is.ID, is.Severity, is.Path, is.Lines[0], is.Lines[1], is.Summary)
	}
	fmt.Fprintf(&u, "\nFinding:\nfile: %s\nlines: %s\ntitle: %s\ntext: %s\n", f.File, lines(f), oneLine(f.Header), f.Content)
	body, _ := json.Marshal(map[string]any{
		"model":       jc.Model,
		"temperature": 0,
		"messages": []map[string]string{
			{"role": "system", "content": judgeSystem},
			{"role": "user", "content": u.String()},
		},
	})
	for attempt := 0; attempt < 2; attempt++ {
		content, retry, err := postJudge(ctx, jc, body)
		if err == nil {
			ids := make([]string, len(c.Issues))
			for i, is := range c.Issues {
				ids[i] = is.ID
			}
			return parseVerdict(content, ids)
		}
		if !retry {
			break
		}
	}
	return verdictError, ""
}

func postJudge(ctx context.Context, jc judgeConfig, body []byte) (string, bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, jc.BaseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", false, err
	}
	req.Header.Set("Authorization", "Bearer "+jc.key)
	req.Header.Set("Content-Type", "application/json")
	resp, err := jc.client.Do(req)
	if err != nil {
		return "", true, errors.New("unreachable")
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
		return "", resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500, errors.New("status")
	}
	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&out); err != nil || len(out.Choices) == 0 {
		return "", false, errors.New("unreadable")
	}
	return out.Choices[0].Message.Content, false, nil
}

// parseVerdict reads the judge's answer: the JSON object asked for, or a
// first line of the form "<verdict>: <reason>". A matches: verdict must
// name an issue of the case.
func parseVerdict(content string, ids []string) (string, string) {
	verdict, reason := "", ""
	if i, j := strings.IndexByte(content, '{'), strings.LastIndexByte(content, '}'); i >= 0 && j > i {
		var v struct {
			Verdict string `json:"verdict"`
			Reason  string `json:"reason"`
		}
		if json.Unmarshal([]byte(content[i:j+1]), &v) == nil {
			verdict, reason = v.Verdict, v.Reason
		}
	}
	if verdict == "" {
		first := firstLine(content)
		for _, p := range []string{verdictValid, verdictInvalid} {
			if strings.HasPrefix(strings.ToLower(first), p) {
				verdict, reason = p, strings.TrimLeft(first[len(p):], " :-")
			}
		}
		if strings.HasPrefix(strings.ToLower(first), verdictMatches) {
			rest := first[len(verdictMatches):]
			id, r, _ := strings.Cut(strings.TrimSpace(rest), " ")
			verdict, reason = verdictMatches+strings.TrimRight(id, ":,"), strings.TrimLeft(r, " :-")
		}
	}
	verdict = strings.TrimSpace(verdict)
	reason = oneLine(reason)
	if len(reason) > 300 {
		reason = reason[:300]
	}
	switch low := strings.ToLower(verdict); {
	case low == verdictValid, low == verdictInvalid:
		return low, reason
	case strings.HasPrefix(low, verdictMatches):
		if id := strings.TrimSpace(verdict[len(verdictMatches):]); slices.Contains(ids, id) {
			return verdictMatches + id, reason
		}
	}
	return verdictUnparsed, reason
}
