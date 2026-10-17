package review

import (
	"strings"

	"github.com/nevzatcirak/review-mcp/internal/issuectx"
)

// Issue compliance (X-42, v2.2 spec V-2 §2): when the prompt carries the
// issue the pull request should implement (issuectx), the model is asked
// for one more optional field of the review, issue_compliance, which says
// for each issue which requirements are met, not met, or need a human to
// verify, and gives one verdict. It is validated here and reported in the
// result's issue object, not in review: it is not one of the X-4 fields,
// and a run without an issue neither asks for it nor shows it. The idea
// follows PR-Agent's ticket compliance check (MIT, see NOTICE); no code or
// prompt text is copied.

// KeyIssueCompliance is the answer field of issue compliance.
const KeyIssueCompliance = "issue_compliance"

// Verdicts of issue compliance.
const (
	VerdictCompliant        = "compliant"
	VerdictPartlyCompliant  = "partly_compliant"
	VerdictNotCompliant     = "not_compliant"
	VerdictUnclear          = "unclear"
	maxComplianceItemBytes  = 300
	maxComplianceListItems  = 10
	maxComplianceIssueItems = 3
)

// Verdicts are the verdict values, in the order the prompt lists them.
var Verdicts = []string{VerdictCompliant, VerdictPartlyCompliant, VerdictNotCompliant, VerdictUnclear}

// IssueComplianceSentence is the system prompt's fixed paragraph of a run
// with an issue (spec §2.2 item 2).
const IssueComplianceSentence = "The user prompt carries the issue this pull request should implement. Judge each " +
	"requirement of the issue as met, not met, or needing human verification, and report it in issue_compliance. " +
	"Quote no long passages from the issue. When the issue is vague, say so instead of inventing requirements."

// NoteComplianceDropped: the model's issue_compliance was missing or of an
// unusable shape, so the run has no compliance verdict.
//
// DESIGN-QUESTION (WP-10b DQ-2): the spec says that invalid shapes are
// dropped "with a note" without giving its text. This sentence is used, and
// also for an answer without the field. Recommendation: keep.
const NoteComplianceDropped = "The model's issue compliance answer was missing or could not be read; issue compliance was not reported."

// IssueResult is the issue object of a pr_review result (spec §2.3 item 1).
type IssueResult struct {
	// Source is provider, argument or none.
	Source string `json:"source"`
	// Refs are the issue references of the run, with their outcomes.
	Refs []issuectx.Ref `json:"refs"`
	// DetectedKeys are the tracker keys the pull request names when no
	// issue_context was given.
	DetectedKeys []string `json:"detected_keys"`
	// Compliance is the model's validated verdict; nil when no issue was
	// checked.
	Compliance *Compliance `json:"compliance"`
}

// Compliance is the validated issue_compliance answer.
type Compliance struct {
	Issues  []IssueVerdict `json:"issues"`
	Verdict string         `json:"verdict"`
}

// IssueVerdict is the verdict on one issue: its reference as the model gave
// it and the requirements, each at most 300 bytes, at most 10 per list.
type IssueVerdict struct {
	Ref               string   `json:"ref"`
	Met               []string `json:"met"`
	NotMet            []string `json:"not_met"`
	NeedsVerification []string `json:"needs_verification"`
}

// issueResultOf is the issue object of a run with the issue context c.
func issueResultOf(c *issuectx.Context) *IssueResult {
	if c == nil {
		c = issuectx.None()
	}
	r := &IssueResult{Source: c.Source, Refs: c.Refs, DetectedKeys: c.DetectedKeys}
	if r.Refs == nil {
		r.Refs = []issuectx.Ref{}
	}
	if r.DetectedKeys == nil {
		r.DetectedKeys = []string{}
	}
	return r
}

// complianceClasses are the Pydantic-style classes of issue_compliance for
// the system prompt's schema, complianceField its line in the Review class
// and complianceExample its part of the example answer.
const complianceClasses = `class IssueRequirements(BaseModel):
    ref: str = Field(description="The issue's key or number, as the issue block names it, written as a block scalar ('#' starts a YAML comment)")
    met: List[str] = Field(description="Requirements of the issue that the PR meets, each in a short sentence")
    not_met: List[str] = Field(description="Requirements of the issue that the PR does not meet, each in a short sentence")
    needs_verification: List[str] = Field(description="Requirements that cannot be judged from the PR code diff and need a human to verify, each in a short sentence")

class IssueCompliance(BaseModel):
    issues: List[IssueRequirements] = Field(description="One entry per issue in the issue block")
    verdict: Literal["compliant", "partly_compliant", "not_compliant", "unclear"] = Field(description="compliant: every requirement is met; partly_compliant: some are; not_compliant: none is; unclear: the issue is too vague to judge")

`

const complianceField = "\n    " + KeyIssueCompliance + ": IssueCompliance = Field(description=\"How well the PR " +
	"implements the issue in the issue block, requirement by requirement\")"

const complianceExample = "\n  " + KeyIssueCompliance + ":\n    issues:\n    - ref: |\n        #12\n      met:\n" +
	"      - |\n        ...\n      not_met: []\n      needs_verification: []\n    verdict: partly_compliant"

// convertCompliance validates an issue_compliance answer: a mapping with a
// known verdict and an issues list of mappings, each with a ref; the lists
// hold text items. Text is trimmed and cut to 300 bytes, lists to 10 items
// and issues to 3; an item without a ref, and empty texts, are dropped. ok
// is false for any other shape.
func convertCompliance(v any) (*Compliance, bool) {
	m, ok := v.(map[string]any)
	if !ok {
		return nil, false
	}
	verdict, _ := toText(m["verdict"])
	verdict = strings.ToLower(verdict)
	known := false
	for _, k := range Verdicts {
		known = known || k == verdict
	}
	if !known {
		return nil, false
	}
	items, ok := m["issues"].([]any)
	if !ok && m["issues"] != nil {
		return nil, false
	}
	c := &Compliance{Verdict: verdict, Issues: []IssueVerdict{}}
	for _, it := range items {
		im, ok := it.(map[string]any)
		if !ok {
			return nil, false
		}
		ref, ok := toText(im["ref"])
		if !ok {
			return nil, false
		}
		ref = cutBytes(strings.Join(strings.Fields(ref), " "), maxComplianceItemBytes)
		if ref == "" {
			continue
		}
		iv := IssueVerdict{Ref: ref}
		for _, l := range []struct {
			key string
			dst *[]string
		}{{"met", &iv.Met}, {"not_met", &iv.NotMet}, {"needs_verification", &iv.NeedsVerification}} {
			list, ok := complianceList(im[l.key])
			if !ok {
				return nil, false
			}
			*l.dst = list
		}
		if len(c.Issues) < maxComplianceIssueItems {
			c.Issues = append(c.Issues, iv)
		}
	}
	return c, true
}

// complianceList reads one requirement list: absent or null is empty; a
// list of text items is trimmed, cut and capped.
func complianceList(v any) ([]string, bool) {
	out := []string{}
	if v == nil {
		return out, true
	}
	items, ok := v.([]any)
	if !ok {
		return nil, false
	}
	for _, it := range items {
		s, ok := toText(it)
		if !ok {
			return nil, false
		}
		if s = cutBytes(s, maxComplianceItemBytes); s != "" && len(out) < maxComplianceListItems {
			out = append(out, s)
		}
	}
	return out, true
}
