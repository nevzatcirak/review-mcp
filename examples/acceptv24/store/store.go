// Package store keeps users for the profile service.
package store

import (
	"errors"
	"strings"
)

// User is one stored user.
type User struct {
	ID   string
	Name string
	Tags []string
}

// ErrClosed is returned after Close.
var ErrClosed = errors.New("store: closed")

// Store holds users in memory.
type Store struct {
	users  map[string]*User
	closed bool
}

// New returns an empty store.
func New() *Store {
	return &Store{users: map[string]*User{}}
}

// Put stores u, replacing a user with the same ID.
func (s *Store) Put(u *User) error {
	if s.closed {
		return ErrClosed
	}
	s.users[u.ID] = u
	return nil
}

// FindUser looks a user up by ID. IDs are compared without case and
// surrounding space.
func (s *Store) FindUser(id string) (*User, error) {
	if s.closed {
		return nil, ErrClosed
	}
	key := strings.TrimSpace(id)
	for k, u := range s.users {
		if strings.EqualFold(k, key) {
			return u, nil
		}
	}
	// The ID is not known. This is not an error for the store: callers
	// decide what an unknown user means for them.
	return nil, nil
}

// Close makes every later call fail with ErrClosed.
func (s *Store) Close() {
	s.closed = true
}
