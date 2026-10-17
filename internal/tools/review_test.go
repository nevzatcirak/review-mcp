package tools

import (
	"errors"
	"log/slog"
	"testing"

	"github.com/nevzatcirak/review-mcp/internal/config"
	"github.com/nevzatcirak/review-mcp/internal/issuectx"
	"github.com/nevzatcirak/review-mcp/internal/review"
)

func TestPRReviewArgsValidate(t *testing.T) {
	n := func(v int) *int { return &v }
	cases := []struct {
		name string
		a    PRReviewArgs
		want string
	}{
		{"empty", PRReviewArgs{}, ""},
		{"locale", PRReviewArgs{OutputLanguage: "tr-TR", MaxFindings: n(20)}, ""},
		{"short locale", PRReviewArgs{OutputLanguage: "tr", MaxFindings: n(1)}, ""},
		{"bad locale", PRReviewArgs{OutputLanguage: "Turkish please"}, InvalidOutputLanguageMessage},
		{"underscore", PRReviewArgs{OutputLanguage: "tr_TR"}, InvalidOutputLanguageMessage},
		{"zero", PRReviewArgs{MaxFindings: n(0)}, InvalidMaxFindingsMessage},
		{"21", PRReviewArgs{MaxFindings: n(21)}, InvalidMaxFindingsMessage},
		{"issue context", PRReviewArgs{IssueContext: []issuectx.Input{{Key: "PROJ-1", Title: "Retry"}}}, ""},
		{"issue context without a title", PRReviewArgs{IssueContext: []issuectx.Input{{Key: "PROJ-1"}}},
			"issue_context[1].title is required"},
		{"issue context with a bad url", PRReviewArgs{IssueContext: []issuectx.Input{{Key: "PROJ-1", Title: "T",
			URL: "ftp://tracker.example/PROJ-1"}}}, "issue_context[1].url must be an http or https URL"},
	}
	for _, c := range cases {
		err := c.a.Validate()
		switch {
		case c.want == "" && err != nil:
			t.Errorf("%s: unexpected error %v", c.name, err)
		case c.want != "" && (err == nil || UserMessage(err) != c.want):
			t.Errorf("%s: got %v, want %q", c.name, err, c.want)
		}
	}
}

// TestPreparePRReviewRefusesModesFirst: the refusals of X-33 come before
// the LLM client is built or the URL resolved, with their fixed sentences.
func TestPreparePRReviewRefusesModesFirst(t *testing.T) {
	built := 0
	deps := ReviewDeps{
		Config: config.Defaults(),
		NewLLM: func(*config.Config, *slog.Logger) (review.Completer, error) {
			built++
			return nil, errors.New("not reached")
		},
	}
	for _, a := range []PRReviewArgs{
		{PRURL: "https://your-gitea.example/octo/demo/pulls/7", Focus: "security", Publish: true},
		{PRURL: "https://your-gitea.example/octo/demo/pulls/7", Findings: []review.SuppliedFinding{}},
	} {
		_, err := PreparePRReview(deps, a)
		var re *review.Error
		if !errors.As(err, &re) || re.Class != review.ClassInvalidArgument || UserMessage(err) != err.Error() {
			t.Errorf("%+v: err = %v", a, err)
		}
	}
	if built != 0 {
		t.Errorf("the LLM client was built %d times", built)
	}
}

// TestPreparePRReviewIssueContext: an invalid issue_context is refused
// before the LLM client is built; a valid one reaches the pipeline cleaned.
func TestPreparePRReviewIssueContext(t *testing.T) {
	built := 0
	deps := ReviewDeps{
		Config: config.Defaults(),
		NewLLM: func(*config.Config, *slog.Logger) (review.Completer, error) {
			built++
			return nil, errors.New("not reached")
		},
	}
	_, err := PreparePRReview(deps, PRReviewArgs{PRURL: "https://your-gitea.example/octo/demo/pulls/7",
		IssueContext: []issuectx.Input{{Key: "", Title: "T"}}})
	var ae *ArgumentError
	if !errors.As(err, &ae) || UserMessage(err) != "issue_context[1].key is required" || built != 0 {
		t.Errorf("err = %v, built %d", err, built)
	}
}

// TestPRDescribeArgsIssueContext: pr_describe checks issue_context as
// pr_review does, with the same fixed sentences.
func TestPRDescribeArgsIssueContext(t *testing.T) {
	ok := PRDescribeArgs{IssueContext: []issuectx.Input{{Key: "PROJ-1", Title: "Retry", AcceptanceCriteria: []any{"a"}}}}
	if err := ok.Validate(); err != nil {
		t.Errorf("valid: %v", err)
	}
	bad := PRDescribeArgs{IssueContext: []issuectx.Input{{Key: "PROJ-1", Title: "T", AcceptanceCriteria: 5}}}
	if err := bad.Validate(); err == nil || UserMessage(err) != "issue_context[1].acceptance_criteria must be a string or a list of strings" {
		t.Errorf("err = %v", err)
	}
}
