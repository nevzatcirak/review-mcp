package httpx

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nevzatcirak/review-mcp/internal/provider"
)

func TestGetHeaders(t *testing.T) {
	const userMarker = "USERMARKER-31aa"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/ctx/denied" {
			w.Header().Set("X-AUSERNAME", userMarker)
			http.Error(w, "no", http.StatusUnauthorized)
			return
		}
		w.Header().Set("X-AUSERNAME", userMarker)
		w.Header().Add("X-AUSERID", "7")
		w.Header().Add("X-AUSERID", "8")
		_, _ = w.Write([]byte(`{"version":"8.9.0"}`))
	}))
	defer srv.Close()
	var logs bytes.Buffer
	c := newClient(t, srv.URL+"/ctx", func(o *Options) {
		o.Logger = slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	})
	h, err := c.GetHeaders(context.Background(), "/props", "X-AUSERNAME", "x-auserid", "X-Missing")
	if err != nil {
		t.Fatal(err)
	}
	if h["X-AUSERNAME"] != userMarker || h["x-auserid"] != "7" || h["X-Missing"] != "" || len(h) != 3 {
		t.Fatalf("headers = %v", h)
	}
	_, err = c.GetHeaders(context.Background(), "/denied", "X-AUSERNAME")
	if !errors.Is(err, provider.ErrAuth) {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(logs.String(), "http request") || strings.Contains(logs.String(), userMarker) {
		t.Fatalf("logs:\n%s", logs.String())
	}
}
