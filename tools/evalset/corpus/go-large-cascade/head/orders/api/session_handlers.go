package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"example.com/orders/session"
	"example.com/platform/obs"
)

type loginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request body", http.StatusBadRequest)
		return
	}
	guest := ""
	if c, err := r.Cookie("guest"); err == nil {
		guest = c.Value
	}
	res, err := s.Login.Login(r.Context(), req.Email, req.Password, guest)
	if errors.Is(err, session.ErrBadCredentials) {
		http.Error(w, "wrong email or password", http.StatusUnauthorized)
		return
	}
	if err != nil {
		obs.Printf("api: login: %v", err)
		http.Error(w, "sign-in failed, please try again", http.StatusInternalServerError)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: "session", Value: res.Token, Path: "/", HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode})
	writeJSON(w, http.StatusOK, map[string]any{"cart": res.Cart, "dropped": res.Dropped, "capped": res.Capped})
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie("session"); err == nil {
		s.Sessions.End(c.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: "session", Value: "", Path: "/", MaxAge: -1})
	w.WriteHeader(http.StatusNoContent)
}
