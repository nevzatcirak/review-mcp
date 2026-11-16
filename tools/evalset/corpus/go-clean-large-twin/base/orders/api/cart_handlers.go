package api

import (
	"encoding/json"
	"log"
	"net/http"

	"example.com/orders/cart"
)

type addLineRequest struct {
	SKU      string `json:"sku"`
	Quantity int    `json:"quantity"`
}

// cartID is the signed-in customer's cart, or the guest cart of the cookie.
func (s *Server) cartID(r *http.Request) string {
	if c, err := r.Cookie("session"); err == nil {
		if customerID, ok := s.Sessions.Customer(c.Value); ok {
			return cart.CustomerCartID(customerID)
		}
	}
	if c, err := r.Cookie("guest"); err == nil && c.Value != "" {
		return c.Value
	}
	return ""
}

func (s *Server) addLine(w http.ResponseWriter, r *http.Request) {
	id := s.cartID(r)
	if id == "" {
		http.Error(w, "no cart", http.StatusBadRequest)
		return
	}
	var req addLineRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Quantity <= 0 {
		http.Error(w, "bad request body", http.StatusBadRequest)
		return
	}
	entry, err := s.Catalog.Lookup(r.Context(), req.SKU)
	if err != nil {
		log.Printf("api: add %s to cart: %v", req.SKU, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if entry == nil {
		http.Error(w, "no such product", http.StatusNotFound)
		return
	}
	c, _ := s.Carts.Get(id)
	c.Set(req.SKU, c.QuantityOf(req.SKU)+req.Quantity, entry.PriceCents)
	s.Carts.Save(c)
	writeJSON(w, http.StatusOK, c)
}

func (s *Server) getCart(w http.ResponseWriter, r *http.Request) {
	c, _ := s.Carts.Get(s.cartID(r))
	writeJSON(w, http.StatusOK, c)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("api: encode response: %v", err)
	}
}
