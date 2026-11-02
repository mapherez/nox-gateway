package app

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"time"

	noxmcp "github.com/mapherez/nox-mcp/go"
)

// LocalHandler is mounted only on the owned Unix listener, never TCP.
func (s *Server) LocalHandler(receive func(context.Context, noxmcp.SuitePairingIntent) error) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		if r.Host != "nox-gateway" {
			writeJSON(w, r, 400, map[string]string{"code": "INVALID_DESTINATION"})
			return
		}
		if r.URL.Path == "/nox/integration" && r.Method == "GET" {
			writeJSON(w, r, 200, s.declaration)
			return
		}
		if r.URL.Path != "/nox/integration/pairing" {
			writeJSON(w, r, 404, map[string]string{"code": "NOT_FOUND"})
			return
		}
		if r.Method != "POST" {
			w.Header().Set("Allow", "POST")
			writeJSON(w, r, 405, map[string]string{"code": "METHOD_NOT_ALLOWED"})
			return
		}
		if r.URL.RawQuery != "" || r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" || r.Header.Get("Idempotency-Key") == "" {
			writeJSON(w, r, 400, map[string]string{"code": "INVALID_PAIRING"})
			return
		}
		_ = http.NewResponseController(w).SetReadDeadline(time.Now().Add(20 * time.Second))
		raw, e := io.ReadAll(http.MaxBytesReader(w, r.Body, noxmcp.SuiteMaxPayloadBytes))
		r.Body.Close()
		var delivery struct {
			ContractVersion string                    `json:"contractVersion"`
			BackendOrigin   string                    `json:"backendOrigin"`
			Intent          noxmcp.SuitePairingIntent `json:"intent"`
		}
		if e != nil {
			writeJSON(w, r, 413, map[string]string{"code": "PAYLOAD_LIMIT"})
			return
		}
		if _, e = noxmcp.ParseSuiteJSON(raw); e != nil || json.Unmarshal(raw, &delivery) != nil || delivery.ContractVersion != "1.0" || delivery.BackendOrigin != "http://nox-backend" {
			writeJSON(w, r, 400, map[string]string{"code": "INVALID_PAIRING"})
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
		defer cancel()
		if e = receive(ctx, delivery.Intent); e != nil {
			writeJSON(w, r, 503, map[string]string{"code": "PAIRING_RECONCILIATION_REQUIRED"})
			return
		}
		writeJSON(w, r, 200, map[string]bool{"accepted": true})
	})
}
