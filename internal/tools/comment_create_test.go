package tools

import (
	"errors"
	"strings"
	"testing"

	"github.com/nevzatcirak/review-mcp/internal/provider"
)

func intp(n int) *int { return &n }

// TestPRCommentCreateValidate is the argument table of pr_comment_create:
// every refusal is one of the fixed sentences and never echoes the input.
func TestPRCommentCreateValidate(t *testing.T) {
	const marker = "[//]: # (review-mcp:overview:v1)"
	cases := []struct {
		name string
		a    PRCommentCreateArgs
		want string // "" when valid
	}{
		{"pr-level", PRCommentCreateArgs{Body: "hello"}, ""},
		{"inline", PRCommentCreateArgs{Body: "hello", File: "a.go", Line: intp(3)}, ""},
		{"empty body", PRCommentCreateArgs{}, CreateBodyEmptyMessage},
		{"blank body", PRCommentCreateArgs{Body: " \t\r\n "}, CreateBodyEmptyMessage},
		{"20000 characters", PRCommentCreateArgs{Body: strings.Repeat("a", 20000)}, ""},
		{"20001 characters", PRCommentCreateArgs{Body: strings.Repeat("a", 20001)}, CreateBodyTooLongMessage},
		// Runes, not bytes: 20000 two-byte characters are 40000 bytes.
		{"20000 multi-byte characters", PRCommentCreateArgs{Body: strings.Repeat("é", 20000)}, ""},
		{"20001 multi-byte characters", PRCommentCreateArgs{Body: strings.Repeat("é", 20001)}, CreateBodyTooLongMessage},
		{"invalid utf-8 is replaced, not refused", PRCommentCreateArgs{Body: "a\xffb"}, ""},
		{"overview marker, last line", PRCommentCreateArgs{Body: "text\n" + marker}, CreateBodyMarkerMessage},
		{"overview marker, first line", PRCommentCreateArgs{Body: marker + "\ntext"}, CreateBodyMarkerMessage},
		{"finding marker", PRCommentCreateArgs{Body: "text\n[//]: # (review-mcp:finding:0123456789ab)\n"}, CreateBodyMarkerMessage},
		{"marker, upper case and indented", PRCommentCreateArgs{Body: "text\n   [//]: # (REVIEW-MCP:Overview:V1)  \t"}, CreateBodyMarkerMessage},
		{"marker, CRLF", PRCommentCreateArgs{Body: "text\r\n" + marker + "\r\n"}, CreateBodyMarkerMessage},
		{"marker of a future version", PRCommentCreateArgs{Body: "[//]: # (review-mcp:future:v2)"}, CreateBodyMarkerMessage},
		{"marker words inside a sentence", PRCommentCreateArgs{Body: "the review-mcp: prefix and [//]: # are fine apart"}, ""},
		{"other hidden comment", PRCommentCreateArgs{Body: "text\n[//]: # (note)"}, ""},
		{"line without file", PRCommentCreateArgs{Body: "x", Line: intp(3)}, CreateLineNeedsFile},
		{"file without line", PRCommentCreateArgs{Body: "x", File: "a.go"}, CreateFileNeedsLine},
		{"line zero", PRCommentCreateArgs{Body: "x", File: "a.go", Line: intp(0)}, CreateLineInvalidMessage},
		{"negative line", PRCommentCreateArgs{Body: "x", File: "a.go", Line: intp(-4)}, CreateLineInvalidMessage},
		{"body is checked before the line", PRCommentCreateArgs{File: "a.go", Line: intp(0)}, CreateBodyEmptyMessage},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := c.a.Validate()
			if c.want == "" {
				if err != nil {
					t.Fatalf("Validate = %v, want nil", err)
				}
				return
			}
			var ae *ArgumentError
			if !errors.As(err, &ae) || ae.Error() != c.want || UserMessage(err) != c.want {
				t.Fatalf("Validate = %v, want %q", err, c.want)
			}
		})
	}
}

func TestUserMessageShowsCommentError(t *testing.T) {
	if got := UserMessage(&CommentError{NotInDiffMessage}); got != NotInDiffMessage {
		t.Errorf("UserMessage = %q", got)
	}
	// A wrapped *provider.Error keeps its own sentence.
	pe := &provider.Error{Class: provider.ClassRateLimited, Status: 429}
	if UserMessage(pe) != pe.Error() {
		t.Error("provider error sentence changed")
	}
}
