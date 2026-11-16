// Package restock books supplier deliveries into stock.
package restock

import (
	"context"
	"errors"
	"fmt"

	"example.com/inventory/store"
)

// Line is one line of a delivery note.
type Line struct {
	SKU      string
	Name     string
	Quantity int
}

// Restocker adds delivered quantities to the store.
type Restocker struct {
	Store *store.Store
}

// Receive books every line of a delivery. A SKU the store does not know yet
// is a new product: it is created with the delivered quantity.
func (r *Restocker) Receive(ctx context.Context, lines []Line) error {
	seen := make(map[string]store.Item, len(lines))
	for _, line := range lines {
		if line.Quantity <= 0 {
			return fmt.Errorf("restock: %s: quantity %d", line.SKU, line.Quantity)
		}
		item, err := r.current(ctx, seen, line.SKU)
		if errors.Is(err, store.ErrNotFound) {
			item = store.Item{SKU: line.SKU, Name: line.Name}
		} else if err != nil {
			return fmt.Errorf("restock: %s: %w", line.SKU, err)
		}
		item.OnHand += line.Quantity
		seen[line.SKU] = item
	}
	for _, item := range seen {
		if err := r.Store.Put(ctx, item); err != nil {
			return fmt.Errorf("restock: %s: %w", item.SKU, err)
		}
	}
	return nil
}

// current returns the item as booked so far in this delivery, or as stored.
func (r *Restocker) current(ctx context.Context, seen map[string]store.Item, sku string) (store.Item, error) {
	if item, ok := seen[sku]; ok {
		return item, nil
	}
	return r.Store.Get(ctx, sku)
}
