// Package delivery sends invoices to their recipients.
package delivery

import (
	"context"

	"example.com/billing/invoice"
)

// Channel delivers invoices to one kind of recipient.
type Channel interface {
	Name() string
	Deliver(ctx context.Context, inv invoice.Invoice) error
}

// BatchChannel is implemented by channels that can deliver many invoices
// at once. The dispatcher prefers it when a channel offers it.
type BatchChannel interface {
	DeliverBatch(ctx context.Context, invs []invoice.Invoice) error
}
