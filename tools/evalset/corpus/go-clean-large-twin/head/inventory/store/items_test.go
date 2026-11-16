package store

import (
	"context"
	"errors"
	"testing"
)

func TestItemStore(t *testing.T) {
	ctx := context.Background()
	s := NewItemStore()
	if err := s.Put(ctx, Item{SKU: "mug-1", Name: "Mug", PriceCents: 850, OnHand: 4}); err != nil {
		t.Fatal(err)
	}
	if err := s.AdjustStock(ctx, "mug-1", -3); err != nil {
		t.Fatal(err)
	}
	item, err := s.Get(ctx, "mug-1")
	if err != nil || item == nil || item.OnHand != 1 {
		t.Fatalf("Get = %+v, %v", item, err)
	}
	if err := s.AdjustStock(ctx, "mug-1", -2); err == nil {
		t.Fatal("stock went below zero")
	}
}

func TestUnknownSKU(t *testing.T) {
	ctx := context.Background()
	s := NewItemStore()
	if _, err := s.Get(ctx, "lamp-9"); !errors.Is(err, ErrUnknownSKU) {
		t.Fatalf("Get: %v, want ErrUnknownSKU", err)
	}
	if err := s.AdjustStock(ctx, "lamp-9", 1); !errors.Is(err, ErrUnknownSKU) {
		t.Fatalf("AdjustStock: %v, want ErrUnknownSKU", err)
	}
}
