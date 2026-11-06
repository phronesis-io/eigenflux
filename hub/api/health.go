// Package api holds the Hub's HTTP surface.
package api

import (
	"encoding/json"
	"net/http"
)

// RegisterHealth installs GET /health (story 001 AC5).
func RegisterHealth(mux *http.ServeMux) {
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"status":  "ok",
			"service": "fluxspace-hub",
		})
	})
}
