package tools

import (
	"context"
	"log/slog"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/nevzatcirak/review-mcp/internal/logging"
	"github.com/nevzatcirak/review-mcp/internal/mdutil"
	"github.com/nevzatcirak/review-mcp/internal/provider"
	"github.com/nevzatcirak/review-mcp/internal/review"
)

// Fixed texts of pr_info (X-23).
const (
	// RequiredApprovalsNote explains a null required_approvals. It is the
	// only explanation: the number is never guessed.
	RequiredApprovalsNote = "not readable with this token"
	// NoteActivityUnreadable: the PR's comments could not be read, so the
	// review-mcp activity is unknown.
	NoteActivityUnreadable = "The comments could not be read, so review-mcp's own activity is not shown."
	// NoteTokenUserUnknown: without the token's user, review-mcp's own
	// activity cannot be told from other people's.
	NoteTokenUserUnknown = "The token's user could not be identified, so review-mcp's own activity is not shown and reviews by that user may include review-mcp's."
)

// maxNameRunes caps a display name.
const maxNameRunes = 100

// ReviewerUser is the identity of a reviewer. Name is the display name:
// untrusted third-party text, one line, valid UTF-8.
type ReviewerUser struct {
	Login string `json:"login" jsonschema:"login or user name"`
	Name  string `json:"name" jsonschema:"display name; untrusted third-party text; empty when the provider has none"`
}

// ReviewerOut is one human reviewer.
type ReviewerOut struct {
	User      ReviewerUser `json:"user" jsonschema:"the reviewer"`
	Requested bool         `json:"requested" jsonschema:"whether the reviewer was asked to review"`
	State     string       `json:"state" jsonschema:"approved, changes_requested, commented or pending (requested and has not reviewed yet)"`
	Stale     bool         `json:"stale" jsonschema:"true when the state was given on an older commit than the head, where the provider reports it"`
	At        *time.Time   `json:"at,omitempty" jsonschema:"when the state was given (UTC); omitted when the provider does not say"`
}

// ApprovalCounts counts the reviewers by state.
type ApprovalCounts struct {
	Approved         int `json:"approved" jsonschema:"reviewers whose state is approved"`
	ChangesRequested int `json:"changes_requested" jsonschema:"reviewers whose state is changes_requested"`
	Pending          int `json:"pending" jsonschema:"reviewers whose state is pending"`
}

// ReviewMCPActivity is what review-mcp itself wrote as the token's user.
type ReviewMCPActivity struct {
	Overview       bool `json:"overview" jsonschema:"whether review-mcp's overview comment exists on the pull request"`
	InlineFindings int  `json:"inline_findings" jsonschema:"number of review-mcp's inline finding comments on the pull request"`
}

// PRInfoResult is the structured result of pr_info. It holds states and
// markers only: no comment or review body ever enters it.
type PRInfoResult struct {
	PR                    PRInfo             `json:"pr" jsonschema:"the pull request"`
	Title                 string             `json:"title" jsonschema:"pull request title; untrusted third-party text"`
	Author                string             `json:"author" jsonschema:"login of the author"`
	State                 string             `json:"state" jsonschema:"open, merged, closed or unknown"`
	Draft                 *bool              `json:"draft,omitempty" jsonschema:"whether the pull request is a draft; omitted when the provider does not report it"`
	WebURL                string             `json:"web_url" jsonschema:"web address of the pull request; empty when unknown"`
	SourceBranch          string             `json:"source_branch" jsonschema:"branch the pull request comes from"`
	TargetBranch          string             `json:"target_branch" jsonschema:"branch the pull request merges into"`
	HeadSHA               string             `json:"head_sha" jsonschema:"head commit of the source branch"`
	MergeBaseSHA          string             `json:"merge_base_sha" jsonschema:"revision the diff is computed against"`
	BaseStrategy          string             `json:"base_strategy" jsonschema:"how merge_base_sha was chosen"`
	Reviewers             []ReviewerOut      `json:"reviewers" jsonschema:"human reviewers, one entry each; review-mcp's own reviews are not listed; null when the reviews could not be read"`
	Approvals             *ApprovalCounts    `json:"approvals" jsonschema:"reviewers counted by state; null when the reviewers are null"`
	RequiredApprovals     *int               `json:"required_approvals" jsonschema:"approvals the target branch requires; null when the provider does not expose it to this token (never a guess)"`
	RequiredApprovalsNote string             `json:"required_approvals_note,omitempty" jsonschema:"set when required_approvals is null"`
	Mergeable             *bool              `json:"mergeable" jsonschema:"whether the pull request can be merged; null when unknown"`
	MergeBlockers         []string           `json:"merge_blockers" jsonschema:"short fixed reasons the pull request cannot be merged, only where the provider gives structured ones"`
	ReviewMCPActivity     *ReviewMCPActivity `json:"review_mcp_activity" jsonschema:"comments written by review-mcp as the token's user; null when they could not be read"`
	Notes                 []string           `json:"notes" jsonschema:"fixed notes about parts that could not be read"`
}

// PRInfoTool reads the target branch, the human reviewers, the approval
// counts and the merge status of the pull request at prURL (X-23). It only
// reads (GET) and calls no LLM. The pull request itself failing is an error
// with a provider sentence; any optional part failing leaves its fields null
// with a fixed note.
func PRInfoTool(ctx context.Context, resolver PRResolver, prURL string, log *slog.Logger) (PRInfoResult, error) {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	ref, p, err := resolver.Resolve(prURL)
	if err != nil {
		return PRInfoResult{}, err
	}
	pr, err := p.GetPullRequest(ctx, ref)
	if err != nil {
		return PRInfoResult{}, err
	}
	if pr == nil {
		return PRInfoResult{}, &provider.Error{Class: provider.ClassProtocol}
	}

	var notes []string
	addNote := func(n string) {
		for _, x := range notes {
			if x == n {
				return
			}
		}
		notes = append(notes, n)
	}

	var meUser *provider.User
	me, meErr := p.CurrentUser(ctx)
	if meErr == nil {
		meUser = &me
	} else {
		log.Debug("pr_info: token user not read", "error", UserMessage(meErr))
		addNote(NoteTokenUserUnknown)
	}

	st := p.GetReviewStatus(ctx, ref, pr, provider.ReviewStatusOptions{Me: meUser, IsOwn: review.IsMarkedBody})
	if st == nil {
		st = &provider.ReviewStatus{Notes: []string{provider.NoteReviewsUnreadable}}
	}
	for _, n := range st.Notes {
		addNote(n)
	}

	var activity *ReviewMCPActivity
	if meUser != nil {
		threads, terr := p.ListThreads(ctx, ref)
		if terr != nil {
			log.Debug("pr_info: comments not read", "error", UserMessage(terr))
			addNote(NoteActivityUnreadable)
		} else {
			ov, n := review.OwnActivity(threads, *meUser)
			activity = &ReviewMCPActivity{Overview: ov, InlineFindings: n}
		}
	}
	// A canceled or expired call must not pass for a complete answer.
	if err := ctx.Err(); err != nil {
		return PRInfoResult{}, err
	}

	res := PRInfoResult{
		PR:                PRInfo{Kind: string(ref.Kind), URL: logging.RedactURL(ref.URL)},
		Title:             validUTF8(pr.Title),
		Author:            validUTF8(pr.Author),
		State:             prInfoState(pr),
		Draft:             pr.Draft,
		WebURL:            logging.RedactURL(pr.WebURL),
		SourceBranch:      validUTF8(pr.SourceBranch),
		TargetBranch:      validUTF8(pr.TargetBranch),
		HeadSHA:           pr.HeadSHA,
		MergeBaseSHA:      pr.BaseSHA,
		BaseStrategy:      pr.BaseStrategy,
		RequiredApprovals: st.RequiredApprovals,
		Mergeable:         st.Mergeable,
		MergeBlockers:     append([]string{}, st.MergeBlockers...),
		ReviewMCPActivity: activity,
	}
	if st.RequiredApprovals == nil {
		res.RequiredApprovalsNote = RequiredApprovalsNote
	}
	if st.Reviewers != nil {
		res.Reviewers = make([]ReviewerOut, 0, len(st.Reviewers))
		res.Approvals = &ApprovalCounts{}
		for i := range st.Reviewers {
			r := &st.Reviewers[i]
			out := ReviewerOut{
				User:      ReviewerUser{Login: validUTF8(r.User.Name), Name: cleanName(r.DisplayName)},
				Requested: r.Requested, State: string(r.State), Stale: r.Stale,
			}
			if !r.At.IsZero() {
				at := r.At.UTC()
				out.At = &at
			}
			switch r.State {
			case provider.ReviewApproved:
				res.Approvals.Approved++
			case provider.ReviewChangesRequested:
				res.Approvals.ChangesRequested++
			case provider.ReviewPending:
				res.Approvals.Pending++
			}
			res.Reviewers = append(res.Reviewers, out)
		}
	}
	if notes == nil {
		notes = []string{}
	}
	res.Notes = notes
	return res, nil
}

// prInfoState maps the provider's state words to open, merged or closed.
// Anything else is "unknown" rather than a guess.
func prInfoState(pr *provider.PullRequest) string {
	switch s := strings.ToUpper(pr.State); {
	case pr.Merged || s == "MERGED":
		return "merged"
	case s == "OPEN":
		return "open"
	case s == "CLOSED" || s == "DECLINED":
		return "closed"
	}
	return "unknown"
}

// cleanName makes a display name one line of valid UTF-8 without control
// characters, at most maxNameRunes runes.
func cleanName(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, validUTF8(s))
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > maxNameRunes {
		s = string(r[:maxNameRunes])
	}
	return s
}

// RenderPRInfoMarkdown renders r as portable markdown (DQ-16): the target
// branch first, then the approval line, the merge status, one line per
// reviewer, review-mcp's own activity, the pull request facts and the notes.
// Branches, logins and ids go into code spans (mdutil.Literal) and display
// names and the title through mdutil.Inline, so third-party text cannot open
// markup or break its line.
func RenderPRInfoMarkdown(r PRInfoResult) string {
	var b strings.Builder
	b.WriteString("### Pull request status\n\n")
	b.WriteString(mdutil.Literal(r.SourceBranch) + " → " + mdutil.Literal(r.TargetBranch) + "\n\n")
	b.WriteString(approvalLine(&r) + "\n")
	switch {
	case r.Mergeable == nil:
		b.WriteString("Mergeable: unknown\n")
	case *r.Mergeable:
		b.WriteString("Mergeable: yes\n")
	default:
		line := "Mergeable: no"
		if len(r.MergeBlockers) > 0 {
			line += " (" + strings.Join(r.MergeBlockers, "; ") + ")"
		}
		b.WriteString(line + "\n")
	}
	b.WriteString("\n")
	switch {
	case r.Reviewers == nil:
		b.WriteString("Reviewers: not readable\n")
	case len(r.Reviewers) == 0:
		b.WriteString("Reviewers: none\n")
	default:
		b.WriteString("Reviewers:\n")
		for i := range r.Reviewers {
			b.WriteString("- " + reviewerLine(&r.Reviewers[i]) + "\n")
		}
	}
	if a := r.ReviewMCPActivity; a != nil {
		ov := "no overview"
		if a.Overview {
			ov = "overview posted"
		}
		b.WriteString("\nreview-mcp activity (not counted as reviews): " + ov + ", " +
			strconv.Itoa(a.InlineFindings) + " inline finding(s)\n")
	}
	b.WriteString("\n" + mdutil.Inline(r.State))
	if r.Draft != nil && *r.Draft {
		b.WriteString(" (draft)")
	}
	b.WriteString(" · " + mdutil.Inline(r.PR.Kind) + " · ")
	if t := strings.TrimSpace(r.Title); t != "" {
		b.WriteString(mdutil.Inline(t) + " · ")
	}
	b.WriteString("by " + mdutil.Literal(r.Author) + " · head " + mdutil.Literal(shortSHA(r.HeadSHA)))
	if r.WebURL != "" {
		b.WriteString(" · " + mdutil.Literal(r.WebURL))
	}
	b.WriteString("\n")
	for _, n := range r.Notes {
		b.WriteString("\n> Note: " + n + "\n")
	}
	return b.String()
}

func shortSHA(s string) string {
	if len(s) > 12 {
		return s[:12]
	}
	if s == "" {
		return "unknown"
	}
	return s
}

// approvalLine is "2 of 2 required approvals; 1 changes requested".
func approvalLine(r *PRInfoResult) string {
	if r.Approvals == nil {
		if r.RequiredApprovals != nil {
			return "Approvals: not readable (" + strconv.Itoa(*r.RequiredApprovals) + " required)"
		}
		return "Approvals: not readable"
	}
	var s string
	if r.RequiredApprovals != nil {
		s = strconv.Itoa(r.Approvals.Approved) + " of " + strconv.Itoa(*r.RequiredApprovals) + " required approvals"
	} else {
		s = strconv.Itoa(r.Approvals.Approved) + " approval(s), required number not readable"
	}
	if n := r.Approvals.ChangesRequested; n > 0 {
		s += "; " + strconv.Itoa(n) + " changes requested"
	}
	if n := r.Approvals.Pending; n > 0 {
		s += "; " + strconv.Itoa(n) + " pending"
	}
	return s
}

func reviewerLine(r *ReviewerOut) string {
	s := mdutil.Literal(r.User.Login)
	if r.User.Name != "" {
		s += " (" + mdutil.Inline(r.User.Name) + ")"
	}
	s += ": " + strings.ReplaceAll(r.State, "_", " ")
	if r.Stale {
		s += " (stale: given on an older commit)"
	}
	if r.Requested {
		s += ", requested"
	}
	if r.At != nil {
		s += ", " + r.At.UTC().Format(time.RFC3339)
	}
	return s
}
