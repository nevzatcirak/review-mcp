package tools

import "testing"

func TestPRDescribeArgsValidate(t *testing.T) {
	cases := []struct {
		name string
		a    PRDescribeArgs
		want string
	}{
		{"defaults", PRDescribeArgs{}, ""},
		{"comment mode", PRDescribeArgs{PublishMode: PublishModeComment, OutputLanguage: "tr-TR"}, ""},
		{"description mode without publish", PRDescribeArgs{PublishMode: PublishModeDescription}, ""},
		{"bad locale", PRDescribeArgs{OutputLanguage: "tr_TR"}, InvalidOutputLanguageMessage},
		{"bad mode", PRDescribeArgs{PublishMode: "Comment"}, InvalidPublishModeMessage},
		{"update_title without publish", PRDescribeArgs{UpdateTitle: true, PublishMode: PublishModeDescription}, InvalidUpdateTitleMessage},
		{"update_title in comment mode", PRDescribeArgs{UpdateTitle: true, Publish: true}, InvalidUpdateTitleMessage},
		{"publish refused", PRDescribeArgs{Publish: true}, DescribePublishUnavailableMessage},
		{"publish description refused", PRDescribeArgs{Publish: true, PublishMode: PublishModeDescription, UpdateTitle: true}, DescribePublishUnavailableMessage},
		// The argument checks come before the publish refusal.
		{"order", PRDescribeArgs{Publish: true, PublishMode: "x"}, InvalidPublishModeMessage},
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
