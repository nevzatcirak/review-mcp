package session

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"example.com/orders/cart"
	"example.com/platform/obs"
)

// ErrBadCredentials means the email or the password is wrong.
var ErrBadCredentials = errors.New("session: wrong email or password")

// Accounts checks customers' credentials.
type Accounts interface {
	Authenticate(ctx context.Context, email, password string) (customerID string, err error)
}

// LoginResult is what the sign-in page shows after a successful sign-in.
type LoginResult struct {
	Token   string
	Cart    cart.Cart
	Dropped []string
	Capped  []string
}

// Service signs customers in.
type Service struct {
	Accounts Accounts
	Sessions *Sessions
	Merger   *cart.Merger
}

// Login checks the credentials, moves the guest cart into the customer's
// cart and starts a session.
func (s *Service) Login(ctx context.Context, email, password, guestCartID string) (LoginResult, error) {
	customerID, err := s.Accounts.Authenticate(ctx, email, password)
	if err != nil {
		obs.Printf("session: failed sign-in for %s", mask(email))
		return LoginResult{}, ErrBadCredentials
	}
	merged, err := s.Merger.MergeGuestCart(ctx, guestCartID, customerID)
	if err != nil {
		return LoginResult{}, fmt.Errorf("session: sign-in of %s: %w", customerID, err)
	}
	if len(merged.Dropped) > 0 {
		obs.Printf("session: %d cart lines of %s dropped at sign-in", len(merged.Dropped), customerID)
	}
	token := s.Sessions.Start(customerID)
	return LoginResult{Token: token, Cart: merged.Cart, Dropped: merged.Dropped, Capped: merged.Capped}, nil
}

// mask keeps the first letter and the domain of an email for the logs.
func mask(email string) string {
	user, domain, ok := strings.Cut(email, "@")
	if !ok || user == "" {
		return "***"
	}
	return user[:1] + "***@" + domain
}
