// Package textutil holds small helpers for plain text.
package textutil

import "strings"

// CountWords returns the number of words in s. Words are separated by any
// Unicode white space; empty input and input that is only white space give 0.
func CountWords(s string) int {
	return len(strings.Fields(s))
}
