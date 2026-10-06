package tools

import (
	"context"
	"strings"
	"unicode/utf8"

	"github.com/nevzatcirak/review-mcp/internal/logging"
	"github.com/nevzatcirak/review-mcp/internal/provider"
	"github.com/nevzatcirak/review-mcp/internal/review"
	"github.com/nevzatcirak/review-mcp/internal/review/anchor"
)

// MaxCreateBodyChars is the most characters (Unicode code points, as for
// pr_ask's question) of a pr_comment_create body. It is a constant, not a
// configuration key (X-9).
const MaxCreateBodyChars = 20000

// Fixed sentences of pr_comment_create (X-6). They never echo an argument.
const (
	CreateBodyEmptyMessage   = "body must not be empty"
	CreateBodyTooLongMessage = "body is too long: at most 20000 characters are allowed"
	CreateBodyMarkerMessage  = "body must not contain a review-mcp marker line"
	CreateLineInvalidMessage = "line must be a positive integer"
	CreateLineNeedsFile      = "line requires file"
	CreateFileNeedsLine      = "file requires line"
	// NotInDiffMessage is the refusal of an inline request whose line is not
	// part of the pull request's diff (X-14). The comment is never posted
	// at PR level instead.
	NotInDiffMessage = "the line is not part of the pull request diff; use a changed or context line of a changed file"
)

// CommentError is a refusal or failure of a tool whose text is a fixed
// sentence; UserMessage shows it as is.
type CommentError struct{ msg string }

func (e *CommentError) Error() string { return e.msg }

// PRCommentCreateArgs are the arguments of pr_comment_create.
type PRCommentCreateArgs struct {
	PRURL string
	Body  string
	// File is the new path of a changed file; empty posts a PR-level comment.
	File string
	// Line is the new-side line of File; nil when not given.
	Line *int
}

// Validate checks the arguments before any network call. The body is
// checked first. A body made only of whitespace is empty; invalid UTF-8 is
// replaced with U+FFFD as ValidateQuestion does. The body is otherwise posted verbatim, but a line that looks like a review-mcp marker
// is refused: the overview lookup adopts a comment of our own author that
// ends in the marker, so a planted one would be edited or counted as ours.
func (a PRCommentCreateArgs) Validate() error {
	body := strings.ToValidUTF8(a.Body, "�")
	switch {
	case strings.TrimSpace(body) == "":
		return &ArgumentError{CreateBodyEmptyMessage}
	case utf8.RuneCountInString(body) > MaxCreateBodyChars:
		return &ArgumentError{CreateBodyTooLongMessage}
	case review.ContainsMarkerLine(body):
		return &ArgumentError{CreateBodyMarkerMessage}
	}
	switch {
	case a.Line != nil && *a.Line <= 0:
		return &ArgumentError{CreateLineInvalidMessage}
	case a.Line != nil && a.File == "":
		return &ArgumentError{CreateLineNeedsFile}
	case a.File != "" && a.Line == nil:
		return &ArgumentError{CreateFileNeedsLine}
	}
	return nil
}

// PRCommentCreateResult is the structured result of pr_comment_create.
type PRCommentCreateResult struct {
	ID     string `json:"id" jsonschema:"id of the posted comment; empty when the server did not report one"`
	URL    string `json:"url" jsonschema:"URL of the posted comment with credentials and query values removed; empty when the server did not report one"`
	Inline bool   `json:"inline" jsonschema:"true when the comment was posted on a line of the diff, false for a PR-level comment"`
}

// PRCommentCreate posts body on the pull request at prURL: at PR level
// without a file, otherwise as an inline comment on the file's line. The
// line must be a new-side line of the provider's own diff (the anchor of
// the review pipeline, anchor.Resolve); otherwise the call is refused with
// NotInDiffMessage and nothing is posted.
func PRCommentCreate(ctx context.Context, resolver PRResolver, a PRCommentCreateArgs) (PRCommentCreateResult, error) {
	if err := a.Validate(); err != nil {
		return PRCommentCreateResult{}, err
	}
	body := strings.ToValidUTF8(a.Body, "�")
	ref, p, err := resolver.Resolve(a.PRURL)
	if err != nil {
		return PRCommentCreateResult{}, err
	}
	if a.File == "" {
		c, err := p.PostComment(ctx, ref, body)
		if err != nil {
			return PRCommentCreateResult{}, err
		}
		if c == nil {
			return PRCommentCreateResult{}, &provider.Error{Class: provider.ClassProtocol}
		}
		return PRCommentCreateResult{ID: c.ID, URL: logging.RedactURL(c.URL)}, nil
	}

	pr, err := p.GetPullRequest(ctx, ref)
	if err != nil {
		return PRCommentCreateResult{}, err
	}
	// Only the requested file's patch is needed, so no other file's
	// contents are fetched.
	d, err := p.GetDiff(ctx, ref, pr, provider.DiffOptions{Include: func(path string) bool { return path == a.File }})
	if err != nil {
		return PRCommentCreateResult{}, err
	}
	var file *provider.FilePatch
	if d != nil {
		for i := range d.Files {
			if d.Files[i].Path == a.File { // exactly, as the review pipeline does
				file = &d.Files[i]
			}
		}
	}
	an, ok := anchor.Resolve(file, *a.Line, *a.Line)
	if !ok || an.Line != *a.Line {
		return PRCommentCreateResult{}, &CommentError{NotInDiffMessage}
	}
	results, err := p.PostInlineComments(ctx, ref, pr, []provider.InlineComment{an.Comment(body)})
	if err != nil {
		return PRCommentCreateResult{}, err
	}
	if len(results) != 1 {
		return PRCommentCreateResult{}, &provider.Error{Class: provider.ClassProtocol}
	}
	if !results[0].Posted {
		return PRCommentCreateResult{}, &CommentError{results[0].Error}
	}
	return PRCommentCreateResult{ID: results[0].ID, URL: logging.RedactURL(results[0].URL), Inline: true}, nil
}

// RenderPRCommentCreateText renders the text content of pr_comment_create: a
// fixed sentence saying where the comment landed, then the comment id.
func RenderPRCommentCreateText(r PRCommentCreateResult) string {
	s := "Comment posted on the pull request."
	if r.Inline {
		s = "Comment posted on the line."
	}
	if r.ID == "" {
		return s + "\n"
	}
	return s + "\n\nComment id: " + codeSpan(r.ID) + "\n"
}
