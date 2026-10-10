package provider

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

func TestValidateEditMatchesValidateReply(t *testing.T) {
	for _, c := range []struct{ id, body string }{{"1", "x"}, {"1", " "}, {"0", "x"}, {"a", ""}} {
		a, b := ValidateEdit(c.id, c.body), ValidateReply(c.id, c.body)
		if fmt.Sprint(a) != fmt.Sprint(b) {
			t.Errorf("(%q,%q): edit %v, reply %v", c.id, c.body, a, b)
		}
	}
}

func TestValidateInlineComments(t *testing.T) {
	ok := InlineComment{Path: "dir/a b.go", Line: 3, LineType: LineAdded, Body: "x"}
	for name, c := range map[string]struct {
		mod  func(*InlineComment)
		hint string
	}{
		"valid added":     {func(*InlineComment) {}, ""},
		"valid context":   {func(c *InlineComment) { c.LineType = LineContext }, ""},
		"valid rename":    {func(c *InlineComment) { c.OldPath = "old/a.go" }, ""},
		"valid range":     {func(c *InlineComment) { c.EndLine = 5 }, ""},
		"range of one":    {func(c *InlineComment) { c.EndLine = 3 }, ""},
		"end before line": {func(c *InlineComment) { c.EndLine = 2 }, "invalid inline comment line range"},
		"negative end":    {func(c *InlineComment) { c.EndLine = -1 }, "invalid inline comment line range"},
		"empty path":      {func(c *InlineComment) { c.Path = "" }, "invalid inline comment path"},
		"blank path":      {func(c *InlineComment) { c.Path = "  " }, "invalid inline comment path"},
		"control in path": {func(c *InlineComment) { c.Path = "a\tb" }, "invalid inline comment path"},
		"DEL in path":     {func(c *InlineComment) { c.Path = "a\x7f" }, "invalid inline comment path"},
		"bad old path":    {func(c *InlineComment) { c.OldPath = "\r" }, "invalid inline comment path"},
		"zero line":       {func(c *InlineComment) { c.Line = 0 }, "invalid inline comment line"},
		"negative line":   {func(c *InlineComment) { c.Line = -4 }, "invalid inline comment line"},
		"empty line type": {func(c *InlineComment) { c.LineType = "" }, "invalid inline comment line type"},
		"upper line type": {func(c *InlineComment) { c.LineType = "ADDED" }, "invalid inline comment line type"},
		"removed line":    {func(c *InlineComment) { c.LineType = "removed" }, "invalid inline comment line type"},
		"empty body":      {func(c *InlineComment) { c.Body = "" }, "empty body"},
		"blank body":      {func(c *InlineComment) { c.Body = " \n\t" }, "empty body"},
	} {
		it := ok
		c.mod(&it)
		err := ValidateInlineComments([]InlineComment{ok, it})
		if c.hint == "" {
			if err != nil {
				t.Errorf("%s: unexpected error %v", name, err)
			}
			continue
		}
		var pe *Error
		if !errors.As(err, &pe) || pe.Class != ClassProtocol || pe.Hint != c.hint || pe.Status != 0 {
			t.Errorf("%s: err = %v, want protocol/%q", name, err, c.hint)
		}
	}
	if err := ValidateInlineComments(nil); err != nil {
		t.Errorf("nil items: %v", err)
	}
}

func TestIsUser(t *testing.T) {
	me := User{ID: "42", Name: "Bot"}
	for _, c := range []struct {
		u        User
		id, name string
		want     bool
	}{
		{me, "42", "bot", true},
		{me, "42", "someone-else", true}, // ids decide (a renamed user)
		{me, "7", "Bot", false},          // same name, other account
		{me, "", "bot", true},            // no author id: case-insensitive name
		{User{Name: "Bot"}, "7", "BOT", true},
		{me, "", "alice", false},
		{me, "", "", false},
		{User{}, "", "", false},
		{User{ID: "42"}, "", "", false},
		{User{Name: ""}, "", "", false},
	} {
		if got := IsUser(c.u, c.id, c.name); got != c.want {
			t.Errorf("IsUser(%+v, %q, %q) = %v, want %v", c.u, c.id, c.name, got, c.want)
		}
	}
}

func TestStopsBatchAndItemError(t *testing.T) {
	for _, c := range []struct {
		err  error
		want bool
	}{
		{&Error{Class: ClassAuth, Status: 403}, true},
		{&Error{Class: ClassRateLimited, Status: 429}, true},
		{&Error{Class: ClassTransport, Hint: "canceled"}, true},
		{fmt.Errorf("w: %w", context.Canceled), true},
		{&Error{Class: ClassTransport, Hint: "timeout"}, false},
		{&Error{Class: ClassProtocol, Status: 422}, false},
		{&Error{Class: ClassUpstream, Status: 500}, false},
		{&Error{Class: ClassNotFound, Status: 404}, false},
	} {
		if got := StopsBatch(c.err); got != c.want {
			t.Errorf("StopsBatch(%v) = %v", c.err, got)
		}
	}
	if got := ItemError(&Error{Class: ClassProtocol, Status: 400}); got != "the server sent an unexpected response (HTTP 400)" {
		t.Errorf("ItemError = %q", got)
	}
	if got := ItemError(errors.New("raw BODY-MARKER text")); got != "the comment could not be posted" {
		t.Errorf("ItemError(raw) = %q", got)
	}
}
