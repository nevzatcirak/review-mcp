package cart

import (
	"sync"
	"time"

	"example.com/platform/obs"
)

// Store keeps carts in memory. It is safe for concurrent use.
type Store struct {
	mu      sync.Mutex
	carts   map[string]Cart
	touched map[string]time.Time
	now     func() time.Time
}

func NewStore(now func() time.Time) *Store {
	return &Store{carts: make(map[string]Cart), touched: make(map[string]time.Time), now: now}
}

// Get returns a copy of the cart and whether it exists.
func (s *Store) Get(id string) (Cart, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.carts[id]
	if !ok {
		return Cart{ID: id}, false
	}
	c.Lines = append([]Line(nil), c.Lines...)
	return c, true
}

func (s *Store) Save(c Cart) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.carts[c.ID] = c
	s.touched[c.ID] = s.now()
}

func (s *Store) Delete(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.carts, id)
	delete(s.touched, id)
}

// Purge removes guest carts not touched for maxAge; customers' carts stay.
func (s *Store) Purge(maxAge time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cutoff := s.now().Add(-maxAge)
	purged := 0
	for id, at := range s.touched {
		if at.Before(cutoff) && !isCustomerCart(id) {
			delete(s.carts, id)
			delete(s.touched, id)
			purged++
		}
	}
	obs.Printf("cart: purged %d guest carts older than %s", purged, maxAge)
}

func isCustomerCart(id string) bool {
	return len(id) > len("customer:") && id[:len("customer:")] == "customer:"
}
