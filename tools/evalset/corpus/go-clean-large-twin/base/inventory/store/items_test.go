package store

import (
	"context"
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
