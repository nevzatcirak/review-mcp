// Package convert converts amounts between currencies.
package convert

import (
	"context"
	"fmt"
	"math"

	"example.com/fx/rates"
)

// Converter converts with the rates of Source. It is shared by all requests.
type Converter struct {
	Source rates.Source
}

// Convert converts amount cents of from into cents of to, rounded half away
// from zero.
func (c *Converter) Convert(ctx context.Context, amount int64, from, to string) (int64, error) {
	if from == to {
		return amount, nil
	}
	rate, err := c.rate(ctx, from, to)
	if err != nil {
		return 0, err
	}
	return int64(math.Round(float64(amount) * rate)), nil
}

func (c *Converter) rate(ctx context.Context, from, to string) (float64, error) {
	rate, err := c.Source.Rate(ctx, from, to)
	if err != nil {
		return 0, fmt.Errorf("convert %s to %s: %w", from, to, err)
	}
	return rate, nil
}
