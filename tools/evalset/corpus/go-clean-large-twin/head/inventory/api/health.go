package api

import (
	"net/http"

	"example.com/platform/obs"
)

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	if _, err := s.Items.List(r.Context()); err != nil {
		obs.Printf("api: health: %v", err)
		http.Error(w, "unhealthy", http.StatusServiceUnavailable)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
