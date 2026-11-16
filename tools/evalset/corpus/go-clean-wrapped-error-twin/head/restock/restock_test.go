package restock

import (
	"context"
	"testing"

	"example.com/inventory/store"
)

func TestReceiveAddsToStock(t *testing.T) {
	s := store.New()
	ctx := context.Background()
	if err := s.Put(ctx, store.Item{SKU: "mug-1", Name: "Mug", OnHand: 3}); err != nil {
		t.Fatal(err)
	}
	r := &Restocker{Store: s}

	if err := r.Receive(ctx, []Line{{SKU: "mug-1", Quantity: 10}, {SKU: "mug-1", Quantity: 2}}); err != nil {
		t.Fatal(err)
	}
	item, _ := s.Get(ctx, "mug-1")
	if item.OnHand != 15 {
		t.Fatalf("on hand = %d, want 15", item.OnHand)
	}
}

func TestReceiveCreatesNewItems(t *testing.T) {
	s := store.New()
	ctx := context.Background()
	r := &Restocker{Store: s}

	if err := r.Receive(ctx, []Line{{SKU: "bowl-2", Name: "Bowl", Quantity: 6}}); err != nil {
		t.Fatal(err)
	}
	item, err := s.Get(ctx, "bowl-2")
	if err != nil || item.OnHand != 6 || item.Name != "Bowl" {
		t.Fatalf("Get = %+v, %v", item, err)
	}
}
