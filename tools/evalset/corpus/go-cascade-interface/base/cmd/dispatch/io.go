package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/smtp"
	"os"
	"path/filepath"
	"time"

	"example.com/billing/invoice"
)

// loadIssued reads the invoices issued on day from dir/<yyyy-mm-dd>.json.
func loadIssued(dir string, day time.Time) ([]invoice.Invoice, error) {
	data, err := os.ReadFile(filepath.Join(dir, day.Format("2006-01-02")+".json"))
	if err != nil {
		return nil, err
	}
	var invs []invoice.Invoice
	if err := json.Unmarshal(data, &invs); err != nil {
		return nil, fmt.Errorf("invoices of %s: %w", day.Format("2006-01-02"), err)
	}
	return invs, nil
}

type smtpMailer struct{ host string }

func (m smtpMailer) Send(_ context.Context, to, subject, body string) error {
	msg := "To: " + to + "\r\nSubject: " + subject + "\r\n\r\n" + body
	return smtp.SendMail(m.host+":25", nil, "billing@example.com", []string{to}, []byte(msg))
}

// sftpInbox stands for the partner's inbox, mounted as a directory.
type sftpInbox struct{ dir string }

func (s sftpInbox) Upload(_ context.Context, name string, content []byte) error {
	return os.WriteFile(filepath.Join(s.dir, name), content, 0o600)
}
