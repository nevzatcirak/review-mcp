package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"example.com/inventory/reserve"
	"example.com/platform/obs"
)

type reservationRequest struct {
	SKU      string `json:"sku"`
	Quantity int    `json:"quantity"`
}

func (s *Server) createReservation(w http.ResponseWriter, r *http.Request) {
	var req reservationRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request body", http.StatusBadRequest)
		return
	}
	res, err := s.Reserver.Reserve(r.Context(), req.SKU, req.Quantity)
	switch {
	case errors.Is(err, reserve.ErrNoSuchItem):
		http.Error(w, "no such item", http.StatusNotFound)
	case errors.Is(err, reserve.ErrNotEnoughStock):
		http.Error(w, "not enough stock", http.StatusConflict)
	case err != nil:
		obs.Printf("api: reserve %s: %v", req.SKU, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
	default:
		w.WriteHeader(http.StatusCreated)
		writeJSON(w, map[string]any{"id": res.ID, "expires_at": res.ExpiresAt})
	}
}

func (s *Server) deleteReservation(w http.ResponseWriter, r *http.Request) {
	if err := s.Reserver.Release(r.Context(), r.PathValue("id")); err != nil {
		obs.Printf("api: release %s: %v", r.PathValue("id"), err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
