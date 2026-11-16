package delivery

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"example.com/billing/invoice"
)

type singleOnly struct{ got []string }

func (s *singleOnly) Name() string { return "single" }

func (s *singleOnly) Deliver(_ context.Context, inv invoice.Invoice) error {
	s.got = append(s.got, inv.Number)
	return nil
}

type recordingMailer struct {
	to     []string
	failTo string
}

func (m *recordingMailer) Send(_ context.Context, to, _, _ string) error {
	if to == m.failTo {
		return errors.New("mailbox unavailable")
	}
	m.to = append(m.to, to)
	return nil
}

func TestSendOneByOne(t *testing.T) {
	ch := &singleOnly{}
	invs := []invoice.Invoice{{Number: "A-1"}, {Number: "A-2"}}
	if err := (&Dispatcher{}).Send(context.Background(), ch, invs); err != nil {
		t.Fatal(err)
	}
	if len(ch.got) != 2 {
		t.Fatalf("delivered %v, want two invoices", ch.got)
	}
}

func TestSendEmailBatch(t *testing.T) {
	m := &recordingMailer{}
	invs := []invoice.Invoice{{Number: "A-1", Email: "ada@example.com"}, {Number: "A-2", Email: "bo@example.com"}}
	if err := (&Dispatcher{}).Send(context.Background(), &EmailChannel{Mailer: m}, invs); err != nil {
		t.Fatal(err)
	}
	if len(m.to) != 2 {
		t.Fatalf("mailed %v, want two recipients", m.to)
	}
}

func TestSendLogsAPartialBatch(t *testing.T) {
	var logged []string
	d := &Dispatcher{Log: func(format string, args ...any) { logged = append(logged, fmt.Sprintf(format, args...)) }}
	m := &recordingMailer{failTo: "bo@example.com"}
	invs := []invoice.Invoice{{Number: "A-1", Email: "ada@example.com"}, {Number: "A-2", Email: "bo@example.com"}}

	if err := d.Send(context.Background(), &EmailChannel{Mailer: m}, invs); err == nil {
		t.Fatal("Send succeeded despite the failed email")
	}
	if len(logged) != 1 || logged[0] != "email: delivered 1 of 2 invoices before the failure" {
		t.Fatalf("logged %q", logged)
	}
}
