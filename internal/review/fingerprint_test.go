package review

import (
	"strings"
	"testing"
)

// TestFingerprintVector: the value is computed independently with Python's
// hashlib: sha256("src/app.go\npossible bug\nline 11 is wrong.")[:12].
func TestFingerprintVector(t *testing.T) {
	if got := Fingerprint("src/app.go", "Possible Bug", "Line 11 is wrong."); got != "763daeb0f0c1" {
		t.Errorf("fingerprint = %q", got)
	}
}

func TestFingerprintNormalizes(t *testing.T) {
	base := Fingerprint("src/app.go", "Possible Bug", "Line 11 is wrong.")
	for name, fp := range map[string]string{
		"case":               Fingerprint("src/app.go", "POSSIBLE bug", "LINE 11 IS WRONG."),
		"whitespace":         Fingerprint("src/app.go", "  Possible\n\tBug ", "Line  11\nis   wrong.\n"),
		"surrounding spaces": Fingerprint("src/app.go", "Possible Bug", "\n  Line 11 is wrong.  \n"),
	} {
		if fp != base {
			t.Errorf("%s: %q != %q", name, fp, base)
		}
	}
	for name, fp := range map[string]string{
		"path":    Fingerprint("src/other.go", "Possible Bug", "Line 11 is wrong."),
		"header":  Fingerprint("src/app.go", "Style", "Line 11 is wrong."),
		"content": Fingerprint("src/app.go", "Possible Bug", "Line 12 is wrong."),
	} {
		if fp == base {
			t.Errorf("%s change kept the fingerprint", name)
		}
	}
	if len(base) != 12 || strings.Trim(base, "0123456789abcdef") != "" {
		t.Errorf("not 12 lower-case hex digits: %q", base)
	}
}

// TestFingerprintContentPrefixIsRunes: only the first 200 runes count, and
// a multi-byte character is never cut (the vector is Python's
// sha256("src/a.go\nh\n" + "ü"*200)[:12]).
func TestFingerprintContentPrefixIsRunes(t *testing.T) {
	head := strings.Repeat("ü", FingerprintContentRunes)
	a := Fingerprint("src/a.go", "h", head+" tail one")
	b := Fingerprint("src/a.go", "h", head+"different tail")
	if a != b || a != "8e7372307be4" {
		t.Errorf("fingerprints %q and %q, want 8e7372307be4", a, b)
	}
	if Fingerprint("src/a.go", "h", head[:len(head)-2]+"x") == a {
		t.Errorf("a change inside the first 200 runes kept the fingerprint")
	}
}

func TestFingerprintMarker(t *testing.T) {
	m := FingerprintMarker("763daeb0f0c1")
	if m != "[//]: # (review-mcp:finding:763daeb0f0c1)" {
		t.Fatalf("marker = %q", m)
	}
	for body, want := range map[string]string{
		"**Header**\n\ntext\n\n" + m:          "763daeb0f0c1",
		"**Header**\n\ntext\n\n" + m + "\n\n": "763daeb0f0c1",
		m:                                     "763daeb0f0c1",
		m + "\n\nlater text":                  "",
		"text " + m:                           "",
		"[//]: # (review-mcp:finding:763DAEB0F0C1)": "",
		"[//]: # (review-mcp:finding:763daeb0f0c)":  "",
		"[//]: # (review-mcp:overview:v1)":          "",
		"":                                          "",
	} {
		got, ok := ParseFingerprintMarker(body)
		if got != want || ok != (want != "") {
			t.Errorf("ParseFingerprintMarker(%q) = %q, %v", body, got, ok)
		}
	}
}
