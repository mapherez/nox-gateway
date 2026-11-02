// Package app owns HTTP diagnostics and the process's request lifecycle.
package app

import (
	"context"
	"crypto/tls"
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

type Options struct {
	PublicURL        string
	TLSConfig        *tls.Config
	MCPHandler       http.Handler
	Ready            func() bool
	ConnectionStatus func() string
}

type Server struct {
	options     Options
	declaration json.RawMessage
	http        *http.Server
	stopping    atomic.Bool
}

func New(version string, logger *slog.Logger, options Options) (*Server, error) {
	declaration, err := Declaration(version)
	if err != nil {
		return nil, errors.New("invalid Gateway build declaration")
	}
	s := &Server{options: options, declaration: declaration}
	s.http = &http.Server{
		Handler:           s.routes(version),
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    32 << 10,
		ErrorLog:          logging.HTTPErrorLog(logger),
		TLSConfig:         options.TLSConfig,
	}
	return s, nil
}

func (s *Server) routes(version string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if r.URL.Path == "/mcp" && s.options.MCPHandler != nil {
			if origin := r.Header.Get("Origin"); origin != "" {
				expected := s.options.PublicURL
				if expected == "" {
					scheme := "http"
					if r.TLS != nil {
						scheme = "https"
					}
					expected = scheme + "://" + r.Host
				}
				if origin != expected {
					writeJSON(w, r, 403, map[string]string{"code": "FORBIDDEN"})
					return
				}
			}
			s.options.MCPHandler.ServeHTTP(w, r)
			return
		}
		if r.URL.Path != "/healthz" && r.URL.Path != "/readyz" && r.URL.Path != "/nox/integration" {
			writeJSON(w, r, http.StatusNotFound, map[string]string{"code": "NOT_FOUND"})
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			writeJSON(w, r, http.StatusMethodNotAllowed, map[string]string{"code": "METHOD_NOT_ALLOWED"})
			return
		}
		if r.URL.Path == "/nox/integration" {
			writeJSON(w, r, http.StatusOK, s.declaration)
			return
		}
		if r.URL.Path == "/healthz" {
			writeJSON(w, r, http.StatusOK, map[string]string{
				"status": "ok", "productId": buildinfo.ProductID,
				"productVersion": version, "contractVersion": buildinfo.ContractVersion,
			})
			return
		}
		if !s.stopping.Load() && s.options.Ready != nil && s.options.Ready() {
			writeJSON(w, r, 200, map[string]string{"status": "ready", "serviceConnection": s.connectionStatus()})
			return
		}
		code := "GATEWAY_NOT_CONFIGURED"
		if s.connectionStatus() != "notPaired" {
			code = "GATEWAY_NOT_READY"
		}
		if s.stopping.Load() {
			code = "SHUTTING_DOWN"
		}
		// Readiness requires live trust and the synchronized Backend registry.
		writeJSON(w, r, http.StatusServiceUnavailable, map[string]string{"status": "notReady", "code": code, "serviceConnection": s.connectionStatus()})
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
	go func() {
		if s.http.TLSConfig != nil {
			served <- s.http.ServeTLS(listener, "", "")
		} else {
			served <- s.http.Serve(listener)
		}
	}()
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

func (s *Server) connectionStatus() string {
	if s.options.ConnectionStatus == nil {
		return "notPaired"
	}
	return s.options.ConnectionStatus()
}
