// Package rates fetches exchange rates.
package rates

import "context"

// Source returns how many units of to one unit of from buys.
type Source interface {
	Rate(ctx context.Context, from, to string) (float64, error)
}
