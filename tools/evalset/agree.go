package main

import (
	"encoding/csv"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// agreement is DIR3/agreement.json, written by evalset agree.
type agreement struct {
	Items     int     `json:"items"`
	Labeled   int     `json:"labeled"`
	Unknown   int     `json:"unknown_labels"`
	Agree     int     `json:"agree"`
	Share     float64 `json:"share"`
	ByVerdict map[string]struct {
		Labeled int `json:"labeled"`
		Agree   int `json:"agree"`
	} `json:"by_judge_verdict"`
}

// cmdAgree compares the owner's labels in sample.csv with the judge's
// verdicts in sample_key.csv (next to it) and reports the agreement.
func cmdAgree(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("evalset agree", flag.ContinueOnError)
	fs.SetOutput(stderr)
	sample := fs.String("sample", "", "the labeled sample.csv `file` of evalset score -judge")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	fail := func(code int, format string, a ...any) int {
		_, _ = fmt.Fprintf(stderr, "evalset agree: "+format+"\n", a...)
		return code
	}
	if *sample == "" || fs.NArg() > 0 {
		return fail(2, "-sample is required")
	}
	dir := filepath.Dir(*sample)
	sheet, err := readCSV(*sample)
	if err != nil || len(sheet) == 0 {
		return fail(2, "cannot read -sample")
	}
	key, err := readCSV(filepath.Join(dir, "sample_key.csv"))
	if err != nil || len(key) == 0 {
		return fail(2, "cannot read sample_key.csv next to -sample")
	}
	col := func(header []string, name string) int {
		for i, h := range header {
			if h == name {
				return i
			}
		}
		return -1
	}
	sID, sHuman := col(sheet[0], "sample"), col(sheet[0], "human")
	kID, kJudge := col(key[0], "sample"), col(key[0], "judge")
	if sID < 0 || sHuman < 0 || kID < 0 || kJudge < 0 {
		return fail(2, "sample.csv or sample_key.csv lacks its columns")
	}
	judge := map[string]string{}
	for _, r := range key[1:] {
		if len(r) > max(kID, kJudge) {
			judge[r[kID]] = r[kJudge]
		}
	}
	a := agreement{ByVerdict: map[string]struct {
		Labeled int `json:"labeled"`
		Agree   int `json:"agree"`
	}{}}
	for _, r := range sheet[1:] {
		if len(r) <= max(sID, sHuman) {
			continue
		}
		a.Items++
		human := strings.TrimSpace(r[sHuman])
		if human == "" {
			continue
		}
		lower := strings.ToLower(human)
		switch {
		case lower == verdictValid || lower == verdictInvalid:
			human = lower
		case strings.HasPrefix(lower, verdictMatches):
			human = verdictMatches + strings.TrimSpace(human[len(verdictMatches):])
		default:
			a.Unknown++
			continue
		}
		j, ok := judge[r[sID]]
		if !ok {
			return fail(2, "sample %s is not in sample_key.csv", r[sID])
		}
		a.Labeled++
		bv := a.ByVerdict[verdictClass(j)]
		bv.Labeled++
		if human == j {
			a.Agree++
			bv.Agree++
		}
		a.ByVerdict[verdictClass(j)] = bv
	}
	if a.Labeled > 0 {
		a.Share = round4(float64(a.Agree) / float64(a.Labeled))
	}
	if err := writeJSON(filepath.Join(dir, "agreement.json"), a); err != nil {
		return fail(1, "cannot write agreement.json")
	}
	if a.Labeled == 0 {
		_, _ = fmt.Fprintf(stdout, "evalset agree: none of %d items is labeled; %s\n", a.Items, agreementNotMeasured)
		return 0
	}
	_, _ = fmt.Fprintf(stdout, "evalset agree: %d of %d items labeled (%d labels not understood); the judge agrees on %d (%.1f%%); see %s\n",
		a.Labeled, a.Items, a.Unknown, a.Agree, a.Share*100, filepath.Join(dir, "agreement.json"))
	return 0
}

func readCSV(path string) ([][]string, error) {
	f, err := os.Open(path) //nolint:gosec // G304: the operator's own file
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	r := csv.NewReader(f)
	r.FieldsPerRecord = -1
	return r.ReadAll()
}
