package tools

import "testing"

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
