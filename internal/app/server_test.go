package app

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func testServer() *Server {
	s, err := New("test-build", slog.New(slog.NewJSONHandler(io.Discard, nil)), Options{})
	if err != nil {
		panic(err)
	}
	return s
}

func TestDiagnosticsDoNotClaimIntegratedReadiness(t *testing.T) {
	s := testServer()
	for _, tc := range []struct {
		method, path string
		status       int
		contains     string
	}{
		{"GET", "/healthz", 200, `"productVersion":"test-build"`},
		{"GET", "/readyz", 503, `"code":"GATEWAY_NOT_CONFIGURED"`},
		{"HEAD", "/healthz", 200, ""}, {"HEAD", "/readyz", 503, ""},
		{"POST", "/healthz", 405, `"code":"METHOD_NOT_ALLOWED"`},
		{"GET", "/mcp?api_key=PRIVATE_KEY", 404, `"code":"NOT_FOUND"`},
		{"GET", "/nox/integration", 200, `"productId":"nox-gateway"`},
		{"GET", "/debug/pprof/", 404, `"code":"NOT_FOUND"`},
	} {
		t.Run(tc.method+tc.path, func(t *testing.T) {
			r := httptest.NewRequest(tc.method, tc.path, nil)
			r.Header.Set("Authorization", "Bearer PRIVATE_KEY")
			w := httptest.NewRecorder()
			s.http.Handler.ServeHTTP(w, r)
			if w.Code != tc.status || !strings.Contains(w.Body.String(), tc.contains) || strings.Contains(w.Body.String(), "PRIVATE_KEY") {
				t.Fatalf("unexpected response: %d %s", w.Code, w.Body.String())
			}
			if tc.method == "HEAD" && w.Body.Len() != 0 {
				t.Fatal("HEAD returned a body")
			}
			if tc.status == 405 && w.Header().Get("Allow") != "GET, HEAD" {
				t.Fatal("missing Allow")
			}
			if w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("X-Content-Type-Options") != "nosniff" {
				t.Fatal("missing response protections")
			}
		})
	}
}

func listen(t *testing.T) net.Listener {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	return l
}

func await[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case value := <-ch:
		return value
	case <-time.After(5 * time.Second):
		t.Fatal("operation did not finish within test deadline")
		var zero T
		return zero
	}
}

func TestShutdownCancelsActiveRequestAndDrains(t *testing.T) {
	s := testServer()
	started, requestCancelled := make(chan struct{}), make(chan struct{})
	s.http.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-r.Context().Done()
		close(requestCancelled)
		w.WriteHeader(http.StatusNoContent)
	})
	l := listen(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	served := make(chan error, 1)
	go func() { served <- s.Serve(ctx, l, time.Second) }()
	request := make(chan error, 1)
	go func() {
		client := &http.Client{Timeout: 3 * time.Second}
		r, err := client.Get("http://" + l.Addr().String() + "/active")
		if err == nil {
			r.Body.Close()
			if r.StatusCode != 204 {
				err = errors.New("active request did not drain")
			}
		}
		request <- err
	}()
	await(t, started)
	cancel()
	await(t, requestCancelled)
	if err := await(t, served); err != nil {
		t.Fatal(err)
	}
	if err := await(t, request); err != nil {
		t.Fatal(err)
	}
	if !s.stopping.Load() {
		t.Fatal("shutdown was not marked")
	}
	w := httptest.NewRecorder()
	s.routes("test-build").ServeHTTP(w, httptest.NewRequest("GET", "/readyz", nil))
	if w.Code != 503 || !strings.Contains(w.Body.String(), "SHUTTING_DOWN") {
		t.Fatal("incorrect draining readiness")
	}
}

func TestShutdownDeadlineBoundsUncooperativeHandler(t *testing.T) {
	s := testServer()
	started, release := make(chan struct{}), make(chan struct{})
	defer close(release)
	s.http.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { close(started); <-release })
	l := listen(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	served := make(chan error, 1)
	go func() { served <- s.Serve(ctx, l, 30*time.Millisecond) }()
	request := make(chan error, 1)
	go func() {
		r, err := (&http.Client{Timeout: 3 * time.Second}).Get("http://" + l.Addr().String() + "/active")
		if r != nil {
			r.Body.Close()
		}
		request <- err
	}()
	await(t, started)
	cancel()
	if err := await(t, served); !errors.Is(err, ErrShutdown) {
		t.Fatalf("expected bounded failed drain: %v", err)
	}
	if err := await(t, request); err == nil {
		t.Fatal("uncooperative request connection was not closed")
	}
}

func TestCancelledStartupAndListenerFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	l := listen(t)
	if err := testServer().Serve(ctx, l, time.Second); err != nil {
		t.Fatal(err)
	}
	if c, err := net.DialTimeout("tcp", l.Addr().String(), time.Second); err == nil {
		c.Close()
		t.Fatal("cancelled startup left listener open")
	}
	l = listen(t)
	_ = l.Close()
	if err := testServer().Serve(t.Context(), l, time.Second); !errors.Is(err, ErrServe) {
		t.Fatalf("listener failure was hidden: %v", err)
	}
}

func TestPairingCannotBeInvokedThroughNativeTCP(t *testing.T) {
	s := testServer()
	r := httptest.NewRequest("POST", "http://nox-gateway/nox/integration/pairing", strings.NewReader(`{}`))
	r.Header.Set("X-Forwarded-Proto", "https")
	w := httptest.NewRecorder()
	s.http.Handler.ServeHTTP(w, r)
	if w.Code != 404 {
		t.Fatal("network pairing was exposed", w.Code)
	}
}
