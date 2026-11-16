// Package store keeps the warehouse's items and reservations.
package store

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
)

// Item is a stock-keeping unit of the warehouse.
type Item struct {
	SKU        string
	Name       string
	PriceCents int64
	OnHand     int
}

// ItemStore keeps the items in memory. It is safe for concurrent use.
type ItemStore struct {
	mu    sync.RWMutex
	items map[string]Item
}

// NewItemStore returns an empty store.
func NewItemStore() *ItemStore {
	return &ItemStore{items: make(map[string]Item)}
}

// Get returns the item with the given SKU, or nil when the store does not
// know the SKU.
func (s *ItemStore) Get(ctx context.Context, sku string) (*Item, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	item, ok := s.items[sku]
	if !ok {
		return nil, nil
	}
	return &item, nil
}

// Put stores the item, replacing any item with the same SKU.
func (s *ItemStore) Put(ctx context.Context, item Item) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if item.SKU == "" {
		return errors.New("store: an item needs a SKU")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.items[item.SKU] = item
	return nil
}

// Delete removes the item. Deleting an unknown SKU is not an error.
func (s *ItemStore) Delete(ctx context.Context, sku string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.items, sku)
	return nil
}

// AdjustStock adds delta (which may be negative) to the stock on hand.
func (s *ItemStore) AdjustStock(ctx context.Context, sku string, delta int) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	item, ok := s.items[sku]
	if !ok {
		return fmt.Errorf("store: adjust %s: unknown SKU", sku)
	}
	if item.OnHand+delta < 0 {
		return fmt.Errorf("store: adjust %s by %d: only %d on hand", sku, delta, item.OnHand)
	}
	item.OnHand += delta
	s.items[sku] = item
	return nil
}

// List returns all items ordered by SKU.
func (s *ItemStore) List(ctx context.Context) ([]Item, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Item, 0, len(s.items))
	for _, item := range s.items {
		out = append(out, item)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].SKU < out[j].SKU })
	return out, nil
}
