// Package yamlrepair loads the YAML a model returns for a structured tool
// (spec P4 §3, DQ-7, DQ-8): it preprocesses the raw answer, parses it and,
// when the parse fails, runs an ordered, table-driven chain of repair
// tactics until one of them yields a non-empty mapping.
//
// Behaviour mirrors PR-Agent at commit
// 8e5a9295973b24af4b70cafd0b660a230811ef9e (pr_agent/algo/utils.py:
// load_yaml, try_fix_yaml, sanitize_yaml_control_chars,
// drop_sign_off_after_wrapper_fence, _looks_like_more_answer, is_value_no).
// No upstream code is copied. The goldens under testdata/cases were written
// by running upstream's load_yaml (see testdata/oracle/README.md).
//
// Deliberate deviations from upstream:
//   - the chain keeps upstream tactics 1, 2, 4, 5, 6, 7, 8, 9, 11 and 12, in
//     upstream order; tactics 3 (brace re-indent) and 10 (code-section
//     re-indent for other tools) are not ported (DQ-8);
//   - a tactic wins only with a non-empty mapping; upstream stops at the
//     first tactic whose parse is not None, even a scalar or a list (spec
//     §3: "the first non-empty map wins");
//   - tactic 1 forces only the keys the caller passes in Keys.Names (the
//     descriptor-derived list, DQ-8); upstream also always adds a built-in
//     list of other tools' keys;
//   - the parser is gopkg.in/yaml.v3 (YAML 1.2 scalars), not PyYAML (YAML
//     1.1); see Dialect below.
//
// # Dialect
//
// Values are decoded with yaml.v3's YAML 1.2 rules into map[string]any,
// []any, string, int, float64, bool and nil, with these rules on top:
//   - duplicate mapping keys: the last one wins (PyYAML's behaviour;
//     yaml.v3's own decoder rejects duplicates);
//   - YAML 1.1 words such as yes, no, on and off stay strings (PyYAML turns
//     them into booleans); IsNo recognises "No" either way;
//   - timestamps stay the strings they were written as (yaml.v3 would
//     produce time.Time);
//   - a document with an unknown tag (for example "!foo"), a non-scalar
//     mapping key or more than one document is a parse failure, as in
//     PyYAML's safe_load;
//   - mapping keys that are not strings are formatted the way JSON
//     formats them (1 becomes "1", true becomes "true", null becomes
//     "null").
//
// Nothing in this package logs. The caller logs Trace.Tactic at debug
// level; the text itself is never logged (X-8).
package yamlrepair

// Trace values that are not tactic names.
const (
	// TacticDirect: the preprocessed text parsed without any repair. The
	// result may still be empty (the text was empty, a scalar or a list).
	TacticDirect = "direct"
	// TacticNone: the direct parse failed and no tactic produced a
	// non-empty mapping. The result is empty.
	TacticNone = "none"
)

// Keys configures the key-dependent tactics. It is derived from the review
// field descriptors by the caller.
type Keys struct {
	// Names are the keys whose values tactic 1 forces into block scalars.
	// Each is matched as the substring "<name>:" anywhere in a line, as
	// upstream matches its keys_fix_yaml entries. Empty names are ignored.
	// A key whose value is a mapping (for example the root "review") should
	// not be listed: forcing it turns the whole mapping into one string.
	Names []string
	// First is the root key (upstream first_key, "review" for the review
	// tool) and Last the last enabled field (upstream last_key). Tactic 6
	// runs only when both are set.
	First string
	Last  string
}

// Trace says how a Load result was obtained. It never holds any content.
type Trace struct {
	// Tactic is TacticDirect, the name of the winning repair tactic, or
	// TacticNone.
	Tactic string
}

// Load parses a model answer into a mapping (spec P4 §3). It returns an
// empty, non-nil map when the answer is not a non-empty mapping after
// preprocessing and repairs; callers apply their own gate (for review, a
// non-empty "review" mapping).
//
// Load mirrors upstream's load_yaml. Upstream's review caller strips
// surrounding whitespace before calling it (PRReviewer._load_review_yaml);
// Load does not, so a caller that mirrors that path passes
// strings.TrimSpace(raw).
func Load(raw string, keys Keys) (map[string]any, Trace) {
	// DESIGN-QUESTION: should Load also apply the review caller's
	// prediction.strip() (all surrounding whitespace) before load_yaml's
	// newline trim? — chose no because spec §3 lists load_yaml's steps only
	// and the oracle goldens run load_yaml on the fixtures as given; the
	// review pipeline (WP-PR-4c) passes strings.TrimSpace(raw) to match
	// upstream's review path.
	return load(raw, keys, chain)
}

// load is Load with an explicit tactic chain, so tests can run the chain
// with one tactic removed or truncated.
func load(raw string, keys Keys, tactics []tactic) (map[string]any, Trace) {
	text := preprocess(raw)

	// A text that preprocessing emptied goes to the failure path instead of
	// returning an empty result without trying any repair.
	emptied := pyStrip(raw) != "" && pyStrip(text) == ""
	if !emptied {
		// DESIGN-QUESTION: should a direct parse that succeeds with a
		// scalar, a list, null or an empty mapping enter the repair chain,
		// as a tactic result of that shape does under the spec's "first
		// non-empty map wins" rule? — chose no (return an empty map with
		// Trace "direct") because that is upstream's load_yaml behaviour
		// (only a parse error enters try_fix_yaml), it keeps the goldens at
		// parity, and the caller's gate plus the DQ-9 re-ask already handle
		// such an answer; running the chain would rescue more answers but
		// also turn more prose into "valid but wrong" mappings.
		if v, err := parse(text); err == nil {
			m, _ := v.(map[string]any)
			if m == nil {
				m = map[string]any{}
			}
			return m, Trace{Tactic: TacticDirect}
		}
	}

	in := input{text: text, original: sanitizeControlChars(raw), keys: keys}
	for _, t := range tactics {
		for _, candidate := range t.apply(in) {
			v, err := parse(candidate)
			if err != nil {
				continue
			}
			if m, ok := v.(map[string]any); ok && len(m) > 0 {
				return m, Trace{Tactic: t.name}
			}
		}
	}
	return map[string]any{}, Trace{Tactic: TacticNone}
}
