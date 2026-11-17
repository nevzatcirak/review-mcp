package repocfg

// allowList is the §1.2 allow-list of V-5, in the order of its table. The
// order is that of the applied keys in the result and the note.
//
// A key not in this list is ignored, whatever its name or its value: llm.*,
// the provider sections, context.* other than the context.impact keys
// below, serve.*, log.*, repo_config.*, the other diff.* limits, the
// *_tokens budgets and every secret-looking key.
var allowList = []string{
	"output.language",
	"ignore.glob",
	"ignore.regex",
	"diff.ignore_generated_frameworks",
	"review.guidelines_paths",
	"review.require_tests",
	"review.require_security",
	"review.require_performance",
	"review.require_effort_estimate",
	"review.inline_findings",
	"review.persistent_overview",
	"review.ci_status",
	"review.file_tree",
	"review.incremental",
	"review.max_findings",
	"review.max_total_findings",
	"improve.max_suggestions",
	"improve.min_score",
	"review.extra_instructions",
	"ask.extra_instructions",
	"review.focuses",
	"review.docs_style",
	// The focus profiles (X-51, v2.9 spec V-19 §1.2): custom focuses come
	// before the rules, which may name them.
	"review.custom_focuses",
	"review.focus_rules",
	"review.docs_style_file",
	"review.issue_compliance",
	"related.contract_globs",
	// The impact trace (X-50, v2.10 spec V-18 §2.2): enabled may only turn
	// it off, the numbers only tighten, paths and entry_points replace the
	// operator's lists and exclude_paths adds to them.
	"context.impact.enabled",
	"context.impact.depth",
	"context.impact.max_callers_per_node",
	"context.impact.max_links",
	"context.impact.paths",
	"context.impact.exclude_paths",
	"context.impact.entry_points",
	// related.repos is [[related.repos]], a list of tables (v2.5 spec V-13b
	// §3.2), kept only with the operator key related.from_repo_config.
	"related.repos",
}

var allowed = func() map[string]bool {
	m := make(map[string]bool, len(allowList))
	for _, k := range allowList {
		m[k] = true
	}
	return m
}()
