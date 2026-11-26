// Package api holds the Hub's HTTP surface.
package api

import (
	"encoding/json"
	"net/http"
)

// RegisterHealth installs GET /health (story 001 AC5).
//
// It also reports the authentication mode. A deployment check must be able to
// answer "is this Hub gated" with one curl and no credential — otherwise the one
// setting that most needs verifying is the one that cannot be seen from outside
// (ADR-0009 §2.1).
func RegisterHealth(mux *http.ServeMux, srv *Server) {
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		auth := "disabled"
		if srv != nil && srv.authenticated() {
			auth = "enabled"
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"status":  "ok",
			"service": "fluxspace-hub",
			"auth":    auth,
		})
	})
}
