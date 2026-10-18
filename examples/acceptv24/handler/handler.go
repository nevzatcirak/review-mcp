// Package handler answers profile requests.
package handler

import (
	"fmt"

	"github.com/nevzatcirak/review-mcp/examples/acceptv24/store"
)

// Greeting returns the greeting shown on a user's profile page.
func Greeting(s *store.Store, id string) (string, error) {
	u, err := s.FindUser(id)
	if err != nil {
		return "", fmt.Errorf("find user %q: %w", id, err)
	}
	return "Hello, " + u.Name + "!", nil
}
