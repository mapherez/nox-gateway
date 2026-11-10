package gateway

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// callResponse belongs to one dispatch session. The MCP client decodes arbitrary
// JSON into float64; retain only this call's wire result to restore exact numbers.
// Nothing is cached, persisted or shared with another user or product.
type callResponse struct {
	mu  sync.Mutex
	raw json.RawMessage
}

func (c *callResponse) request(r *http.Request, limit int) (json.RawMessage, error) {
	if c == nil || r.Method != http.MethodPost || r.Body == nil {
		return nil, nil
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, int64(limit)+1))
	r.Body.Close()
	if err != nil || len(raw) > limit {
		return nil, errors.New("PAYLOAD_LIMIT")
	}
	r.Body = io.NopCloser(bytes.NewReader(raw))
	var request struct {
		Method string          `json:"method"`
		ID     json.RawMessage `json:"id"`
	}
	if err := json.Unmarshal(raw, &request); err != nil {
		return nil, err
	}
	if request.Method != "tools/call" {
		return nil, nil
	}
	return request.ID, nil
}

func (c *callResponse) receive(id json.RawMessage, raw []byte) {
	if c == nil || len(id) == 0 {
		return
	}
	var envelope struct {
		ID     json.RawMessage `json:"id"`
		Result json.RawMessage `json:"result"`
	}
	if json.Unmarshal(raw, &envelope) != nil || !bytes.Equal(id, envelope.ID) {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.raw = envelope.Result
}

func (c *callResponse) restore(result *mcp.CallToolResult) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if result == nil || len(c.raw) == 0 || bytes.Equal(c.raw, []byte("null")) {
		return errors.New("missing JSON tool response")
	}
	var wire struct {
		Structured any      `json:"structuredContent"`
		Meta       mcp.Meta `json:"_meta"`
	}
	decoder := json.NewDecoder(bytes.NewReader(c.raw))
	decoder.UseNumber()
	if err := decoder.Decode(&wire); err != nil {
		return err
	}
	result.StructuredContent = wire.Structured
	result.Meta = wire.Meta
	return nil
}
