// Package backend implements the approved Backend management HTTP adapter.
package backend

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/mapherez/nox-gateway/internal/transport"
	noxmcp "github.com/mapherez/nox-mcp/go"
)

type Failure struct {
	Code      string
	Retryable bool
	Denied    bool
}

func (e *Failure) Error() string   { return e.Code }
func (e *Failure) Temporary() bool { return e.Retryable }

type Client struct {
	origin    string
	transport *transport.Client
	slots     chan struct{}
}

func New(origin string, t *transport.Client) *Client {
	return &Client{origin: origin, transport: t, slots: make(chan struct{}, 8)}
}
func (c *Client) Close() { c.transport.Close() }

// Request never returns/logs untrusted response error text or raw network errors.
// Callers durably journal every mutation key/body before sending it.
func (c *Client) Request(ctx context.Context, method, path, token, key, revision string, body any) ([]byte, int, error) {
	if !strings.HasPrefix(path, "/api/nox/") || strings.ContainsAny(path, "?#\r\n") || strings.ContainsAny(token, "\r\n") {
		return nil, 0, &Failure{Code: "INVALID_BACKEND_REQUEST"}
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	select {
	case c.slots <- struct{}{}:
		defer func() { <-c.slots }()
	case <-ctx.Done():
		return nil, 0, &Failure{Code: "BACKEND_UNAVAILABLE", Retryable: true}
	}
	var data []byte
	var err error
	if body != nil {
		data, err = json.Marshal(body)
		if err != nil || len(data) > noxmcp.SuiteMaxPayloadBytes {
			return nil, 0, &Failure{Code: "INVALID_BACKEND_REQUEST"}
		}
	}
	request, err := http.NewRequestWithContext(ctx, method, c.origin+path, bytes.NewReader(data))
	if err != nil {
		return nil, 0, &Failure{Code: "INVALID_BACKEND_REQUEST"}
	}
	// Recovery is owned by the durable state machine. Suppress net/http's
	// implicit Idempotency-Key replay so every failed attempt returns to it.
	if method != "GET" && method != "HEAD" {
		request.GetBody = nil
		if request.Body == nil || request.Body == http.NoBody {
			request.Body = io.NopCloser(bytes.NewReader(nil))
		}
	}
	request.Header.Set("Accept", "application/json")
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	if key != "" {
		request.Header.Set("Idempotency-Key", key)
	}
	if revision != "" {
		request.Header.Set("If-Match", `"`+revision+`"`)
	}
	response, err := c.transport.HTTP.Do(request)
	if err != nil {
		if errors.Is(err, transport.ErrRedirect) {
			return nil, 0, &Failure{Code: "BACKEND_REDIRECT_REJECTED"}
		}
		if errors.Is(err, transport.ErrTarget) {
			return nil, 0, &Failure{Code: "BACKEND_TARGET_NOT_ALLOWED"}
		}
		return nil, 0, &Failure{Code: "BACKEND_UNAVAILABLE", Retryable: true}
	}
	defer response.Body.Close()
	if response.StatusCode == 401 || response.StatusCode == 403 {
		return nil, response.StatusCode, &Failure{Code: "BACKEND_AUTHORIZATION_DENIED", Denied: true}
	}
	if response.StatusCode >= 500 || response.StatusCode == 408 || response.StatusCode == 429 {
		return nil, response.StatusCode, &Failure{Code: "BACKEND_UNAVAILABLE", Retryable: true}
	}
	if response.StatusCode >= 300 {
		return nil, response.StatusCode, &Failure{Code: "BACKEND_OPERATION_REJECTED"}
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, noxmcp.SuiteMaxPayloadBytes+1))
	if err != nil {
		return nil, response.StatusCode, &Failure{Code: "BACKEND_UNAVAILABLE", Retryable: true}
	}
	if len(raw) > noxmcp.SuiteMaxPayloadBytes {
		return nil, response.StatusCode, &Failure{Code: "INVALID_BACKEND_RESPONSE"}
	}
	content, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || content != "application/json" {
		return nil, response.StatusCode, &Failure{Code: "INVALID_BACKEND_RESPONSE"}
	}
	if _, err = noxmcp.ParseSuiteJSON(raw); err != nil {
		return nil, response.StatusCode, &Failure{Code: "INVALID_BACKEND_RESPONSE"}
	}
	return raw, response.StatusCode, nil
}

func Resource(id string) string { return url.PathEscape(id) }
