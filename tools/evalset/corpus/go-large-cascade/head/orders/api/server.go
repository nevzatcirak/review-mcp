// Package api serves the shop's cart, sign-in and checkout endpoints.
package api

import (
	"net/http"

	"example.com/orders/cart"
	"example.com/orders/checkout"
	"example.com/orders/session"
	"example.com/platform/obs"
)

// Server bundles the handlers of the orders API.
type Server struct {
	Carts    *cart.Store
	Catalog  cart.Catalog
	Sessions *session.Sessions
	Login    *session.Service
	Checkout *checkout.Service
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /cart/lines", s.addLine)
	mux.HandleFunc("GET /cart", s.getCart)
	mux.HandleFunc("POST /login", s.login)
	mux.HandleFunc("POST /logout", s.logout)
	mux.HandleFunc("POST /checkout", s.checkout)
	return recoverPanics(mux)
}

func recoverPanics(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				obs.Printf("api: panic serving %s %s: %v", r.Method, r.URL.Path, v)
				http.Error(w, "internal error", http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}
