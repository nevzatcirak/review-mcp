package review

import "github.com/nevzatcirak/review-mcp/internal/provider"

// IsMarkedBody reports whether body's last line is the overview marker or a
// fingerprint marker, that is, whether the text has the shape of something
// the review wrote. It says nothing about the author: anyone can type a
// marker, so callers combine it with an author check (see ownMarked). It
// reads the body and keeps nothing.
func IsMarkedBody(body string) bool {
	if HasOverviewMarker(body) {
		return true
	}
	_, ok := ParseFingerprintMarker(body)
	return ok
}

// OwnActivity summarises what the review wrote on the PR as the token's own
// user (pr_info, X-23): whether an overview comment exists (a root of a
// general thread, as findOverview reads it) and how many inline comments
// carry a fingerprint marker. Comments by anyone else never count, whatever
// they contain. Only the counts leave this function.
func OwnActivity(threads []provider.Thread, me provider.User) (overview bool, inlineFindings int) {
	for i := range threads {
		t := &threads[i]
		switch t.Kind {
		case provider.ThreadGeneral:
			if len(t.Comments) == 0 {
				continue
			}
			c := &t.Comments[0]
			if HasOverviewMarker(c.Body) && provider.IsUser(me, c.AuthorID, c.AuthorLogin) {
				overview = true
			}
		case provider.ThreadInline:
			for j := range t.Comments {
				c := &t.Comments[j]
				if !provider.IsUser(me, c.AuthorID, c.AuthorLogin) {
					continue
				}
				if _, ok := ParseFingerprintMarker(c.Body); ok {
					inlineFindings++
				}
			}
		}
	}
	return overview, inlineFindings
}
