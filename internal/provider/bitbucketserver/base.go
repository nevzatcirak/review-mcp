package bitbucketserver

import (
	"context"
	"errors"
	"net/url"
	"strconv"

	"github.com/nevzatcirak/review-mcp/internal/provider"
	"github.com/nevzatcirak/review-mcp/internal/provider/httpx"
)

// ensureSupported runs the version probe once per provider instance (DQ-20).
// It returns unsupported_version when the probe succeeds and the major
// version is below 7. A probe that fails for any other reason is logged at
// debug level and ignored: feature detection decides the rest.
func (p *Provider) ensureSupported(ctx context.Context) error {
	p.probeMu.Lock()
	defer p.probeMu.Unlock()
	if p.probed {
		return p.probeErr
	}
	var props struct {
		Version string `json:"version"`
	}
	if err := p.client.GetJSON(ctx, apiV1+"/application-properties", &props); err != nil {
		p.logger.Debug("bitbucket server version probe failed; continuing", "error", errClass(err))
		// A canceled caller must not poison later calls with a skipped probe.
		p.probed = ctx.Err() == nil
		return nil
	}
	p.probed = true
	major, ok := parseMajor(props.Version)
	if !ok {
		p.logger.Debug("bitbucket server version could not be parsed; continuing")
		return nil
	}
	p.logger.Debug("bitbucket server version probed", "major", major)
	if major < minMajorVersion {
		p.probeErr = &provider.Error{
			Class: provider.ClassUnsupportedVersion,
			Hint:  "Bitbucket Server / Data Center 7.0 or later is required",
		}
	}
	return p.probeErr
}

// parseMajor returns the leading integer of a version string such as
// "8.9.0" or "7.21.3-SNAPSHOT".
func parseMajor(v string) (int, bool) {
	end := 0
	for end < len(v) && v[end] >= '0' && v[end] <= '9' {
		end++
	}
	if end == 0 || end > 6 {
		return 0, false
	}
	n, err := strconv.Atoi(v[:end])
	return n, err == nil
}

// computeBase determines the revision the diff is computed against (DQ-20):
// the merge-base endpoint on 200, the ancestor walk on 404, and a failure on
// anything else (a wrong base is worse than no review).
func (p *Provider) computeBase(ctx context.Context, ref provider.PRRef, targetHead string) (sha, strategy string, err error) {
	rp, err := repoPath(apiLatest, ref)
	if err != nil {
		return "", "", err
	}
	var mb struct {
		ID string `json:"id"`
	}
	err = p.client.GetJSON(ctx, rp+"/pull-requests/"+strconv.FormatInt(ref.Number, 10)+"/merge-base", &mb)
	switch {
	case err == nil:
		if mb.ID == "" {
			return "", "", protocolErr("merge-base response has no commit id")
		}
		return mb.ID, provider.BaseBBSMergeBaseEP, nil
	case errors.Is(err, provider.ErrNotFound):
		p.logger.Debug("merge-base endpoint not available; using the ancestor walk")
		sha, err = p.ancestorWalk(ctx, ref, targetHead)
		if err != nil {
			return "", "", err
		}
		return sha, provider.BaseBBSAncestorWalk, nil
	default:
		return "", "", err
	}
}

// ancestorWalk finds the best common ancestor of the PR and its target
// branch on servers without the merge-base endpoint.
//
//  1. G, the guaranteed ancestor, is the first parent of the oldest PR
//     commit (the last element of the newest-first commit list).
//  2. The destination set is every commit reachable from the target head but
//     not from G, plus G itself.
//  3. Walk the PR commits newest first and, for each, its parents in order;
//     the first parent that is in the destination set is the base. If the PR
//     branch merged the target branch after branching, that parent is a
//     target commit newer than G, so already-merged target commits stay out
//     of the diff.
//  4. If nothing matches, the base is G.
func (p *Provider) ancestorWalk(ctx context.Context, ref provider.PRRef, targetHead string) (string, error) {
	pp, err := prPath(ref)
	if err != nil {
		return "", err
	}
	commits, err := httpx.PagesStartLimit[apiCommit](ctx, p.client, pp+"/commits", pageLimit)
	if err != nil {
		return "", err
	}
	if len(commits) == 0 {
		return "", protocolErr("the pull request has no commits; cannot determine the base revision")
	}
	oldest := commits[len(commits)-1]
	if len(oldest.Parents) == 0 || oldest.Parents[0].ID == "" {
		return "", protocolErr("the oldest pull request commit has no parent; cannot determine the base revision")
	}
	if targetHead == "" {
		return "", protocolErr("the pull request has no target head commit; cannot determine the base revision")
	}
	guaranteed := oldest.Parents[0].ID

	rp, err := repoPath(apiV1, ref)
	if err != nil {
		return "", err
	}
	dest, err := httpx.PagesStartLimit[apiCommit](ctx, p.client,
		rp+"/commits?since="+url.QueryEscape(guaranteed)+"&until="+url.QueryEscape(targetHead), pageLimit)
	if err != nil {
		return "", err
	}
	inDest := make(map[string]struct{}, len(dest)+1)
	for _, c := range dest {
		inDest[c.ID] = struct{}{}
	}
	inDest[guaranteed] = struct{}{}

	for _, c := range commits {
		for _, par := range c.Parents {
			if _, ok := inDest[par.ID]; ok {
				return par.ID, nil
			}
		}
	}
	return guaranteed, nil
}
