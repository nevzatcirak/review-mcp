package catalog

import (
	"context"
	"testing"
	"time"

	"example.com/inventory/store"
)

func TestLookupCountsReservations(t *testing.T) {
	ctx := context.Background()
	items, reservations := store.NewItemStore(), store.NewReservations()
	if err := items.Put(ctx, store.Item{SKU: "mug-1", Name: "Mug", PriceCents: 850, OnHand: 5}); err != nil {
		t.Fatal(err)
	}
	if err := reservations.Add(ctx, store.Reservation{ID: "r-1", SKU: "mug-1", Quantity: 2, ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	c := New(items, reservations, time.Minute)

	entry, err := c.Lookup(ctx, "mug-1")
	if err != nil || entry == nil || entry.Available != 3 || entry.Name != "Mug" {
		t.Fatalf("Lookup = %+v, %v", entry, err)
	}
	if n, err := c.Availability(ctx, "mug-1"); err != nil || n != 3 {
		t.Fatalf("Availability = %d, %v", n, err)
	}
}

func TestLookupUsesTheCache(t *testing.T) {
	ctx := context.Background()
	items := store.NewItemStore()
	if err := items.Put(ctx, store.Item{SKU: "mug-1", Name: "Mug", OnHand: 5}); err != nil {
		t.Fatal(err)
	}
	c := New(items, store.NewReservations(), time.Minute)
	if _, err := c.Lookup(ctx, "mug-1"); err != nil {
		t.Fatal(err)
	}
	if err := items.Put(ctx, store.Item{SKU: "mug-1", Name: "Large mug", OnHand: 5}); err != nil {
		t.Fatal(err)
	}
	entry, _ := c.Lookup(ctx, "mug-1")
	if entry.Name != "Mug" {
		t.Fatalf("Lookup within the TTL = %q, want the cached name", entry.Name)
	}
	c.Invalidate("mug-1")
	entry, _ = c.Lookup(ctx, "mug-1")
	if entry.Name != "Large mug" {
		t.Fatalf("Lookup after Invalidate = %q", entry.Name)
	}
}
