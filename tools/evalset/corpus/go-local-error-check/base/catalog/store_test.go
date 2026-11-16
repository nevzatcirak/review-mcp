package catalog

import (
	"errors"
	"testing"
)

func TestSetAndGet(t *testing.T) {
	s := NewStore()
	s.Set([]Price{{SKU: "mug-1", Cents: 1250, Currency: "EUR"}})

	p, err := s.Get("mug-1")
	if err != nil || p.String() != "mug-1 12.50 EUR" {
		t.Fatalf("Get = %v, %v", p, err)
	}
	if _, err := s.Get("bowl-2"); !errors.Is(err, ErrUnknownSKU) {
		t.Fatalf("err = %v, want ErrUnknownSKU", err)
	}
}
