package edi

import (
	"context"
	"errors"
	"time"

	"example.com/billing/invoice"
)

// Uploader puts a file into the partner's inbox.
type Uploader interface {
	Upload(ctx context.Context, name string, content []byte) error
}

// Channel delivers the day's invoices for the partner as a single
// interchange. The partner accepts exactly one interchange per day; a
// second one on the same day is rejected as a duplicate.
type Channel struct {
	Sender   string
	Uploader Uploader
	Today    func() time.Time

	control int
}

var errSingle = errors.New("edi: the partner takes one interchange per day; deliver the invoices as a batch")

func (c *Channel) Name() string { return "edi" }

// Deliver refuses single invoices: see the type documentation.
func (c *Channel) Deliver(context.Context, invoice.Invoice) error {
	return errSingle
}

// DeliverBatch uploads all invoices of the day as one interchange.
func (c *Channel) DeliverBatch(ctx context.Context, invs []invoice.Invoice) error {
	c.control++
	day := c.Today()
	name := "INV" + day.Format("20060102") + ".edi"
	return c.Uploader.Upload(ctx, name, []byte(interchange(c.Sender, c.control, day, invs)))
}
