// Package invoice holds the invoice type shared by the delivery channels.
package invoice

import "time"

// Invoice is an issued invoice. Amounts are in cents.
type Invoice struct {
	Number    string
	Customer  string
	Email     string
	PartnerID string
	IssuedOn  time.Time
	Total     int64
}
