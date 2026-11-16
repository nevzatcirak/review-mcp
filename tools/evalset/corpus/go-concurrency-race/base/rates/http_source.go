package rates

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
)

// HTTPSource asks the rates provider's API, for example
// https://rates.example.com/v1/latest?base=EUR&symbols=USD.
type HTTPSource struct {
	BaseURL string
	Client  *http.Client
}

func (s *HTTPSource) Rate(ctx context.Context, from, to string) (float64, error) {
	q := url.Values{"base": {from}, "symbols": {to}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.BaseURL+"/v1/latest?"+q.Encode(), nil)
	if err != nil {
		return 0, err
	}
	resp, err := s.Client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("rates: %s/%s: status %d", from, to, resp.StatusCode)
	}
	var body struct {
		Rates map[string]float64 `json:"rates"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return 0, fmt.Errorf("rates: %s/%s: %w", from, to, err)
	}
	rate, ok := body.Rates[to]
	if !ok || rate <= 0 {
		return 0, fmt.Errorf("rates: no rate for %s/%s", from, to)
	}
	return rate, nil
}
