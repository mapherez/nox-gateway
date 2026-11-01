// Package app owns HTTP diagnostics and the process's request lifecycle.
package app

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/mapherez/nox-gateway/internal/buildinfo"
	"github.com/mapherez/nox-gateway/internal/logging"
)

var (
	ErrServe    = errors.New("HTTP listener stopped unexpectedly")
	ErrShutdown = errors.New("graceful shutdown exceeded its configured deadline")
)

type Server struct {
	http     *http.Server
	stopping atomic.Bool
}

func New(version string, logger *slog.Logger) *Server {
	s := &Server{}
	s.http = &http.Server{
		Handler:           s.routes(version),
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    32 << 10,
		ErrorLog:          logging.HTTPErrorLog(logger),
	}
	return s
}

func (s *Server) routes(version string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if r.URL.Path != "/healthz" && r.URL.Path != "/readyz" {
			writeJSON(w, r, http.StatusNotFound, map[string]string{"code": "NOT_FOUND"})
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			writeJSON(w, r, http.StatusMethodNotAllowed, map[string]string{"code": "METHOD_NOT_ALLOWED"})
			return
		}
		if r.URL.Path == "/healthz" {
			writeJSON(w, r, http.StatusOK, map[string]string{
				"status": "ok", "productId": buildinfo.ProductID,
				"productVersion": version, "contractVersion": buildinfo.ContractVersion,
			})
			return
		}
		code := "GATEWAY_NOT_CONFIGURED"
		if s.stopping.Load() {
			code = "SHUTTING_DOWN"
		}
		// Point 1 cannot admit integrated traffic. Liveness is not MCP readiness.
		writeJSON(w, r, http.StatusServiceUnavailable, map[string]string{"status": "notReady", "code": code})
	})
}

func writeJSON(w http.ResponseWriter, r *http.Request, status int, body any) {
	w.WriteHeader(status)
	if r.Method != http.MethodHead {
		_ = json.NewEncoder(w).Encode(body)
	}
}

// Serve owns listener until shutdown. Cancellation reaches active requests;
// Shutdown drains handlers, then Close bounds a handler that ignores cancellation.
func (s *Server) Serve(ctx context.Context, listener net.Listener, timeout time.Duration) error {
	if ctx.Err() != nil {
		_ = listener.Close()
		return nil
	}
	defer s.http.Close()
	requests, cancelRequests := context.WithCancel(context.Background())
	defer cancelRequests()
	s.http.BaseContext = func(net.Listener) context.Context { return requests }
	served := make(chan error, 1)
	go func() { served <- s.http.Serve(listener) }()
	select {
	case err := <-served:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return ErrServe
	case <-ctx.Done():
		s.stopping.Store(true)
		cancelRequests()
		shutdown, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		if err := s.http.Shutdown(shutdown); err != nil {
			_ = s.http.Close()
			<-served
			return ErrShutdown
		}
		<-served
		return nil
	}
}
