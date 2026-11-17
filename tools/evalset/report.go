package main

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"math"
	"math/rand/v2"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// tally counts one variant in one repeat over every case.
type tally struct {
	runs, failed                 int
	findings, ruleMatched        int
	judgeMatched, validUnlisted  int
	issues, issuesHit, judgedHit int
	sevTotal, sevHit             int
	cascade, cascadeHit          int
	hops                         [3][2]int // [hops][total, hit]
	bodyOnly, bodyOnlyHit        int
	large, largeHit              int
	local, localHit              int
	low, lowHit                  int
	clean, cleanFindings         int
	cleanZero                    int
	wallMS                       int64
	llmCalls                     int
}

func (t *tally) add(rs *runScore) {
	t.runs++
	if !rs.rec.OK {
		// A failed run is "no answer": counted here, never as zero
		// findings, and outside every ratio below.
		t.failed++
		return
	}
	t.wallMS += rs.rec.WallMS
	t.llmCalls += rs.rec.LLMCalls
	c := rs.c
	t.findings += len(rs.findings)
	for _, s := range rs.findings {
		switch {
		case s.rule == ruleLocation || s.rule == ruleMention:
			t.ruleMatched++
		case s.rule == ruleJudge:
			t.judgeMatched++
		case s.verdict == verdictValid:
			t.validUnlisted++
		}
	}
	if c.Category == "clean" {
		t.clean++
		t.cleanFindings += len(rs.findings)
		if len(rs.findings) == 0 {
			t.cleanZero++
		}
	}
	for i, is := range c.Issues {
		hit := rs.matched[i] == ruleLocation || rs.matched[i] == ruleMention
		b := func(v bool) int {
			if v {
				return 1
			}
			return 0
		}
		t.issues++
		t.issuesHit += b(hit)
		t.judgedHit += b(rs.matched[i] != "")
		w := sevWeight(is.Severity)
		t.sevTotal += w
		t.sevHit += w * b(hit)
		if is.Kind == "cascade" {
			t.cascade++
			t.cascadeHit += b(hit)
			if is.Hops == 1 || is.Hops == 2 {
				t.hops[is.Hops][0]++
				t.hops[is.Hops][1] += b(hit)
			}
		} else {
			t.local++
			t.localHit += b(hit)
		}
		if c.HasTag("body-only") {
			t.bodyOnly++
			t.bodyOnlyHit += b(hit)
		}
		if c.ID == largeCaseID {
			t.large++
			t.largeHit += b(hit)
		}
		if is.Severity == "low" {
			t.low++
			t.lowHit += b(hit)
		}
	}
}

func ratio(a, b int) (float64, bool) {
	if b == 0 {
		return 0, false
	}
	return float64(a) / float64(b), true
}

// metricDef is one metric of §2.3 and the WP-21b additions.
type metricDef struct {
	key, label string
	judgeOnly  bool
	// count marks a count (failed runs), shown without a percent sign.
	count, seconds, plain bool
	f                     func(t *tally) (float64, bool)
}

var metricDefs = []metricDef{
	{key: "precision", label: "Precision", f: func(t *tally) (float64, bool) { return ratio(t.ruleMatched, t.findings) }},
	{key: "augmented_precision", label: "Augmented precision (judge)", judgeOnly: true, f: func(t *tally) (float64, bool) {
		return ratio(t.ruleMatched+t.judgeMatched+t.validUnlisted, t.findings)
	}},
	{key: "recall", label: "Recall", f: func(t *tally) (float64, bool) { return ratio(t.issuesHit, t.issues) }},
	{key: "judged_recall", label: "Recall with judge matches", judgeOnly: true, f: func(t *tally) (float64, bool) { return ratio(t.judgedHit, t.issues) }},
	{key: "f1", label: "F1", f: func(t *tally) (float64, bool) {
		p, ok1 := ratio(t.ruleMatched, t.findings)
		r, ok2 := ratio(t.issuesHit, t.issues)
		if !ok1 || !ok2 || p+r == 0 {
			return 0, ok1 && ok2
		}
		return 2 * p * r / (p + r), true
	}},
	{key: "severity_weighted_recall", label: "Severity-weighted recall", f: func(t *tally) (float64, bool) { return ratio(t.sevHit, t.sevTotal) }},
	{key: "cascade_recall", label: "Cascade recall", f: func(t *tally) (float64, bool) { return ratio(t.cascadeHit, t.cascade) }},
	{key: "recall_hops_1", label: "Recall, hops 1", f: func(t *tally) (float64, bool) { return ratio(t.hops[1][1], t.hops[1][0]) }},
	{key: "recall_hops_2", label: "Recall, hops 2", f: func(t *tally) (float64, bool) { return ratio(t.hops[2][1], t.hops[2][0]) }},
	{key: "recall_local", label: "Recall, local issues", f: func(t *tally) (float64, bool) { return ratio(t.localHit, t.local) }},
	{key: "recall_body_only", label: "Recall, tag body-only", f: func(t *tally) (float64, bool) { return ratio(t.bodyOnlyHit, t.bodyOnly) }},
	{key: "recall_2000_token_case", label: "2,000-token case (" + largeCaseID + ")", f: func(t *tally) (float64, bool) { return ratio(t.largeHit, t.large) }},
	{key: "recall_low", label: "Recall, low issues", f: func(t *tally) (float64, bool) { return ratio(t.lowHit, t.low) }},
	{key: "findings_per_clean_pr", label: "Findings per clean pull request", plain: true, f: func(t *tally) (float64, bool) { return ratio(t.cleanFindings, t.clean) }},
	{key: "clean_zero_share", label: "Clean pull requests with zero findings", f: func(t *tally) (float64, bool) { return ratio(t.cleanZero, t.clean) }},
	{key: "failed_runs", label: "Failed runs (no answer)", count: true, f: func(t *tally) (float64, bool) { return float64(t.failed), true }},
	{key: "wall_time_s", label: "Wall time per run (s)", seconds: true, f: func(t *tally) (float64, bool) {
		ok := t.runs - t.failed
		if ok == 0 {
			return 0, false
		}
		return float64(t.wallMS) / 1000 / float64(ok), true
	}},
	{key: "model_calls", label: "Model calls per run", plain: true, f: func(t *tally) (float64, bool) { return ratio(t.llmCalls, t.runs-t.failed) }},
}

// stat is a metric over the repeats: mean, min and max of the repeats where
// it is defined, and each repeat's value (nil where undefined).
type stat struct {
	Mean      *float64   `json:"mean"`
	Min       *float64   `json:"min"`
	Max       *float64   `json:"max"`
	PerRepeat []*float64 `json:"per_repeat"`
}

func round4(v float64) float64 { return math.Round(v*10000) / 10000 }

func statOf(vals []*float64) stat {
	s := stat{PerRepeat: vals}
	var sum float64
	n := 0
	for _, v := range vals {
		if v == nil {
			continue
		}
		x := *v
		if s.Min == nil || x < *s.Min {
			s.Min = &x
		}
		if s.Max == nil || x > *s.Max {
			s.Max = &x
		}
		sum += x
		n++
	}
	if n > 0 {
		m := round4(sum / float64(n))
		s.Mean = &m
	}
	return s
}

// caseRun is one run of a case in metrics.json.
type caseRun struct {
	Repeat   int      `json:"repeat"`
	Failed   bool     `json:"failed"`
	Findings int      `json:"findings"`
	Matched  []string `json:"matched_issues"`
	Issues   int      `json:"issues"`
	// BudgetVerified is the run's flag (runRecord.BudgetVerified).
	BudgetVerified *bool `json:"budget_verified,omitempty"`
}

// report is metrics.json.
type report struct {
	Version        int       `json:"version"`
	Mode           string    `json:"mode"`
	BinaryVersion  string    `json:"binary_version"`
	Variants       []variant `json:"variants"`
	Repeat         int       `json:"repeat"`
	ReviewerModels []string  `json:"reviewer_models"`
	Judge          struct {
		Enabled   bool           `json:"enabled"`
		Model     string         `json:"model,omitempty"`
		Verdicts  map[string]int `json:"verdicts,omitempty"`
		Agreement string         `json:"agreement"`
	} `json:"judge"`
	Corpus struct {
		Cases      int     `json:"cases"`
		CleanCases int     `json:"clean_cases"`
		Issues     int     `json:"issues"`
		LowIssues  int     `json:"low_issues"`
		LowShare   float64 `json:"low_share"`
	} `json:"corpus"`
	// BudgetVerified is set for each variant that sets a budget: false
	// when a successful run of it could not verify the budget, because the
	// binary does not report it (the WP-20c review, DQ-4).
	BudgetVerified map[string]bool                 `json:"budget_verified,omitempty"`
	Metrics        map[string]map[string]stat      `json:"metrics"`
	Cases          map[string]map[string][]caseRun `json:"cases"`

	failedRuns, findings, matchedFindings int
	order                                 []string // case ids in run order
	categories                            map[string]string
}

const agreementNotMeasured = "judge agreement not measured"

// budgetNotVerified marks a variant in report.md whose budget the binary
// does not report (the WP-20c review, DQ-4).
const budgetNotVerified = "budget not verified (the binary does not report it)"

func buildReport(rf runFile, runs []*runScore, jc *judgeConfig) *report {
	rep := &report{Version: 1, Mode: rf.Mode, BinaryVersion: rf.BinaryVersion, Variants: rf.Variants, Repeat: rf.Repeat,
		ReviewerModels: reviewerModels(rf), Metrics: map[string]map[string]stat{}, Cases: map[string]map[string][]caseRun{},
		categories: map[string]string{}}
	rep.Judge.Agreement = "judge off"
	if jc != nil {
		rep.Judge.Enabled, rep.Judge.Model, rep.Judge.Verdicts = true, jc.Model, map[string]int{}
		rep.Judge.Agreement = agreementNotMeasured
	}
	tallies := map[string][]*tally{}
	for _, v := range rf.Variants {
		tallies[v.Name] = make([]*tally, rf.Repeat)
		for i := range tallies[v.Name] {
			tallies[v.Name][i] = &tally{}
		}
	}
	seen := map[string]bool{}
	for _, rs := range runs {
		if rs.rec.Repeat < 1 || rs.rec.Repeat > rf.Repeat || tallies[rs.rec.Variant] == nil {
			continue
		}
		tallies[rs.rec.Variant][rs.rec.Repeat-1].add(rs)
		id := rs.c.ID
		if !seen[id] {
			seen[id] = true
			rep.order = append(rep.order, id)
			rep.categories[id] = rs.c.Category
			rep.Corpus.Cases++
			if rs.c.Category == "clean" {
				rep.Corpus.CleanCases++
			}
			for _, is := range rs.c.Issues {
				rep.Corpus.Issues++
				if is.Severity == "low" {
					rep.Corpus.LowIssues++
				}
			}
		}
		cr := caseRun{Repeat: rs.rec.Repeat, Failed: !rs.rec.OK, Findings: len(rs.findings), Issues: len(rs.c.Issues), Matched: []string{},
			BudgetVerified: rs.rec.BudgetVerified}
		if bv := rs.rec.BudgetVerified; bv != nil {
			if rep.BudgetVerified == nil {
				rep.BudgetVerified = map[string]bool{}
			}
			was, seen := rep.BudgetVerified[rs.rec.Variant]
			rep.BudgetVerified[rs.rec.Variant] = *bv && (was || !seen)
		}
		for i, m := range rs.matched {
			if m == ruleLocation || m == ruleMention {
				cr.Matched = append(cr.Matched, rs.c.Issues[i].ID)
			}
		}
		if rep.Cases[id] == nil {
			rep.Cases[id] = map[string][]caseRun{}
		}
		rep.Cases[id][rs.rec.Variant] = append(rep.Cases[id][rs.rec.Variant], cr)
		if !rs.rec.OK {
			rep.failedRuns++
		}
		rep.findings += len(rs.findings)
		for _, s := range rs.findings {
			if s.rule == ruleLocation || s.rule == ruleMention {
				rep.matchedFindings++
			}
			if jc != nil && s.verdict != "" {
				rep.Judge.Verdicts[verdictClass(s.verdict)]++
			}
		}
	}
	if rep.Corpus.Issues > 0 {
		rep.Corpus.LowShare = round4(float64(rep.Corpus.LowIssues) / float64(rep.Corpus.Issues))
	}
	for _, v := range rf.Variants {
		m := map[string]stat{}
		for _, d := range metricDefs {
			if d.judgeOnly && jc == nil {
				continue
			}
			vals := make([]*float64, rf.Repeat)
			for i, t := range tallies[v.Name] {
				if x, ok := d.f(t); ok {
					x = round4(x)
					vals[i] = &x
				}
			}
			m[d.key] = statOf(vals)
		}
		rep.Metrics[v.Name] = m
	}
	return rep
}

func fmtStat(d metricDef, s stat, repeats int) string {
	if s.Mean == nil {
		return "–"
	}
	f := func(x float64) string {
		switch {
		case d.count:
			return strconv.FormatFloat(x, 'f', -1, 64)
		case d.seconds:
			return strconv.FormatFloat(x, 'f', 1, 64)
		case d.plain:
			return strconv.FormatFloat(x, 'f', 2, 64)
		}
		return strconv.FormatFloat(x*100, 'f', 1, 64) + "%"
	}
	if repeats == 1 || *s.Min == *s.Max {
		return f(*s.Mean)
	}
	return fmt.Sprintf("%s (%s–%s)", f(*s.Mean), f(*s.Min), f(*s.Max))
}

// markdown renders report.md: tables of numbers and case ids, no review
// text beyond finding titles (and it shows none).
func (rep *report) markdown(judged bool) string {
	var b strings.Builder
	b.WriteString("# Evaluation report (V-9)\n\n")
	fmt.Fprintf(&b, "- Binary: `%s`\n- Mode: %s\n- Repeats: %d\n", rep.BinaryVersion, rep.Mode, rep.Repeat)
	for _, v := range rep.Variants {
		fmt.Fprintf(&b, "- Variant `%s`: flags `%s`, environment `%s`", v.Name, strings.Join(v.Flags, " "), strings.Join(v.Env, " "))
		if verified, ok := rep.BudgetVerified[v.Name]; ok && !verified {
			b.WriteString("; " + budgetNotVerified)
		}
		b.WriteString("\n")
	}
	fmt.Fprintf(&b, "- Reviewer models recorded: %s\n", strings.Join(rep.ReviewerModels, ", "))
	fmt.Fprintf(&b, "- Cases: %d (%d clean); issues: %d, of which %d low (%.1f%%), counted with weight 1\n",
		rep.Corpus.Cases, rep.Corpus.CleanCases, rep.Corpus.Issues, rep.Corpus.LowIssues, rep.Corpus.LowShare*100)
	if judged {
		fmt.Fprintf(&b, "- Judge: `%s`; %s (label sample.csv, then run `evalset agree`)\n", rep.Judge.Model, rep.Judge.Agreement)
	} else {
		b.WriteString("- Judge: off; augmented precision not measured\n")
	}
	b.WriteString("\nValues are the mean over the repeats, with min–max when they differ. Failed runs are \"no answer\": counted on their own row and left out of every other figure.\n\n")
	b.WriteString("## Metrics\n\n| Metric |")
	for _, v := range rep.Variants {
		b.WriteString(" " + v.Name + " |")
	}
	b.WriteString("\n|---|")
	for range rep.Variants {
		b.WriteString("---|")
	}
	b.WriteString("\n")
	for _, d := range metricDefs {
		if d.judgeOnly && !judged {
			continue
		}
		b.WriteString("| " + d.label + " |")
		for _, v := range rep.Variants {
			b.WriteString(" " + fmtStat(d, rep.Metrics[v.Name][d.key], rep.Repeat) + " |")
		}
		b.WriteString("\n")
	}
	if judged {
		b.WriteString("\n## Judge verdicts\n\n| Verdict | Items |\n|---|---|\n")
		for _, k := range sortedKeysOf(rep.Judge.Verdicts) {
			fmt.Fprintf(&b, "| %s | %d |\n", k, rep.Judge.Verdicts[k])
		}
		b.WriteString("\n`valid_unlisted` findings are listed in valid_unlisted.csv for the owner to add to the ground truth or reject.\n")
	}
	b.WriteString("\n## Cases\n\nIssues matched by the rules / issues, and findings, per repeat.\n\n| Case | Category |")
	for _, v := range rep.Variants {
		b.WriteString(" " + v.Name + " |")
	}
	b.WriteString("\n|---|---|")
	for range rep.Variants {
		b.WriteString("---|")
	}
	b.WriteString("\n")
	for _, id := range rep.order {
		fmt.Fprintf(&b, "| `%s` | %s |", id, rep.categories[id])
		for _, v := range rep.Variants {
			var cells []string
			for _, cr := range rep.Cases[id][v.Name] {
				if cr.Failed {
					cells = append(cells, "failed")
					continue
				}
				cell := fmt.Sprintf("%d f", cr.Findings)
				if cr.Issues > 0 {
					cell = fmt.Sprintf("%d/%d, %s", len(cr.Matched), cr.Issues, cell)
				}
				cells = append(cells, cell)
			}
			b.WriteString(" " + strings.Join(cells, "; ") + " |")
		}
		b.WriteString("\n")
	}
	return b.String()
}

func sortedKeysOf[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

func lines(f finding) string {
	if f.Start <= 0 {
		return ""
	}
	return fmt.Sprintf("%d-%d", f.Start, max(f.End, f.Start))
}

// writeOutputs writes report.md, metrics.json and matches.csv, and with the
// judge sample.csv, sample_key.csv, valid_unlisted.csv and README.txt.
func writeOutputs(out string, rep *report, runs []*runScore, judged bool, seed int64) error {
	if err := os.WriteFile(filepath.Join(out, "report.md"), []byte(rep.markdown(judged)), 0o600); err != nil {
		return err
	}
	if err := writeJSON(filepath.Join(out, "metrics.json"), rep); err != nil {
		return err
	}
	rows := [][]string{{"case", "variant", "repeat", "finding", "finding_title", "finding_file", "finding_lines", "issue", "rule", "judge"}}
	var valid [][]string
	var items []sampleItem
	for _, rs := range runs {
		r := rs.rec
		rep := strconv.Itoa(r.Repeat)
		if !r.OK {
			rows = append(rows, []string{r.Case, r.Variant, rep, "", "", "", "", "", "failed", ""})
			continue
		}
		for i, s := range rs.findings {
			issue := "fp"
			if s.issue >= 0 {
				issue = rs.c.Issues[s.issue].ID
			} else if s.verdict == verdictValid {
				issue = verdictValid
			}
			rows = append(rows, []string{r.Case, r.Variant, rep, strconv.Itoa(i + 1), oneLine(s.f.Header), s.f.File, lines(s.f), issue, s.rule, s.verdict})
			if s.verdict == verdictValid {
				valid = append(valid, []string{r.Case, r.Variant, rep, oneLine(s.f.Header), s.f.File, lines(s.f), oneLine(s.f.Content), s.reason})
			}
			if s.verdict != "" && s.verdict != verdictError && s.verdict != verdictUnparsed {
				items = append(items, sampleItem{rs: rs, s: s})
			}
		}
		for i, m := range rs.matched {
			if m == "" {
				rows = append(rows, []string{r.Case, r.Variant, rep, "", "", "", "", rs.c.Issues[i].ID, "missed", ""})
			}
		}
	}
	if err := writeCSV(filepath.Join(out, "matches.csv"), rows); err != nil {
		return err
	}
	if !judged {
		return nil
	}
	if err := writeCSV(filepath.Join(out, "valid_unlisted.csv"), append([][]string{{"case", "variant", "repeat", "finding_title", "finding_file", "finding_lines", "finding_text", "judge_reason"}}, valid...)); err != nil {
		return err
	}
	return writeSample(out, items, seed)
}

type sampleItem struct {
	rs *runScore
	s  scored
}

// sampleSize is the size of the blind sample of judged items (§2.3).
const sampleSize = 30

// writeSample writes the blind, seeded sample of judged items: sample.csv
// for the owner to label (no judge verdict, no variant), sample_key.csv
// with the judge's verdicts, and README.txt.
func writeSample(out string, items []sampleItem, seed int64) error {
	rng := rand.New(rand.NewPCG(uint64(seed), uint64(seed)^0x9e3779b97f4a7c15)) //nolint:gosec // G404,G115: a reproducible sample, not a secret
	rng.Shuffle(len(items), func(i, j int) { items[i], items[j] = items[j], items[i] })
	if len(items) > sampleSize {
		items = items[:sampleSize]
	}
	sheet := [][]string{{"sample", "case", "finding_file", "finding_lines", "finding_title", "finding_text", "case_issues", "human"}}
	key := [][]string{{"sample", "variant", "repeat", "judge"}}
	for i, it := range items {
		id := fmt.Sprintf("J%03d", i+1)
		var issues []string
		for _, is := range it.rs.c.Issues {
			issues = append(issues, fmt.Sprintf("%s (%s) %s:%d-%d: %s", is.ID, is.Severity, is.Path, is.Lines[0], is.Lines[1], is.Summary))
		}
		if len(issues) == 0 {
			issues = []string{"none: the case is clean"}
		}
		sheet = append(sheet, []string{id, it.rs.c.ID, it.s.f.File, lines(it.s.f), oneLine(it.s.f.Header), oneLine(it.s.f.Content), strings.Join(issues, " | "), ""})
		key = append(key, []string{id, it.rs.rec.Variant, strconv.Itoa(it.rs.rec.Repeat), it.s.verdict})
	}
	if err := writeCSV(filepath.Join(out, "sample.csv"), sheet); err != nil {
		return err
	}
	if err := writeCSV(filepath.Join(out, "sample_key.csv"), key); err != nil {
		return err
	}
	readme := fmt.Sprintf(`Blind sample of judged findings (evalset score -judge)

sample.csv      %d findings the judge examined, in random order. For each
                row, write in "human" one of: matches:<issue-id> (the
                finding describes that issue of the case), valid_unlisted
                (a real problem that is not listed), invalid.
sample_key.csv  the judge's verdicts. Do not open it until you have labeled.
seed            %d (evalset score -seed %d gives the same sample)

Then run: evalset agree -sample %s
`, len(items), seed, seed, filepath.Join(out, "sample.csv"))
	return os.WriteFile(filepath.Join(out, "README.txt"), []byte(readme), 0o600)
}

func writeCSV(path string, rows [][]string) error {
	var b bytes.Buffer
	w := csv.NewWriter(&b)
	if err := w.WriteAll(rows); err != nil {
		return err
	}
	return os.WriteFile(path, b.Bytes(), 0o600)
}
