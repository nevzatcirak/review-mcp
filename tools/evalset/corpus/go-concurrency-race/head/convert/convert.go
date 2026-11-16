// Package convert converts amounts between currencies.
package convert

import (
	"context"
	"fmt"
	"math"
	"time"

	"example.com/fx/convert/internal/clock"
	"example.com/fx/rates"
)

// Converter converts with the rates of Source. It is shared by all requests.
// A rate is fetched at most once per TTL and currency pair; a zero TTL turns
// the cache off.
type Converter struct {
	Source rates.Source
	TTL    time.Duration
	Clock  clock.Clock

	cache map[string]cachedRate
}

type cachedRate struct {
	rate    float64
	fetched time.Time
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
	key := from + "/" + to
	now := clock.Or(c.Clock).Now()
	if cached, ok := c.cache[key]; ok && c.TTL > 0 && now.Sub(cached.fetched) < c.TTL {
		return cached.rate, nil
	}
	rate, err := c.Source.Rate(ctx, from, to)
	if err != nil {
		return 0, fmt.Errorf("convert %s to %s: %w", from, to, err)
	}
	if c.TTL > 0 {
		if c.cache == nil {
			c.cache = make(map[string]cachedRate)
		}
		c.cache[key] = cachedRate{rate: rate, fetched: now}
	}
	return rate, nil
}
