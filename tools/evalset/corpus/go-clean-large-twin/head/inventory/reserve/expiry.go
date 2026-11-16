package reserve

import (
	"context"
	"time"

	"example.com/platform/obs"
)

// RunExpiry removes expired reservations every interval until ctx ends.
func (r *Reserver) RunExpiry(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			obs.Printf("reserve: expiry stopped")
			return
		case <-ticker.C:
			for _, res := range r.Reservations.Expire(r.Now()) {
				r.Catalog.Invalidate(res.SKU)
			}
		}
	}
}
