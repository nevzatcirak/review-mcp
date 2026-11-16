package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"example.com/inventory/store"
	"example.com/platform/obs"
)

func (s *Server) getItem(w http.ResponseWriter, r *http.Request) {
	item, err := s.Items.Get(r.Context(), r.PathValue("sku"))
	if errors.Is(err, store.ErrUnknownSKU) {
		http.Error(w, "no such item", http.StatusNotFound)
		return
	}
	if err != nil {
		obs.Printf("api: get item: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
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
		obs.Printf("api: search: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, entries)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		obs.Printf("api: encode response: %v", err)
	}
}
