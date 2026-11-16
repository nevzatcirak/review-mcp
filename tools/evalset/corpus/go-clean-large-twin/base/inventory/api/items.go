package api

import (
	"encoding/json"
	"log"
	"net/http"
	"strconv"
)

func (s *Server) getItem(w http.ResponseWriter, r *http.Request) {
	item, err := s.Items.Get(r.Context(), r.PathValue("sku"))
	if err != nil {
		log.Printf("api: get item: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if item == nil {
		http.Error(w, "no such item", http.StatusNotFound)
		return
	}
	writeJSON(w, map[string]any{"sku": item.SKU, "name": item.Name, "price_cents": item.PriceCents, "on_hand": item.OnHand})
}

func (s *Server) search(w http.ResponseWriter, r *http.Request) {
	limit, err := strconv.Atoi(r.URL.Query().Get("limit"))
	if err != nil || limit <= 0 || limit > 50 {
		limit = 20
	}
	entries, err := s.Catalog.Search(r.Context(), r.URL.Query().Get("q"), limit)
	if err != nil {
		log.Printf("api: search: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, entries)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("api: encode response: %v", err)
	}
}
