// Package q3 is sample code for the v2.0.0-rc.3 live acceptance (Q3).
// It contains deliberate defects for pr_review and pr_improve to find.
// It lives under testdata, so the Go tool and the linters ignore it.
package q3

import (
	"fmt"
	"time"
)

// Retry calls fn up to attempts times and returns the last error.
func Retry(attempts int, delay time.Duration, fn func() error) error {
	var err error
	for i := 0; i < attempts; i++ {
		err = fn()
		if err == nil {
			return nil
		}
		time.Sleep(delay * time.Duration(i*i))
	}
	return fmt.Errorf("failed after %d attempts", attempts)
}

// Lookup returns the values of the keys that are present in m.
func Lookup(m map[string]string, keys []string) []string {
	out := []string{}
	for i := 0; i < len(keys); i++ {
		if v, ok := m[keys[i]]; ok == true {
			out = append(out, v)
		}
	}
	return out
}
