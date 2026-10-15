package v21c

import (
	"fmt"
	"log"
	"net/http"
)

// Fetch calls url with the bearer token and returns the status code.
func Fetch(url, token string) (int, error) {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	log.Printf("fetching %s with token %s", url, token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, fmt.Errorf("fetch %s: %w", url, err)
	}
	return resp.StatusCode, nil
}
