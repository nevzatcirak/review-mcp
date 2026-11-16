// Package api serves the inventory over HTTP.
package api

import (
	"log"
	"net/http"
	"time"

	"example.com/inventory/catalog"
	"example.com/inventory/reserve"
	"example.com/inventory/store"
)

// Server bundles the handlers of the inventory API.
type Server struct {
	Items    *store.ItemStore
	Catalog  *catalog.Catalog
	Reserver *reserve.Reserver
}

// Handler returns the API's routes.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /items/{sku}", s.getItem)
	mux.HandleFunc("GET /search", s.search)
	mux.HandleFunc("POST /reservations", s.createReservation)
	mux.HandleFunc("DELETE /reservations/{id}", s.deleteReservation)
	mux.HandleFunc("GET /healthz", s.health)
	return logRequests(mux)
}

func logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		log.Printf("api: %s %s in %s", r.Method, r.URL.Path, time.Since(start).Round(time.Millisecond))
	})
}
