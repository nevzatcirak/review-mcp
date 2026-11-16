package api

import (
	"errors"
	"net/http"

	"example.com/orders/checkout"
	"example.com/platform/obs"
)

func (s *Server) checkout(w http.ResponseWriter, r *http.Request) {
	c, err := r.Cookie("session")
	if err != nil {
		http.Error(w, "sign in first", http.StatusUnauthorized)
		return
	}
	customerID, ok := s.Sessions.Customer(c.Value)
	if !ok {
		http.Error(w, "sign in first", http.StatusUnauthorized)
		return
	}
	order, err := s.Checkout.Checkout(r.Context(), customerID)
	switch {
	case errors.Is(err, checkout.ErrItemGone), errors.Is(err, checkout.ErrOutOfStock):
		http.Error(w, err.Error(), http.StatusConflict)
	case errors.Is(err, checkout.ErrDeclined):
		http.Error(w, "payment declined", http.StatusPaymentRequired)
	case err != nil:
		obs.Printf("api: checkout of %s: %v", customerID, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
	default:
		writeJSON(w, http.StatusCreated, order)
	}
}
