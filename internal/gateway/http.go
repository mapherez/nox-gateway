package gateway

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mapherez/nox-gateway/internal/backend"

	noxmcp "github.com/mapherez/nox-mcp/go"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func publicFailure(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{"code": code})
}
func (g *Gateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	if r.URL.RawQuery != "" {
		publicFailure(w, 400, "INVALID_REQUEST")
		return
	}
	auth := strings.Fields(r.Header.Get("Authorization"))
	if len(r.Header.Values("Authorization")) != 1 || len(auth) != 2 || !strings.EqualFold(auth[0], "Bearer") || !strings.HasPrefix(auth[1], "noxsync_") {
		publicFailure(w, 401, "UNAUTHENTICATED")
		return
	}
	if r.Method != "POST" {
		w.Header().Set("Allow", "POST")
		publicFailure(w, 405, "METHOD_NOT_ALLOWED")
		return
	}
	select {
	case g.slots <- struct{}{}:
		defer func() { <-g.slots }()
	default:
		publicFailure(w, 503, "CAPACITY_LIMIT")
		return
	}
	_ = http.NewResponseController(w).SetReadDeadline(time.Now().Add(g.limits.CallTimeout))
	ctx, cancel := context.WithTimeout(r.Context(), g.limits.CallTimeout)
	defer cancel()
	raw, e := io.ReadAll(http.MaxBytesReader(w, r.Body, int64(g.limits.MaxPayloadBytes)))
	r.Body.Close()
	if e != nil {
		publicFailure(w, 413, "PAYLOAD_LIMIT")
		return
	}
	if _, e = noxmcp.ParseSuiteJSON(raw); e != nil {
		publicFailure(w, 400, "INVALID_REQUEST")
		return
	}
	var message struct {
		Method string          `json:"method"`
		ID     json.RawMessage `json:"id"`
		Params struct {
			RequestID json.RawMessage `json:"requestId"`
		} `json:"params"`
	}
	json.Unmarshal(raw, &message)
	partition, e := noxmcp.SuiteCredentialPartition(auth[1], g.secret)
	if e != nil {
		publicFailure(w, 401, "UNAUTHENTICATED")
		return
	}
	if message.Method == "notifications/cancelled" {
		g.mu.RLock()
		prefix := partition + ":" + string(message.Params.RequestID) + ":"
		for id, stop := range g.active {
			if strings.HasPrefix(id, prefix) {
				stop()
			}
		}
		g.mu.RUnlock()
		w.WriteHeader(202)
		return
	}
	if len(message.ID) > 256 {
		publicFailure(w, 400, "INVALID_REQUEST")
		return
	}
	activeKey := partition + ":" + string(message.ID) + ":" + requestID()
	g.mu.Lock()
	g.active[activeKey] = cancel
	g.mu.Unlock()
	defer func() { g.mu.Lock(); delete(g.active, activeKey); g.mu.Unlock() }()
	if !g.Ready() {
		publicFailure(w, 503, "GATEWAY_NOT_READY")
		return
	}
	controls := noxmcp.SuiteRequestControls{RequestID: r.Header.Get("Nox-Request-Id"), DeadlineAt: r.Header.Get("Nox-Deadline-At")}
	if controls.RequestID == "" && controls.DeadlineAt == "" && r.Header.Get("Nox-Timeout-Ms") == "" {
		controls = noxmcp.SuiteRequestControls{RequestID: requestID(), DeadlineAt: deadline(ctx).UTC().Format("2006-01-02T15:04:05.000Z"), TimeoutMS: g.limits.CallTimeout.Milliseconds()}
	} else {
		if json.Unmarshal([]byte(r.Header.Get("Nox-Timeout-Ms")), &controls.TimeoutMS) != nil {
			publicFailure(w, 400, "INVALID_REQUEST_CONTROLS")
			return
		}
	}
	controls, e = noxmcp.SuiteRequestBudget(controls, time.Now(), g.limits.CallTimeout, 5*time.Second)
	if e != nil {
		publicFailure(w, 400, "INVALID_REQUEST_CONTROLS")
		return
	}
	shortDeadline, _ := noxmcp.ParseSuiteTimestamp(controls.DeadlineAt)
	ctx, stopBudget := context.WithDeadline(ctx, shortDeadline)
	defer stopBudget()
	snapshot, _, e := g.catalog(ctx, auth[1], partition)
	if e != nil {
		var denied *backend.Failure
		if errors.As(e, &denied) && denied.Denied {
			publicFailure(w, 401, "UNAUTHENTICATED")
		} else {
			publicFailure(w, 503, "AUTHORIZATION_UNAVAILABLE")
		}
		return
	}
	caps := &mcp.ServerCapabilities{Tools: &mcp.ToolCapabilities{}, Logging: &mcp.LoggingCapabilities{}}
	caps.AddExtension(noxmcp.DiscoveryExtension, noxmcp.DiscoveryCapability())
	caps.AddExtension("nox/gateway", map[string]any{"version": "1.0"})
	server := mcp.NewServer(&mcp.Implementation{Name: "nox-gateway", Version: g.version}, &mcp.ServerOptions{Capabilities: caps, SupportedProtocolVersions: []string{"2025-11-25", "2026-07-28"}})
	descriptors := map[string]noxmcp.JSON{}
	unavailable := []string{}
	for _, definition := range snapshot.Definitions {
		encoded, _ := json.Marshal(definition.Tool)
		var tool mcp.Tool
		if json.Unmarshal(encoded, &tool) != nil {
			publicFailure(w, 503, "INVALID_GATEWAY")
			return
		}
		if definition.Cli != nil {
			b, _ := json.Marshal(definition.Cli)
			var cli noxmcp.JSON
			json.Unmarshal(b, &cli)
			descriptors[tool.Name] = cli
		}
		if definition.Availability != nil && *definition.Availability == "unavailable" {
			unavailable = append(unavailable, tool.Name)
		}
		alias := tool.Name
		server.AddTool(&tool, func(callCtx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			// The MCP server may detach method contexts from the HTTP lifetime.
			// Preserve cancellation, invalidation and the original total deadline.
			callCtx, cancelCall := context.WithDeadline(callCtx, deadline(ctx))
			stop := context.AfterFunc(ctx, cancelCall)
			defer func() { stop(); cancelCall() }()
			result, e := g.dispatch(callCtx, auth[1], partition, snapshot, alias, req.Params, controls)
			if e != nil {
				return callFailure(e), nil
			}
			return result, nil
		})
	}
	presentationRaw, _ := json.Marshal(snapshot.Catalog.Presentation)
	var presentation noxmcp.JSON
	json.Unmarshal(presentationRaw, &presentation)
	// Keep definition hashes request-private and bound their advertised TTL by
	// the verified grant. The server ID remains persistent and deterministic.
	discovery, _ := noxmcp.NewDiscovery(g.serverID)
	discovery.TTLMS = int(snapshot.Catalog.TtlMs)
	discovery.MaxBytes = g.limits.MaxPayloadBytes
	if e = noxmcp.InstallDiscovery(server, noxmcp.DiscoveryOptions{ServerID: g.serverID, Service: discovery, Descriptors: descriptors, ResolveView: func(context.Context, *mcp.RequestExtra, []*mcp.Tool) (string, []string, []string, noxmcp.JSON, error) {
		return snapshot.ViewID, nil, unavailable, presentation, nil
	}}); e != nil {
		publicFailure(w, 503, "INVALID_GATEWAY")
		return
	}
	server.AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(c context.Context, method string, request mcp.Request) (mcp.Result, error) {
			result, err := next(c, method, request)
			if list, ok := result.(*mcp.ListToolsResult); ok {
				list.CacheScope = "private"
				list.TTLMs = int(snapshot.Catalog.TtlMs)
			}
			return result, err
		}
	})
	req := r.Clone(ctx)
	req.Body = io.NopCloser(bytes.NewReader(raw))
	req.ContentLength = int64(len(raw))
	response := &boundedResponse{header: w.Header().Clone(), limit: g.limits.MaxPayloadBytes}
	mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true}).ServeHTTP(response, req)
	if response.exceeded {
		publicFailure(w, 503, "PAYLOAD_LIMIT")
		return
	}
	if response.body.Len() == 0 && ctx.Err() != nil {
		if message.Method == "tools/call" && len(message.ID) > 0 {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": message.ID, "result": callFailure(errors.New("EXECUTION_OUTCOME_UNCONFIRMED"))})
		} else {
			publicFailure(w, 503, "REQUEST_CANCELLED")
		}
		return
	}
	for key, values := range response.header {
		w.Header()[key] = values
	}
	status := response.status
	if status == 0 {
		status = 200
	}
	w.WriteHeader(status)
	_, _ = w.Write(response.body.Bytes())
}
func (g *Gateway) catalog(ctx context.Context, key, partition string) (noxmcp.SuiteGatewaySnapshot, map[string]*noxmcp.VerifiedSuiteIdentity, error) {
	visible, e := g.visible(ctx, key, partition)
	if e != nil {
		return noxmcp.SuiteGatewaySnapshot{}, nil, e
	}
	g.mu.RLock()
	registry := g.state.Registry
	generation := g.generation
	g.mu.RUnlock()
	sources := []noxmcp.SuiteGatewaySource{}
	identities := map[string]*noxmcp.VerifiedSuiteIdentity{}
	expires := time.Now().Add(5 * time.Minute)
	views := []string{}
	type loaded struct {
		id     *noxmcp.VerifiedSuiteIdentity
		view   string
		source *noxmcp.SuiteGatewaySource
	}
	loadedProducts := make([]loaded, len(registry.Products))
	var work sync.WaitGroup
	var catalogBytes atomic.Int64
	var oversized atomic.Bool
	limit := make(chan struct{}, 4)
	for i, r := range registry.Products {
		if !visible[r.ProductID] || r.ProductID == "nox-gateway" || r.Declaration.Capabilities.SuiteMcp == nil {
			continue
		}
		select {
		case limit <- struct{}{}:
		case <-ctx.Done():
			work.Wait()
			return noxmcp.SuiteGatewaySnapshot{}, nil, ctx.Err()
		}
		work.Add(1)
		go func(i int, r noxmcp.SuiteRegistrationRecord) {
			defer work.Done()
			defer func() { <-limit }()
			id, e := g.authorize(ctx, key, r, "user")
			if e != nil {
				return
			}
			view, e := noxmcp.SuiteAuthorizationViewKey(id, partition, "1.0")
			if e != nil {
				return
			}
			loadedProducts[i] = loaded{id: id, view: view}
			source, e := g.source(ctx, partition, r, id, false)
			if e == nil {
				encoded, err := json.Marshal(source)
				if err == nil && catalogBytes.Add(int64(len(encoded))) <= noxmcp.SuiteMaxGatewaySnapshotBytes {
					loadedProducts[i].source = &source
				} else {
					oversized.Store(true)
				}
			}
		}(i, r)
	}
	work.Wait()
	if oversized.Load() {
		return noxmcp.SuiteGatewaySnapshot{}, nil, errors.New("PAYLOAD_LIMIT")
	}
	for i, entry := range loadedProducts {
		if entry.id == nil {
			continue
		}
		views = append(views, entry.view)
		identities[registry.Products[i].ProductID] = entry.id
		expires = minTime(expires, entry.id.ExpiresAt())
		if entry.source != nil {
			sources = append(sources, *entry.source)
		}
	}
	view, e := noxmcp.Fingerprint([]any{partition, views})
	if e != nil {
		return noxmcp.SuiteGatewaySnapshot{}, nil, e
	}
	snapshot, e := noxmcp.ComposeSuiteGateway(noxmcp.SuiteGatewayOptions{ServerID: g.serverID, AuthorizationViewID: view, RegistryRevision: registry.RegistryRevision, PolicyRevision: "1.0", Sources: sources, NowMS: time.Now().UnixMilli(), AuthorizationExpiresAtMS: expires.UnixMilli()})
	if e != nil {
		return snapshot, nil, e
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if generation != g.generation {
		return snapshot, nil, errors.New("AUTHORIZATION_INVALIDATED")
	}
	if len(identities) > 0 {
		if len(g.admissions) >= 128 {
			g.admissions = map[string]admission{}
		}
		g.admissions[partition] = admission{products: visible, expires: expires}
	}
	return snapshot, identities, nil
}
func (g *Gateway) dispatch(ctx context.Context, key, partition string, snapshot noxmcp.SuiteGatewaySnapshot, alias string, params *mcp.CallToolParamsRaw, controls noxmcp.SuiteRequestControls) (*mcp.CallToolResult, error) {
	route, e := noxmcp.SelectSuiteRoute(snapshot, alias, "")
	if e != nil {
		return nil, e
	}
	r, generation, e := g.record(route.ProductID)
	if e != nil || r.Revision != route.RegistrationRevision || r.AssociationID != route.AssociationID {
		return nil, errors.New("STALE_DEFINITION")
	}
	id, e := g.authorize(ctx, key, r, "user")
	if e != nil {
		return nil, e
	}
	fresh, e := g.source(ctx, partition, r, id, true)
	if e != nil {
		return nil, e
	}
	var original *noxmcp.SuiteSourceDefinition
	for i := range fresh.Definitions {
		if fresh.Definitions[i].Tool.Name == route.ToolName {
			original = &fresh.Definitions[i]
			break
		}
	}
	if original == nil {
		return nil, errors.New("FORBIDDEN")
	}
	bundle := map[string]any{"tool": original.Tool}
	if original.Cli != nil {
		bundle["cli"] = original.Cli
	}
	encoded, _ := json.Marshal(bundle)
	value, e := noxmcp.ParseSuiteJSON(encoded)
	if e != nil {
		return nil, e
	}
	fingerprint, e := noxmcp.Fingerprint(value)
	if e != nil || fingerprint != route.SourceFingerprint || fresh.ServerID != route.ServerID || fresh.ViewID != route.ViewID {
		return nil, errors.New("STALE_DEFINITION")
	}
	if e = noxmcp.RequireSuiteMethod(id, "tools/call", time.Now()); e != nil {
		return nil, e
	}
	controls, e = noxmcp.SuiteRequestBudget(controls, time.Now(), g.limits.CallTimeout, 0)
	if e != nil {
		return nil, e
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(controls.TimeoutMS)*time.Millisecond)
	defer cancel()
	g.mu.RLock()
	current := g.generation
	g.mu.RUnlock()
	if current != generation || ctx.Err() != nil {
		return nil, errors.New("AUTHORIZATION_INVALIDATED")
	}
	wire := &callResponse{}
	session, closeSession, e := g.connect(ctx, r, id, &controls, wire)
	if e != nil {
		return nil, e
	}
	defer closeSession()
	meta := mcp.Meta{}
	for k, v := range params.Meta {
		// Protocol identity/capabilities are hop-local. The outgoing MCP client
		// supplies its own negotiated values, never the caller's assertions.
		if k != mcp.MetaKeyProtocolVersion && k != mcp.MetaKeyClientCapabilities && k != mcp.MetaKeyClientInfo {
			meta[k] = v
		}
	}
	if _, ok := meta[noxmcp.DiscoveryExtension]; ok {
		capabilities := session.InitializeResult().Capabilities
		advanced := false
		if capabilities != nil {
			_, advanced = capabilities.Extensions[noxmcp.DiscoveryExtension]
		}
		if advanced {
			meta[noxmcp.DiscoveryExtension] = map[string]any{"expectedFingerprint": route.SourceFingerprint}
		} else {
			delete(meta, noxmcp.DiscoveryExtension)
		}
	}
	result, e := session.CallTool(ctx, &mcp.CallToolParams{Name: route.ToolName, Arguments: params.Arguments, Meta: meta})
	if e != nil || wire.restore(result) != nil {
		return nil, errors.New("EXECUTION_OUTCOME_UNCONFIRMED")
	}
	b, e := json.Marshal(result)
	if e != nil || len(b) > g.limits.MaxPayloadBytes {
		return nil, errors.New("PAYLOAD_LIMIT")
	}
	return result, nil
}
func callFailure(e error) *mcp.CallToolResult {
	code := "GATEWAY_OPERATION_FAILED"
	var contract *noxmcp.SuiteError
	if errors.As(e, &contract) {
		code = contract.Code
	} else {
		switch e.Error() {
		case "STALE_DEFINITION", "FORBIDDEN", "AUTHORIZATION_INVALIDATED", "EXECUTION_OUTCOME_UNCONFIRMED", "PRODUCT_UNREACHABLE", "PAYLOAD_LIMIT":
			code = e.Error()
		}
	}
	value := map[string]any{"code": code, "message": "The Gateway could not complete this operation.", "retryable": false}
	if code == "EXECUTION_OUTCOME_UNCONFIRMED" {
		value["details"] = map[string]any{"outcome": "unknown"}
	}
	b, _ := json.Marshal(value)
	return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: string(b)}}}
}
func requestID() string {
	b := make([]byte, 16)
	rand.Read(b)
	b[6] = (b[6] & 15) | 64
	b[8] = (b[8] & 63) | 128
	v := hex.EncodeToString(b)
	return v[:8] + "-" + v[8:12] + "-" + v[12:16] + "-" + v[16:20] + "-" + v[20:]
}

func deadline(ctx context.Context) time.Time { v, _ := ctx.Deadline(); return v }

// JSON exchange envelopes are bounded independently of tool result bodies. No
// subscription/SSE stream is advertised or buffered by this client endpoint.
type boundedResponse struct {
	header        http.Header
	status, limit int
	body          bytes.Buffer
	exceeded      bool
}

func (w *boundedResponse) Header() http.Header { return w.header }
func (w *boundedResponse) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
}
func (w *boundedResponse) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.status = 200
	}
	if w.exceeded || len(p) > w.limit-w.body.Len() {
		w.exceeded = true
		return 0, errors.New("PAYLOAD_LIMIT")
	}
	return w.body.Write(p)
}
func (w *boundedResponse) Flush() {
	if w.status == 0 {
		w.status = 200
	}
}
