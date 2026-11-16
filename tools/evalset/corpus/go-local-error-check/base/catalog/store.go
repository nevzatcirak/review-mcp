package catalog

import (
	"errors"
	"sync"
)

// ErrUnknownSKU is returned for a SKU without a price.
var ErrUnknownSKU = errors.New("catalog: no price for this SKU")

// Store keeps the current price of each SKU. It is safe for concurrent use.
type Store struct {
	mu     sync.RWMutex
	prices map[string]Price
}

func NewStore() *Store {
	return &Store{prices: make(map[string]Price)}
}

// Set replaces the prices of the given SKUs and leaves the others alone.
func (s *Store) Set(prices []Price) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, p := range prices {
		s.prices[p.SKU] = p
	}
}

func (s *Store) Get(sku string) (Price, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	p, ok := s.prices[sku]
	if !ok {
		return Price{}, ErrUnknownSKU
	}
	return p, nil
}
