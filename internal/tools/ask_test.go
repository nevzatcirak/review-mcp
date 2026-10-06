package tools

import (
	"errors"
	"strings"
	"testing"

	"github.com/nevzatcirak/review-mcp/internal/ask"
)

func TestPRAskArgsValidate(t *testing.T) {
	cases := []struct {
		name string
		args PRAskArgs
		want string // "" means valid
	}{
		{"ok", PRAskArgs{Question: "why?"}, ""},
		{"ok language", PRAskArgs{Question: "why?", OutputLanguage: "tr-TR"}, ""},
		{"empty", PRAskArgs{Question: " \n"}, ask.ErrQuestionEmpty.Error()},
		{"too long", PRAskArgs{Question: strings.Repeat("a", ask.MaxQuestionRunes+1)}, ask.ErrQuestionTooLong.Error()},
		{"bad language", PRAskArgs{Question: "why?", OutputLanguage: "tr_TR"}, InvalidOutputLanguageMessage},
		{"question first", PRAskArgs{OutputLanguage: "bad"}, ask.ErrQuestionEmpty.Error()},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := c.args.Validate()
			switch {
			case c.want == "" && err != nil:
				t.Errorf("unexpected error %v", err)
			case c.want != "" && (err == nil || UserMessage(err) != c.want):
				t.Errorf("error = %v, want %q", err, c.want)
			}
		})
	}
}

func TestUserMessageQuestionErrors(t *testing.T) {
	wrapped := errors.Join(errors.New("context"), ask.ErrQuestionTooLong)
	if got := UserMessage(wrapped); got != ask.ErrQuestionTooLong.Error() {
		t.Errorf("UserMessage = %q", got)
	}
}
