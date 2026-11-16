package store

import (
	"context"
	"errors"
	"sort"
	"sync"
)

// ErrNotFound is returned for a SKU the store does not know.
var ErrNotFound = errors.New("store: item not found")

// Store is an in-memory item store, safe for concurrent use.
type Store struct {
	mu    sync.RWMutex
	items map[string]Item
}

// New returns an empty store.
func New() *Store {
	return &Store{items: make(map[string]Item)}
}

// Get returns the item with the given SKU.
func (s *Store) Get(ctx context.Context, sku string) (Item, error) {
	if err := ctx.Err(); err != nil {
		return Item{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	item, ok := s.items[sku]
	if !ok {
		return Item{}, ErrNotFound
	}
	return item, nil
}

// Put stores the item, replacing any item with the same SKU.
func (s *Store) Put(ctx context.Context, item Item) error {
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

// List returns all items ordered by SKU.
func (s *Store) List(ctx context.Context) ([]Item, error) {
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
