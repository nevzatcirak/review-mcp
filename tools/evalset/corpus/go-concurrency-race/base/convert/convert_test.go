package convert

import (
	"context"
	"testing"
)

type fixed map[string]float64

func (f fixed) Rate(_ context.Context, from, to string) (float64, error) {
	return f[from+"/"+to], nil
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
