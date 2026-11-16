// Package session signs customers in and keeps their sessions.
package session

import (
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"

	"example.com/platform/obs"
)

// Sessions maps session tokens to customers. It is safe for concurrent use.
type Sessions struct {
	mu     sync.Mutex
	byTok  map[string]entry
	maxAge time.Duration
	now    func() time.Time
}

type entry struct {
	customerID string
	started    time.Time
}

func NewSessions(maxAge time.Duration, now func() time.Time) *Sessions {
	return &Sessions{byTok: make(map[string]entry), maxAge: maxAge, now: now}
}

// Start opens a session and returns its token.
func (s *Sessions) Start(customerID string) string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	token := hex.EncodeToString(b[:])
	s.mu.Lock()
	defer s.mu.Unlock()
	s.byTok[token] = entry{customerID: customerID, started: s.now()}
	return token
}

// Customer returns the customer of a live session.
func (s *Sessions) Customer(token string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.byTok[token]
	if !ok {
		return "", false
	}
	if s.now().Sub(e.started) > s.maxAge {
		delete(s.byTok, token)
		obs.Printf("session: expired session of %s", e.customerID)
		return "", false
	}
	return e.customerID, true
}

func (s *Sessions) End(token string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.byTok, token)
}
