package ask

import (
	"strings"
	"unicode/utf8"

	"github.com/nevzatcirak/review-mcp/internal/prompt"
)

// MaxQuestionRunes is the longest accepted question, in runes (spec P5
// §1.2 step 2). A longer question is rejected, never truncated.
const MaxQuestionRunes = 8000

// QuestionError is a rejected question. Its text is a fixed sentence that
// never contains the question (X-6, X-8).
type QuestionError struct{ msg string }

// Error returns the fixed sentence.
func (e *QuestionError) Error() string { return e.msg }

// UserMessage is the sentence an entry point may show to a client.
func (e *QuestionError) UserMessage() string { return e.msg }

// Question errors, for errors.Is.
var (
	ErrQuestionEmpty   = &QuestionError{msg: "question must not be empty"}
	ErrQuestionTooLong = &QuestionError{msg: "question is too long: at most 8000 characters are allowed"}
)

// ValidateQuestion returns the question the prompt carries: invalid UTF-8
// replaced with U+FFFD (the sanitization of the P2e comment bodies,
// strings.ToValidUTF8), surrounding whitespace trimmed (as the template
// trims it). It returns ErrQuestionEmpty for a question that is empty
// after trimming and ErrQuestionTooLong for one longer than
// MaxQuestionRunes runes after trimming.
func ValidateQuestion(q string) (string, error) {
	q = prompt.PyStrip(strings.ToValidUTF8(q, "�"))
	if q == "" {
		return "", ErrQuestionEmpty
	}
	if utf8.RuneCountInString(q) > MaxQuestionRunes {
		return "", ErrQuestionTooLong
	}
	return q, nil
}
