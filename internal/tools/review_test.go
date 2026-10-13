package tools

import (
	"errors"
	"log/slog"
	"testing"

	"github.com/nevzatcirak/review-mcp/internal/config"
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
