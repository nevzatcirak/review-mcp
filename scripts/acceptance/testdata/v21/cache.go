// Package v21 holds small helpers.
package v21

import "sync"

// Cache is a string cache safe for concurrent use.
type Cache struct {
	mu sync.Mutex
	m  map[string]string
}

// Get returns the value stored for key.
func (c *Cache) Get(key string) string {
	return c.m[key]
}

// Set stores value for key.
func (c *Cache) Set(key, value string) {
	c.mu.Lock()
	c.m[key] = value
	c.mu.Unlock()
}
