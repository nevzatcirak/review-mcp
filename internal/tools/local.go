package tools

import (
	"context"
	"errors"
	"path/filepath"

	"github.com/nevzatcirak/review-mcp/internal/gitctx"
	"github.com/nevzatcirak/review-mcp/internal/issuectx"
	"github.com/nevzatcirak/review-mcp/internal/provider"
	"github.com/nevzatcirak/review-mcp/internal/provider/local"
	"github.com/nevzatcirak/review-mcp/internal/review"
	"github.com/nevzatcirak/review-mcp/internal/review/render"
)

// LocalReviewArgs are the arguments of local_review (v2.10 spec V-20 §2.1).
// publish, findings, incremental and related do not exist for local work.
type LocalReviewArgs struct {
	// RepoPath is the absolute path of the work tree or a directory in it.
	RepoPath string
	// Base is a ref name or a hexadecimal commit; "" tries the defaults.
	Base string
	// IncludeUncommitted is nil when not given (default true);
	// IncludeUntracked is nil when not given (default false).
	IncludeUncommitted *bool
	IncludeUntracked   *bool
	Focus              string
	ExtraInstructions  string
	OutputLanguage     string
	MaxFindings        *int
	IssueContext       []issuectx.Input
}

// localURL is the one value the local resolver answers: no provider
// Resolver accepts it (it is not an http or https URL), so no URL-taking
// tool can reach a local source (E4).
const localURL = "local:"

// localResolver hands the pipeline the one opened local source, for
// localURL only (E4 of the WP-24a report).
type localResolver struct {
	src *local.Source
}

func (r localResolver) Resolve(rawURL string) (provider.PRRef, provider.Provider, error) {
	if rawURL != localURL {
		return provider.PRRef{}, nil, &provider.Error{Class: provider.ClassURLNotConfigured}
	}
	return r.src.Ref(), r.src, nil
}

// LocalReviewCall is a local_review call whose arguments are valid and
// whose repository and base are resolved. Run reviews it; Close releases
// the source (Run closes it too).
type LocalReviewCall struct {
	deps   ReviewDeps
	client review.Completer
	args   review.Args
	src    *local.Source
}

// PrepareLocalReview checks the arguments, builds the LLM client and opens
// the local source: the repository path (and local.allowed_roots), the
// base and the merge base. Nothing goes to the model and nothing touches
// the network. The error, if any, is classified; use UserMessage for its
// text.
func PrepareLocalReview(ctx context.Context, deps ReviewDeps, a LocalReviewArgs) (*LocalReviewCall, error) {
	if err := (PRReviewArgs{OutputLanguage: a.OutputLanguage, MaxFindings: a.MaxFindings, IssueContext: a.IssueContext}).Validate(); err != nil {
		return nil, err
	}
	args := review.Args{
		PRURL:             localURL,
		ExtraInstructions: a.ExtraInstructions,
		OutputLanguage:    a.OutputLanguage,
		Focus:             a.Focus,
		Local:             true,
	}
	if a.MaxFindings != nil {
		args.MaxFindings = *a.MaxFindings
	}
	args.IssueContext, _ = issuectx.Validate(a.IssueContext)
	args, err := args.Validated(deps.Config)
	if err != nil {
		return nil, err
	}
	client, err := deps.NewLLM(deps.Config, deps.Logger)
	if err != nil {
		return nil, err
	}
	req := local.DefaultRequest(a.RepoPath)
	req.Base = a.Base
	if a.IncludeUncommitted != nil {
		req.IncludeUncommitted = *a.IncludeUncommitted
	}
	if a.IncludeUntracked != nil {
		req.IncludeUntracked = *a.IncludeUntracked
	}
	opts := local.OptionsFrom(deps.Config)
	opts.AllowedRoots = deps.Config.Local.AllowedRoots
	opts.Logger = deps.Logger
	src, err := local.Open(ctx, opts, req)
	if err != nil {
		return nil, err
	}
	deps.Resolver = localResolver{src: src}
	args = args.WithConfigDefaults(deps.Config)
	return &LocalReviewCall{deps: deps, client: client, args: args, src: src}, nil
}

// Close releases the local source: its temporary directory is removed; the
// repository is not touched.
func (c *LocalReviewCall) Close() { _ = c.src.Close() }

// Info describes what the call reviews.
func (c *LocalReviewCall) Info() local.Info { return c.src.Info() }

// Run runs the review pipeline on the local source and renders the
// client-profile markdown. The result carries pr.kind local, no URL, the
// local object, and the notes of a remote-tracking base and of untracked
// files held back. It closes the source. The error, if any, is classified.
func (c *LocalReviewCall) Run(ctx context.Context, progress func(stage string)) (*review.Result, string, error) {
	defer c.Close()
	res, err := review.Run(ctx, review.Deps{
		Config:   c.deps.Config,
		Logger:   c.deps.Logger,
		Resolver: c.deps.Resolver,
		LLM:      c.client,
		Progress: progress,
	}, c.args)
	if err != nil {
		return nil, "", err
	}
	in := c.src.Info()
	res.Local = &review.LocalInfo{RepoPath: in.RepoPath, BaseRef: in.BaseRef, BaseSHA: in.BaseSHA,
		MergeBase: in.MergeBase, HeadSHA: in.HeadSHA, Uncommitted: in.Uncommitted,
		UntrackedFiles: in.UntrackedFiles, UntrackedHeldBack: in.HeldBack}
	var notes []string
	for _, n := range []string{c.src.StaleBaseNote(), c.src.HeldBackNote()} {
		if n != "" {
			notes = append(notes, n)
		}
	}
	// notes is never nil: the result's notes are always a list.
	res.Notes = append(append([]string{}, notes...), res.Notes...)
	if progress != nil {
		progress(review.StageRendering)
	}
	return res, render.Client(res), nil
}

// localUserMessage is the fixed sentence of a local repository error, or
// "" for another error.
func localUserMessage(err error) string {
	var le *gitctx.LocalError
	if errors.As(err, &le) {
		return le.Error()
	}
	var be *local.Error
	if errors.As(err, &be) {
		return be.Error()
	}
	return ""
}

// LocalLogFields are the debug fields of a local review: the work tree's
// base name and counts only, never a full path or file content (canary k).
func LocalLogFields(in local.Info) []any {
	return []any{"repo", filepath.Base(in.RepoPath), "base_ref", in.BaseRef, "uncommitted", in.Uncommitted,
		"untracked", in.UntrackedFiles, "held_back", in.HeldBack}
}
