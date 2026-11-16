// Package reserve holds stock for carts.
package reserve

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"time"

	"example.com/inventory/catalog"
	"example.com/inventory/store"
)

var (
	ErrNoSuchItem     = errors.New("reserve: no such item")
	ErrNotEnoughStock = errors.New("reserve: not enough stock")
)

// Reserver places and releases reservations.
type Reserver struct {
	Items        *store.ItemStore
	Reservations *store.Reservations
	Catalog      *catalog.Catalog
	Hold         time.Duration
	Now          func() time.Time
}

// Reserve holds quantity units of sku for Hold.
func (r *Reserver) Reserve(ctx context.Context, sku string, quantity int) (store.Reservation, error) {
	item, err := r.Items.Get(ctx, sku)
	if err != nil {
		return store.Reservation{}, fmt.Errorf("reserve %s: %w", sku, err)
	}
	if item == nil {
		return store.Reservation{}, ErrNoSuchItem
	}
	available, err := r.Catalog.Availability(ctx, sku)
	if err != nil {
		return store.Reservation{}, err
	}
	if quantity > available {
		return store.Reservation{}, ErrNotEnoughStock
	}
	res := store.Reservation{ID: newID(), SKU: sku, Quantity: quantity, ExpiresAt: r.Now().Add(r.Hold)}
	if err := r.Reservations.Add(ctx, res); err != nil {
		return store.Reservation{}, err
	}
	r.Catalog.Invalidate(sku)
	log.Printf("reserve: %d x %s held until %s", quantity, sku, res.ExpiresAt.Format(time.RFC3339))
	return res, nil
}

// Release gives the stock of a reservation back.
func (r *Reserver) Release(ctx context.Context, id string) error {
	return r.Reservations.Remove(ctx, id)
}

func newID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}
