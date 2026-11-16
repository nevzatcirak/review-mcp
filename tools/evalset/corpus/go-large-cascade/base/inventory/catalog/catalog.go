// Package catalog is the read side of the inventory that the shop uses:
// what an item is called, what it costs and how many can still be sold. It
// caches entries for a short time, because the cart and checkout pages ask
// for the same SKUs many times a second.
package catalog

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"example.com/inventory/store"
)

// Entry is what the shop needs to know about an item.
type Entry struct {
	SKU        string
	Name       string
	PriceCents int64
	Available  int
}

// Catalog answers item questions from the item store and the reservations.
type Catalog struct {
	items        *store.ItemStore
	reservations *store.Reservations
	ttl          time.Duration
	now          func() time.Time

	mu     sync.Mutex
	cache  map[string]cached
	hits   int
	misses int
}

type cached struct {
	entry *Entry
	at    time.Time
}

// New returns a catalog whose entries are cached for ttl.
func New(items *store.ItemStore, reservations *store.Reservations, ttl time.Duration) *Catalog {
	return &Catalog{
		items:        items,
		reservations: reservations,
		ttl:          ttl,
		now:          time.Now,
		cache:        make(map[string]cached),
	}
}

// Lookup returns the catalog entry of sku, from the cache when it is fresh.
func (c *Catalog) Lookup(ctx context.Context, sku string) (*Entry, error) {
	if entry, ok := c.fromCache(sku); ok {
		return entry, nil
	}
	item, err := c.items.Get(ctx, sku)
	if err != nil {
		return nil, fmt.Errorf("catalog: lookup %s: %w", sku, err)
	}
	var entry *Entry
	if item != nil {
		if entry, err = c.entryFor(ctx, *item); err != nil {
			return nil, err
		}
	}
	c.remember(sku, entry)
	return entry, nil
}

// entryFor builds the entry of an item from the store.
func (c *Catalog) entryFor(ctx context.Context, item store.Item) (*Entry, error) {
	reserved, err := c.reservations.Reserved(ctx, item.SKU)
	if err != nil {
		return nil, fmt.Errorf("catalog: reservations of %s: %w", item.SKU, err)
	}
	return &Entry{
		SKU:        item.SKU,
		Name:       item.Name,
		PriceCents: item.PriceCents,
		Available:  max(item.OnHand-reserved, 0),
	}, nil
}

// LookupMany looks up several SKUs and returns the entries by SKU.
func (c *Catalog) LookupMany(ctx context.Context, skus []string) (map[string]*Entry, error) {
	out := make(map[string]*Entry, len(skus))
	for _, sku := range skus {
		entry, err := c.Lookup(ctx, sku)
		if err != nil {
			return nil, err
		}
		out[sku] = entry
	}
	return out, nil
}

// Availability is how many units of sku can still be sold, bypassing the
// cache; checkout asks it right before taking the payment.
func (c *Catalog) Availability(ctx context.Context, sku string) (int, error) {
	item, err := c.items.Get(ctx, sku)
	if err != nil {
		return 0, fmt.Errorf("catalog: availability of %s: %w", sku, err)
	}
	if item == nil {
		return 0, nil
	}
	reserved, err := c.reservations.Reserved(ctx, sku)
	if err != nil {
		return 0, fmt.Errorf("catalog: reservations of %s: %w", sku, err)
	}
	return max(item.OnHand-reserved, 0), nil
}

// Stats is a snapshot of the cache, for the health page.
type Stats struct {
	Entries int
	Hits    int
	Misses  int
	TTL     time.Duration
}

// Stats returns the cache's size and hit counters since the last LogStats.
func (c *Catalog) Stats() Stats {
	c.mu.Lock()
	defer c.mu.Unlock()
	return Stats{Entries: len(c.cache), Hits: c.hits, Misses: c.misses, TTL: c.ttl}
}

// SetTTL changes how long entries stay fresh. Entries already cached keep
// the time they were stored, so a shorter TTL takes effect at once.
func (c *Catalog) SetTTL(ttl time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ttl = ttl
}

// Prune drops the entries that are no longer fresh, so that SKUs that are
// asked for once do not stay in memory.
func (c *Catalog) Prune() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	pruned := 0
	for sku, hit := range c.cache {
		if c.now().Sub(hit.at) >= c.ttl {
			delete(c.cache, sku)
			pruned++
		}
	}
	return pruned
}

// Invalidate drops the cached entry of sku, for example after a price change.
func (c *Catalog) Invalidate(sku string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.cache, sku)
}

// Warm loads every item into the cache, so that the first page views after
// a start do not all miss.
func (c *Catalog) Warm(ctx context.Context) error {
	items, err := c.items.List(ctx)
	if err != nil {
		return fmt.Errorf("catalog: warm: %w", err)
	}
	for _, item := range items {
		entry, err := c.entryFor(ctx, item)
		if err != nil {
			return err
		}
		c.remember(item.SKU, entry)
	}
	log.Printf("catalog: warmed %d entries", len(items))
	return nil
}

// LogStats writes the cache hit rate since the last call and resets it.
func (c *Catalog) LogStats() {
	c.mu.Lock()
	hits, misses := c.hits, c.misses
	c.hits, c.misses = 0, 0
	size := len(c.cache)
	c.mu.Unlock()
	if hits+misses == 0 {
		return
	}
	log.Printf("catalog: %d entries, %d hits, %d misses (%.0f%% hit rate)",
		size, hits, misses, 100*float64(hits)/float64(hits+misses))
}

func (c *Catalog) fromCache(sku string) (*Entry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	hit, ok := c.cache[sku]
	if !ok || c.now().Sub(hit.at) >= c.ttl {
		c.misses++
		return nil, false
	}
	c.hits++
	return hit.entry, true
}

func (c *Catalog) remember(sku string, entry *Entry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cache[sku] = cached{entry: entry, at: c.now()}
}
