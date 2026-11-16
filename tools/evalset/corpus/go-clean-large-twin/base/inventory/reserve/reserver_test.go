package reserve

import (
	"context"
	"errors"
	"testing"
	"time"

	"example.com/inventory/catalog"
	"example.com/inventory/store"
)

func TestReserve(t *testing.T) {
	ctx := context.Background()
	items, reservations := store.NewItemStore(), store.NewReservations()
	if err := items.Put(ctx, store.Item{SKU: "mug-1", Name: "Mug", OnHand: 3}); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2024, 3, 1, 12, 0, 0, 0, time.UTC)
	r := &Reserver{Items: items, Reservations: reservations, Catalog: catalog.New(items, reservations, time.Minute),
		Hold: 15 * time.Minute, Now: func() time.Time { return now }}

	res, err := r.Reserve(ctx, "mug-1", 2)
	if err != nil || !res.ExpiresAt.Equal(now.Add(15*time.Minute)) {
		t.Fatalf("Reserve = %+v, %v", res, err)
	}
	if _, err := r.Reserve(ctx, "mug-1", 2); !errors.Is(err, ErrNotEnoughStock) {
		t.Fatalf("second Reserve: %v, want ErrNotEnoughStock", err)
	}
	if _, err := r.Reserve(ctx, "lamp-9", 1); !errors.Is(err, ErrNoSuchItem) {
		t.Fatalf("unknown SKU: %v, want ErrNoSuchItem", err)
	}
}
