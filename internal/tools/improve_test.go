package tools

import "testing"

func TestPRImproveArgsValidate(t *testing.T) {
	cases := []struct {
		name string
		a    PRImproveArgs
		want string
	}{
		{"defaults", PRImproveArgs{}, ""},
		{"language", PRImproveArgs{OutputLanguage: "tr-TR"}, ""},
		{"bad locale", PRImproveArgs{OutputLanguage: "tr_TR"}, InvalidOutputLanguageMessage},
		{"publish refused", PRImproveArgs{Publish: true}, ImprovePublishUnavailableMessage},
		// The argument check comes before the publish refusal.
		{"order", PRImproveArgs{Publish: true, OutputLanguage: "x_y"}, InvalidOutputLanguageMessage},
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
