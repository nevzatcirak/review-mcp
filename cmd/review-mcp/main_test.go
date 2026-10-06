package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestRun(t *testing.T) {
	tests := []struct {
		name       string
		args       []string
		wantCode   int
		wantOut    string // substring of stdout; "" means stdout must be empty
		wantErrSub string
	}{
		{"stdio bad flag", []string{"stdio", "-bogus"}, 2, "", "flag provided but not defined"},
		{"version", []string{"version"}, 0, "review-mcp ", ""},
		{"serve bad flag", []string{"serve", "-bogus"}, 2, "", "flag provided but not defined"},
		{"serve extra argument", []string{"serve", "extra"}, 2, "", "serve takes no arguments"},
		{"unknown", []string{"bogus"}, 2, "", "usage:"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var out, errb bytes.Buffer
			code := run(tc.args, &out, &errb)
			if code != tc.wantCode {
				t.Errorf("exit code = %d, want %d", code, tc.wantCode)
			}
			if tc.wantOut == "" && out.Len() != 0 {
				t.Errorf("stdout must be empty, got %q", out.String())
			}
			if tc.wantOut != "" && !strings.Contains(out.String(), tc.wantOut) {
				t.Errorf("stdout = %q, want substring %q", out.String(), tc.wantOut)
			}
			if !strings.Contains(errb.String(), tc.wantErrSub) {
				t.Errorf("stderr = %q, want substring %q", errb.String(), tc.wantErrSub)
			}
		})
	}
}
