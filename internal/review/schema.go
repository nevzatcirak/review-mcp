package review

import (
	"github.com/nevzatcirak/review-mcp/internal/issuectx"
	"github.com/nevzatcirak/review-mcp/internal/llmrun"
	"github.com/nevzatcirak/review-mcp/internal/related"
	"github.com/nevzatcirak/review-mcp/internal/repocfg"
)

// ResultSchema is the JSON schema (draft 2020-12) of Result, for the
// pr_review tool's MCP output schema (DQ-6; WP-PR-4e attaches it). The
// review part comes from the descriptor table (ReviewSchema).
//
// It is a plain map rather than a github.com/google/jsonschema-go value:
// the MCP SDK's Tool.OutputSchema accepts any value that marshals to a JSON
// schema, and that module is only an indirect dependency of this one.
func ResultSchema() map[string]any {
	str := func(desc string) map[string]any { return map[string]any{"type": "string", "description": desc} }
	integer := func(desc string) map[string]any { return map[string]any{"type": "integer", "description": desc} }
	boolean := func(desc string) map[string]any { return map[string]any{"type": "boolean", "description": desc} }
	strList := func(desc string) map[string]any {
		return map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": desc}
	}
	object := func(desc string, props map[string]any, required ...string) map[string]any {
		req := []any{}
		for _, r := range required {
			req = append(req, r)
		}
		return map[string]any{"type": "object", "description": desc, "properties": props,
			"required": req, "additionalProperties": false}
	}
	skipped := func(desc string) map[string]any {
		return map[string]any{"type": "array", "description": desc, "items": object("a file and the reason",
			map[string]any{"path": str("file path"), "reason": str("reason code or filter rule")}, "path", "reason")}
	}
	review := ReviewSchema()
	review["description"] = "the validated review; the optional fields appear only when enabled and usable"

	return object("pr_review result", map[string]any{
		"pr": object("the reviewed pull request", map[string]any{
			"kind":     str("provider kind: gitea, bitbucket_server, github or gitlab"),
			"url":      str("pull request URL with credentials and query values removed"),
			"number":   integer("pull request number"),
			"title":    str("pull request title"),
			"head_sha": str("head commit the review describes"),
		}, "kind", "url", "number", "title"),
		"enabled_fields": strList("the review fields asked for, in order"),
		"review":         review,
		"coverage": object("what the model saw (X-3)", map[string]any{
			"included":       strList("files whose diff was sent in full"),
			"clipped":        strList("files whose diff was sent in part"),
			"deleted_listed": strList("deleted files whose patch was left out by design and whose names were sent in the deleted-files list; they count as reviewed (X-20)"),
			"omitted": object("files left out for the token budget, by change type", map[string]any{
				"added":    strList("added files"),
				"modified": strList("modified and renamed files"),
				"deleted":  strList("deleted files whose names were not sent either"),
			}, "added", "modified", "deleted"),
			"skipped":            skipped("files skipped for other reasons (binary, limits, fetch failures, empty or unparseable diffs, too_large, model_call_failed, unchanged_since_review)"),
			"filtered":           skipped("files excluded by the ignore rules, with the matching rule"),
			"partial":            boolean("true when at least one reviewable changed file was not fully reviewed: omitted, clipped, skipped by the provider for size or limit, or unreadable (X-18); files filtered on purpose do not count"),
			"reviewed_files":     integer("changed files whose diff was sent in full, plus deleted files listed by name"),
			"total_files":        integer("changed files except those filtered on purpose (and binary or empty-diff files): reviewed_files + not_reviewed_files"),
			"not_reviewed_files": integer("changed files the model did not fully see: omitted, clipped, skipped for size or limit, unreadable, too large for a part of their own, or in a part whose model call failed"),
			"unchanged_files":    integer("files of an incremental review skipped because their content did not change since the last review (skipped with reason unchanged_since_review, X-36); they are outside reviewed_files, not_reviewed_files and total_files and never make the review partial; absent when 0"),
			"model_calls":        integer("parts sent to the model (X-19): 1 for a review in one call, N for a review in N parts, 0 without a model call; re-asks are not counted"),
			"failed_parts":       integer("parts whose model call failed; their files are skipped with reason model_call_failed and are not reviewed"),
			"repo_context": object("repository context in the prompt (RC-9)", map[string]any{
				"status":     map[string]any{"type": "string", "enum": []any{"used", "skipped", "off"}, "description": "off: context.repo.enabled is false; used: the repository was searched; skipped: it is on but not in the prompt"},
				"reason":     str("fixed reason when skipped (auth, not_found, timeout, too_large, sha_mismatch, redirect, git_failed, git_unavailable, busy, cache_unusable, unsupported, budget, nothing_to_review, client_findings); empty otherwise"),
				"symbols":    integer("symbols searched (summed over the parts of a review in parts)"),
				"references": integer("uses of those symbols shown to the model"),
				"files":      integer("distinct files among the shown uses (summed over the parts of a review in parts)"),
			}, "status", "reason", "symbols", "references", "files"),
			"related":    RelatedCoverageSchema(),
			"guidelines": llmrun.GuidelinesSchema("the project guidelines read at the target branch (X-32); present except in a publish of supplied findings, which sends no prompt"),
			"file_tree":  llmrun.FileTreeSchema("the listing of the directories the change touches, at the target branch (X-32); present except in a publish of supplied findings, which sends no prompt"),
		}, "included", "clipped", "deleted_listed", "omitted", "skipped", "filtered", "partial", "reviewed_files",
			"total_files", "not_reviewed_files", "model_calls", "failed_parts", "repo_context", "related"),
		"notes": strList("notes about truncation, dropped findings, clipped or trimmed files and the re-ask"),
		"metadata": object("run metadata", map[string]any{
			"model":              str("llm.model"),
			"context_window":     integer("llm.context_window"),
			"prompt_tokens":      integer("estimated tokens of the prompt scaffolding (with the part line for a review in parts)"),
			"diff_tokens":        integer("estimated tokens of the diff sent, summed over the parts"),
			"request_tokens":     integer("estimated tokens of the request, the largest part's for a review in parts; 0 without a model call"),
			"fast_path":          boolean("whether the whole extended diff fit one call"),
			"llm_calls":          integer("number of chat completions: 0 to 2 per part (a part is re-asked at most once); 0 for a publish of supplied findings"),
			"repair_tactic":      str("how the answer was parsed: direct or a YAML repair tactic; empty without a model call"),
			"reasked":            boolean("whether the model was asked a second time after an unparseable answer"),
			"truncated":          boolean("whether the answer was cut off by the output limit"),
			"diff_trimmed":       boolean("whether the request-size guard shortened the diff"),
			"reviewed_at":        str("run time, RFC 3339 in UTC"),
			"already_discussed":  integer("comment threads of the existing pull request discussion that were shown to the model (X-13); absent when 0"),
			"rate_limit_waits":   integer("model requests that waited for the LLM endpoint's rate limit (llm.requests_per_minute, llm.max_concurrent_requests or an HTTP 429 cool-down)"),
			"rate_limit_wait_ms": integer("total time those requests waited, in milliseconds"),
			"models_used":        map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "the distinct models that served model calls, in order of first use (the primary and llm.fallback models)"},
			"fallback_calls":     integer("model calls a fallback model (llm.fallback) served because the model before it could not"),
		}, "model", "context_window", "prompt_tokens", "diff_tokens", "request_tokens", "fast_path", "llm_calls",
			"repair_tactic", "reasked", "truncated", "diff_trimmed", "reviewed_at", "rate_limit_waits", "rate_limit_wait_ms",
			"models_used", "fallback_calls"),
		"ci":          CISchema(),
		"repo_config": repocfg.Schema(),
		"issue":       IssueSchema(),
		"focus": str("the focus of a focused run (X-33): security, architecture, performance, ux, tests, docs or the " +
			"cleaned free text; absent without one"),
		"incremental": object("whether the whole pull request or only the files that changed since the last review were reviewed (X-36); always set by pr_review", map[string]any{
			"mode": map[string]any{"type": "string", "enum": []any{IncrementalFull, IncrementalChanged, IncrementalUnchanged},
				"description": "full: the whole pull request was reviewed; incremental: only the files that changed since the last review; unchanged: nothing changed since the last review, so nothing was reviewed, the model was not called and nothing was published"},
			"since":           str("the head commit the last review recorded, which this run continued from; absent for a full review"),
			"strategy":        str("how the changed files were found: git:diff (repository context cache) or api:contents (the provider's API); empty when no comparison was used"),
			"changed_files":   integer("listed files of the diff treated as changed and reviewed (all of them in a full review)"),
			"unchanged_files": integer("listed files skipped as unchanged since the last review"),
			"carried_findings": integer("findings of earlier reviews on unchanged files carried forward into this review without a model call " +
				"(review.key_issues_to_review items with carried_from); 0 outside mode incremental"),
		}, "mode", "strategy", "changed_files", "unchanged_files", "carried_findings"),
		"publish": object("publishing outcome; present when publish was requested, except when nothing changed since the last review (incremental.mode unchanged), which publishes nothing", map[string]any{
			"published":  boolean("whether the overview was posted or updated"),
			"comment_id": str("id of the overview comment"),
			"url":        str("URL of the overview comment"),
			"error":      str("why publishing failed; the review is still returned"),
			"updated":    boolean("true when the overview of an earlier run was edited in place instead of posting a new one"),
			"inline": object("inline comments; present when inline findings were on and the overview was posted or found", map[string]any{
				"posted":            integer("findings posted as inline comments"),
				"skipped_duplicate": integer("findings already posted on the pull request, not repeated"),
				"unanchorable":      integer("findings not on a line of the diff, listed in the overview only"),
				"failed":            integer("findings whose inline comment could not be posted, listed in the overview only"),
				"failure_class": str("provider error class and HTTP status of the first inline comment that could not be posted, " +
					"for example \"rate_limited 403\"; a fixed string, never a provider message; omitted when none failed"),
			}, "posted", "skipped_duplicate", "unanchorable", "failed"),
		}, "published"),
	}, "pr", "enabled_fields", "review", "coverage", "notes", "metadata", "repo_config")
}

// CISchema is the JSON schema of CI (X-31), for the pr_review output schema.
func CISchema() map[string]any {
	str := func(desc string) map[string]any { return map[string]any{"type": "string", "description": desc} }
	integer := func(desc string) map[string]any { return map[string]any{"type": "integer", "description": desc} }
	object := func(desc string, props map[string]any, required ...string) map[string]any {
		req := []any{}
		for _, r := range required {
			req = append(req, r)
		}
		return map[string]any{"type": "object", "description": desc, "properties": props,
			"required": req, "additionalProperties": false}
	}
	return object("CI results the repository reported for the head commit (X-31); present when review.ci_status is on", map[string]any{
		"state": map[string]any{"type": "string", "enum": []any{CIPassing, CIFailing, CIPending, CINone, CIUnreadable},
			"description": "passing: all success, neutral or skipped; failing: any failure or error; pending: any pending and none failing; none: no checks; unreadable: the checks could not be read"},
		"counts": object("checks counted by state", map[string]any{
			"success": integer("checks that passed"),
			"failure": integer("checks that failed"),
			"pending": integer("checks that are queued or running"),
			"neutral": integer("checks with a neutral result"),
			"skipped": integer("checks that were skipped"),
			"error":   integer("checks that errored or were cancelled"),
		}, "success", "failure", "pending", "neutral", "skipped", "error"),
		"checks": map[string]any{"type": "array", "description": "failing and pending checks first, then the rest, at most 20",
			"items": object("one check", map[string]any{
				"name":  str("check name; untrusted third-party text"),
				"state": str("success, failure, pending, neutral, skipped or error"),
				"url":   str("http(s) link to the check's details; empty when none"),
			}, "name", "state", "url")},
		"note": str("fixed text; set when state is unreadable or the host reported more checks than were read"),
	}, "state", "counts", "checks")
}

// IssueSchema is the JSON schema of IssueResult (X-42), for the pr_review
// output schema.
func IssueSchema() map[string]any {
	str := func(desc string) map[string]any { return map[string]any{"type": "string", "description": desc} }
	enum := func(desc string, values ...string) map[string]any {
		e := make([]any, len(values))
		for i, v := range values {
			e[i] = v
		}
		return map[string]any{"type": "string", "enum": e, "description": desc}
	}
	strList := func(desc string) map[string]any {
		return map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": desc}
	}
	object := func(desc string, props map[string]any, required ...string) map[string]any {
		req := []any{}
		for _, r := range required {
			req = append(req, r)
		}
		return map[string]any{"type": "object", "description": desc, "properties": props,
			"required": req, "additionalProperties": false}
	}
	ref := issuectx.RefSchema()
	verdict := object("the verdict on one issue", map[string]any{
		"ref":                str("the issue as the model named it"),
		"met":                strList("requirements the pull request meets (at most 10, each at most 300 bytes)"),
		"not_met":            strList("requirements the pull request does not meet"),
		"needs_verification": strList("requirements a human has to verify"),
	}, "ref", "met", "not_met", "needs_verification")
	compliance := object("the model's verdict on the issue; null when no issue was checked", map[string]any{
		"issues":  map[string]any{"type": "array", "items": verdict, "description": "one entry per issue"},
		"verdict": enum("the overall verdict", Verdicts...),
	}, "issues", "verdict")
	compliance["type"] = []any{"object", "null"}
	return object("issue compliance (X-42): the issue the pull request should implement and the verdict; always set by pr_review", map[string]any{
		"source":        enum("provider: issues the pull request names on its own host; argument: the issue_context argument; none: no issue was used", "provider", "argument", "none"),
		"refs":          map[string]any{"type": "array", "items": ref, "description": "the issue references of the run, with their outcomes"},
		"detected_keys": strList("tracker keys such as PROJ-123 that the pull request names when no issue_context was given; review-mcp never reads them: pass the issue as issue_context"),
		"compliance":    compliance,
	}, "source", "refs", "detected_keys", "compliance")
}

// RelatedCoverageSchema is the JSON schema of coverage.related (X-41, v2.3
// spec V-13a §1.5), for the output schemas of the tools with the related
// argument.
func RelatedCoverageSchema() map[string]any {
	str := func(desc string) map[string]any { return map[string]any{"type": "string", "description": desc} }
	integer := func(desc string) map[string]any { return map[string]any{"type": "integer", "description": desc} }
	item := map[string]any{"type": "object", "description": "one item of the related argument", "properties": map[string]any{
		"url": str("the URL as normalised (query and fragment dropped)"),
		"status": map[string]any{"type": "string", "enum": []any{"full", "partial", "not_placed", "failed", "ignored"},
			"description": "full: every changed file shown; partial: some files listed by path only; not_placed: read but not in the prompt; failed: not read; ignored: the reviewed pull request itself or a duplicate"},
		"reason": map[string]any{"type": "string", "enum": relatedReasons(),
			"description": "for failed: url_not_configured, url_malformed, credentials_missing, not_found, forbidden, " +
				"unauthorized or unreadable (never meaning it does not exist); for ignored: duplicate or primary; for " +
				"not_placed: did_not_fit, review_in_parts, off (related.max_tokens is 0) or nothing_to_review (the primary " +
				"pull request has nothing to review); absent otherwise"},
		"head_sha":       str("the related pull request's head revision when it was read"),
		"files_included": integer("files shown with hunks"),
		"files_listed":   integer("files listed by path only"),
	}, "required": []any{"url", "status", "files_included", "files_listed"}, "additionalProperties": false}
	return map[string]any{"type": "array", "items": item,
		"description": "related pull requests (X-41): one entry per item of the related argument, in order; [] without it. They are context only: nothing is published to them"}
}

// relatedReasons are the values of coverage.related[].reason.
func relatedReasons() []any {
	var out []any
	for _, r := range []string{related.ReasonURLNotConfigured, related.ReasonURLMalformed, related.ReasonCredentialsMissing,
		related.ReasonNotFound, related.ReasonForbidden, related.ReasonUnauthorized, related.ReasonUnreadable,
		related.ReasonDuplicate, related.ReasonPrimary, related.ReasonDidNotFit, related.ReasonReviewInParts,
		related.ReasonOff, ReasonNothingToReview} {
		out = append(out, r)
	}
	return out
}
