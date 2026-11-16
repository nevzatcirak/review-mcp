package edi

import (
	"context"
	"strings"
	"testing"
	"time"

	"example.com/billing/invoice"
)

type memUploader struct{ files map[string]string }

func (u *memUploader) Upload(_ context.Context, name string, content []byte) error {
	u.files[name] = string(content)
	return nil
}

func TestDeliverAllWritesOneInterchange(t *testing.T) {
	u := &memUploader{files: map[string]string{}}
	c := &Channel{Sender: "SHOP", Uploader: u, Today: func() time.Time { return time.Date(2024, 3, 1, 0, 0, 0, 0, time.UTC) }}
	invs := []invoice.Invoice{{Number: "A-1", PartnerID: "W-9", Total: 1250}, {Number: "A-2", PartnerID: "W-9", Total: 300}}

	sent, err := c.DeliverAll(context.Background(), invs)
	if err != nil || sent != 2 {
		t.Fatalf("DeliverAll = %d, %v; want 2, nil", sent, err)
	}
	got := u.files["INV20240301.edi"]
	if !strings.HasPrefix(got, "UNB+SHOP+20240301+000000001'") || !strings.Contains(got, "UNZ+2+000000001'") {
		t.Fatalf("interchange:\n%s", got)
	}
}

func TestDeliverRefusesSingleInvoices(t *testing.T) {
	c := &Channel{}
	if err := c.Deliver(context.Background(), invoice.Invoice{Number: "A-1"}); err == nil {
		t.Fatal("Deliver accepted a single invoice")
	}
}
