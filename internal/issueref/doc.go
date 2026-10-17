// Package issueref finds the issues a pull request names (X-42, v2.2 spec
// V-2 §1.1).
//
// Only explicit references count: closing keywords in the description, issue
// URLs on the pull request's own host, "#N" in the title and a few branch name
// patterns (Find). A number in free description text without a keyword is not
// a reference, and an issue on another host is never named. Tracker keys such
// as PROJ-123 are only detected (DetectKeys): review-mcp never reads them.
//
// The pull request's text is untrusted. It is sanitised before it is scanned
// (no control characters, at most MaxDescriptionBytes of the description,
// fenced code blocks left out), and nothing here logs or returns any of it
// other than issue numbers, repository paths and tracker keys.
package issueref
