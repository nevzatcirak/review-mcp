package store

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"
)

// Reservation holds stock for a cart until it expires or is checked out.
type Reservation struct {
	ID        string
	SKU       string
	Quantity  int
	ExpiresAt time.Time
}

// Reservations keeps the open reservations. It is safe for concurrent use.
type Reservations struct {
	mu   sync.Mutex
	byID map[string]Reservation
}

func NewReservations() *Reservations {
	return &Reservations{byID: make(map[string]Reservation)}
}

func (r *Reservations) Add(ctx context.Context, res Reservation) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if res.Quantity <= 0 {
		return fmt.Errorf("store: reservation %s: quantity %d", res.ID, res.Quantity)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.byID[res.ID] = res
	return nil
}

func (r *Reservations) Remove(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.byID, id)
	return nil
}

// Reserved is the quantity of sku held by open reservations.
func (r *Reservations) Reserved(ctx context.Context, sku string) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	total := 0
	for _, res := range r.byID {
		if res.SKU == sku {
			total += res.Quantity
		}
	}
	return total, nil
}

// Expire removes the reservations that expired before now and returns them.
func (r *Reservations) Expire(now time.Time) []Reservation {
	r.mu.Lock()
	defer r.mu.Unlock()
	var expired []Reservation
	for id, res := range r.byID {
		if res.ExpiresAt.Before(now) {
			expired = append(expired, res)
			delete(r.byID, id)
		}
	}
	if len(expired) > 0 {
		log.Printf("store: %d reservations expired", len(expired))
	}
	return expired
}
