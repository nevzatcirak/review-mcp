package convert

import (
	"context"
	"testing"
	"time"

	"example.com/fx/convert/internal/clock"
)

type fixed map[string]float64

func (f fixed) Rate(_ context.Context, from, to string) (float64, error) {
	return f[from+"/"+to], nil
}

type counting struct {
	calls int
	rate  float64
}

func (c *counting) Rate(context.Context, string, string) (float64, error) {
	c.calls++
	return c.rate, nil
}

func TestConvert(t *testing.T) {
	c := &Converter{Source: fixed{"EUR/USD": 1.0875}}
	got, err := c.Convert(context.Background(), 1250, "EUR", "USD")
	if err != nil || got != 1359 {
		t.Fatalf("Convert = %d, %v; want 1359", got, err)
	}
	if got, _ := c.Convert(context.Background(), 1250, "EUR", "EUR"); got != 1250 {
		t.Fatalf("same currency = %d", got)
	}
}

func TestConvertCachesTheRate(t *testing.T) {
	src := &counting{rate: 2}
	clk := &clock.Fixed{T: time.Date(2024, 3, 1, 12, 0, 0, 0, time.UTC)}
	c := &Converter{Source: src, TTL: time.Minute, Clock: clk}
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		if _, err := c.Convert(ctx, 100, "EUR", "USD"); err != nil {
			t.Fatal(err)
		}
	}
	if src.calls != 1 {
		t.Fatalf("fetched %d times within the TTL, want once", src.calls)
	}
	clk.T = clk.T.Add(time.Minute)
	if _, err := c.Convert(ctx, 100, "EUR", "USD"); err != nil {
		t.Fatal(err)
	}
	if src.calls != 2 {
		t.Fatalf("fetched %d times after the TTL, want twice", src.calls)
	}
}
