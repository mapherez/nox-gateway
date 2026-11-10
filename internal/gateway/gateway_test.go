package gateway

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mapherez/nox-gateway/internal/backend"
	"github.com/mapherez/nox-gateway/internal/transport"
	"github.com/mapherez/nox-gateway/internal/trust"

	"github.com/google/jsonschema-go/jsonschema"
	noxmcp "github.com/mapherez/nox-mcp/go"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type fixtureAuthority struct {
	mu            sync.Mutex
	signer        *ecdsa.PrivateKey
	public        noxmcp.SuiteSigningKey
	registry      noxmcp.SuiteRegistrySnapshot
	unavailable   bool
	issued        int
	epoch         int
	expiredCursor bool
	page          []byte
	invalid       map[string]bool
}

func (f *fixtureAuthority) Authority() (noxmcp.SuiteProvisioning, trust.KeySet, error) {
	return noxmcp.SuiteProvisioning{Issuer: "urn:nox:fixture", ServiceCredential: "noxsvc_fixture", Service: noxmcp.SuiteServiceIdentity{ServiceID: "gateway-fixture", CredentialRevision: "credential-fixture"}}, trust.KeySet{Issuer: "urn:nox:fixture", Keys: []noxmcp.SuiteSigningKey{f.public}}, nil
}
func (f *fixtureAuthority) Reconcile(context.Context) error { return nil }
func (f *fixtureAuthority) HumanRegistry(_ context.Context, key string) ([]byte, int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.unavailable {
		return nil, 503, &backend.Failure{Code: "BACKEND_UNAVAILABLE", Retryable: true}
	}
	if !strings.HasPrefix(key, "noxsync_") || f.invalid[key] {
		return nil, 401, &backend.Failure{Code: "BACKEND_AUTHORIZATION_DENIED", Denied: true}
	}
	b, _ := json.Marshal(f.registry)
	return b, 200, nil
}
func (f *fixtureAuthority) Request(_ context.Context, method, path, key string, body any) ([]byte, int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.unavailable {
		return nil, 503, &backend.Failure{Code: "BACKEND_UNAVAILABLE", Retryable: true}
	}
	if method == "GET" && path == "/api/nox/products" {
		b, _ := json.Marshal(f.registry)
		return b, 200, nil
	}
	if strings.HasPrefix(path, "/api/nox/events?") {
		if f.expiredCursor {
			return nil, 410, &backend.Failure{Code: "CURSOR_EXPIRED"}
		}
		return f.page, 200, nil
	}
	if path != "/api/nox/authorizations" && path != "/api/nox/service-authorizations" {
		return nil, 404, errors.New("unexpected fixture operation")
	}
	in := body.(map[string]any)
	product := in["productId"].(string)
	var record noxmcp.SuiteRegistrationRecord
	for _, r := range f.registry.Products {
		if r.ProductID == product {
			record = r
		}
	}
	if record.ProductID == "" {
		return nil, 404, &backend.Failure{Code: "BACKEND_OPERATION_REJECTED"}
	}
	personal, _ := in["apiKey"].(string)
	if f.invalid[personal] {
		return nil, 401, &backend.Failure{Code: "BACKEND_AUTHORIZATION_DENIED", Denied: true}
	}
	scopes := []string{"nox:inspect"}
	user := "member"
	if personal == "noxsync_admin" {
		scopes = append(scopes, "nox:write")
		user = "admin"
	}
	now := time.Now().Truncate(time.Second)
	expires := now.Add(5 * time.Minute)
	f.issued++
	context := map[string]any{"contractVersion": "1.0", "userId": user, "keyId": "key-" + user, "authRevision": "auth-1", "gatewayServiceId": "gateway-fixture", "scopes": scopes, "serviceCredentialRevision": "credential-fixture", "productId": product, "registrationRevision": record.Revision, "associationId": record.AssociationID}
	claims := map[string]any{"iss": "urn:nox:fixture", "aud": record.Connection.BaseUrl + "/mcp", "sub": user, "act": map[string]string{"sub": "gateway-fixture"}, "iat": now.Unix(), "nbf": now.Unix(), "exp": expires.Unix(), "jti": "fixture-jti", "scope": strings.Join(scopes, " "), "nox": map[string]string{"contractVersion": "1.0", "keyId": "key-" + user, "authRevision": "auth-1", "productId": product, "registrationRevision": record.Revision, "associationId": record.AssociationID, "serviceCredentialRevision": "credential-fixture"}}
	header := map[string]string{"alg": "ES256", "typ": "nox-delegation+jwt", "kid": "fixture-key"}
	if path == "/api/nox/service-authorizations" {
		context = map[string]any{"contractVersion": "1.0", "serviceId": "gateway-fixture", "purpose": "mcp-probe", "serviceCredentialRevision": "credential-fixture", "productId": product, "registrationRevision": record.Revision, "associationId": record.AssociationID}
		claims["sub"] = "gateway-fixture"
		claims["scope"] = "nox:mcp-probe"
		delete(claims, "act")
		claims["nox"] = map[string]string{"contractVersion": "1.0", "purpose": "mcp-probe", "productId": product, "registrationRevision": record.Revision, "associationId": record.AssociationID, "serviceCredentialRevision": "credential-fixture"}
		header["typ"] = "nox-service+jwt"
	}
	h, _ := json.Marshal(header)
	c, _ := json.Marshal(claims)
	input := base64.RawURLEncoding.EncodeToString(h) + "." + base64.RawURLEncoding.EncodeToString(c)
	digest := sha256.Sum256([]byte(input))
	r, s, e := ecdsa.Sign(rand.Reader, f.signer, digest[:])
	if e != nil {
		return nil, 0, e
	}
	signature := append(r.FillBytes(make([]byte, 32)), s.FillBytes(make([]byte, 32))...)
	token := input + "." + base64.RawURLEncoding.EncodeToString(signature)
	b, _ := json.Marshal(map[string]any{"contractVersion": "1.0", "tokenType": "Bearer", "accessToken": token, "expiresAt": expires.UTC().Format("2006-01-02T15:04:05.000Z"), "context": context})
	return b, 200, nil
}

type memoryStore struct {
	mu   sync.Mutex
	raw  []byte
	fail bool
}

func (s *memoryStore) LoadRegistry() ([]byte, error) { return s.raw, nil }
func (s *memoryStore) SaveRegistry(raw []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail {
		return errors.New("fixture storage unavailable")
	}
	s.raw = bytes.Clone(raw)
	return nil
}

type productFixture struct {
	runtime          *noxmcp.Runtime
	response         atomic.Pointer[mcp.CallToolResult]
	request          atomic.Value // Raw tools/call request, for wire-level assertions.
	writes           atomic.Int64
	drop             atomic.Bool
	started, stopped chan struct{}
}

func fixture(t *testing.T, products ...string) (*Gateway, *fixtureAuthority, map[string]*productFixture) {
	t.Helper()
	dir, e := os.MkdirTemp("", "ng-")
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	signer, e := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if e != nil {
		t.Fatal(e)
	}
	kid := "fixture-key"
	authority := &fixtureAuthority{signer: signer, public: noxmcp.SuiteSigningKey{Kty: "EC", Crv: "P-256", X: base64.RawURLEncoding.EncodeToString(signer.X.FillBytes(make([]byte, 32))), Y: base64.RawURLEncoding.EncodeToString(signer.Y.FillBytes(make([]byte, 32))), Kid: &kid}, invalid: map[string]bool{}, registry: noxmcp.SuiteRegistrySnapshot{ContractVersion: "1.0", RegistryRevision: "registry-1", Cursor: "cursor-1", Products: []noxmcp.SuiteRegistrationRecord{}, Observations: []noxmcp.SuiteObservationEnvelope{}}}
	productStates := map[string]*productFixture{}
	for _, product := range products {
		decl := map[string]any{"contractVersion": "1.0", "productId": product, "productVersion": "test", "displayName": "Protocol participant", "capabilities": map[string]any{"suiteMcp": map[string]string{"path": "/mcp"}}}
		raw, _ := json.Marshal(map[string]any{"contractVersion": "1.0", "productId": product, "revision": "registration-1", "associationId": "association-" + product, "declaration": decl, "connection": map[string]string{"baseUrl": transport.Origin(product)}})
		record, e := noxmcp.LoadSuiteRegistrationRecord(raw)
		if e != nil {
			t.Fatal(e)
		}
		authority.registry.Products = append(authority.registry.Products, record)
		state := &productFixture{started: make(chan struct{}, 1), stopped: make(chan struct{}, 1)}
		productStates[product] = state
		policies := map[string]noxmcp.SuitePolicy{}
		tools := []noxmcp.Tool{}
		descriptors := map[string]noxmcp.JSON{}
		for _, name := range []string{"read", "write", "wait"} {
			scopes := []string{"nox:inspect"}
			if name == "write" {
				scopes = append(scopes, "nox:write")
			}
			policies[name], _ = noxmcp.NewSuitePolicy(scopes, false)
			descriptors[name] = noxmcp.JSON{"version": "1.0", "navigation": noxmcp.JSON{"label": name}, "parameters": noxmcp.JSON{"/selection": noxmcp.JSON{"options": noxmcp.JSON{"tool": "read"}}}}
			tools = append(tools, noxmcp.Tool{Name: name, Description: name, InputSchema: &jsonschema.Schema{Type: "object"}, OutputSchema: &jsonschema.Schema{Type: "object"}, ReadOnly: name != "write", Meta: map[string]any{"cli": name, "custom": "preserved"}, Execute: func(ctx noxmcp.ExecutionContext, _ map[string]any) (map[string]any, error) {
				if name == "write" {
					if e := ctx.CheckActive(); e != nil {
						return nil, e
					}
					state.writes.Add(1)
				}
				if name == "wait" {
					state.started <- struct{}{}
					<-ctx.Done()
					state.stopped <- struct{}{}
					return nil, ctx.Err()
				}
				return map[string]any{"product": product, "user": ctx.UserID}, nil
			}})
		}
		runtime, e := noxmcp.NewSuite(noxmcp.Options{AppID: product, Name: product, Version: "test", Tools: tools}, noxmcp.SuiteRuntimeOptions{Policies: policies, Verification: func() noxmcp.SuiteVerificationOptions {
			return noxmcp.SuiteVerificationOptions{Issuer: "urn:nox:fixture", Audience: record.Connection.BaseUrl + "/mcp", ProductID: product, AssociationID: record.AssociationID, GatewayServiceID: "gateway-fixture", RegistrationRevision: record.Revision, ServiceCredentialRevision: "credential-fixture", Keys: map[string]noxmcp.SuiteSigningKey{kid: authority.public}}
		}}, noxmcp.DiscoveryOptions{ServerID: product, Descriptors: descriptors, ResolveView: func(context.Context, *mcp.RequestExtra, []*mcp.Tool) (string, []string, []string, noxmcp.JSON, error) {
			return "fixture", nil, nil, nil, nil
		}})
		if e != nil {
			t.Fatal(e)
		}
		runtime.Server.AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
			return func(ctx context.Context, method string, request mcp.Request) (mcp.Result, error) {
				result, err := next(ctx, method, request)
				if call, ok := result.(*mcp.CallToolResult); method == "tools/call" && ok && !call.IsError && err == nil {
					if replacement := state.response.Load(); replacement != nil {
						return replacement, nil
					}
				}
				return result, err
			}
		})
		state.runtime = runtime
		listener, cleanup, e := transport.Listen(dir, product)
		if e != nil {
			t.Fatal(e)
		}
		t.Cleanup(cleanup)
		handler := runtime.HTTPHandler()
		server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasPrefix(r.Header.Get("Authorization"), "Bearer noxsync_") {
				t.Error("personal key reached product")
			}
			raw, _ := io.ReadAll(r.Body)
			r.Body = io.NopCloser(bytes.NewReader(raw))
			var message struct {
				Method string `json:"method"`
			}
			_ = json.Unmarshal(raw, &message)
			if message.Method == "tools/call" {
				state.request.Store(bytes.Clone(raw))
			}
			if state.drop.Load() && bytes.Contains(raw, []byte(`"method":"tools/call"`)) {
				buffer := httptest.NewRecorder()
				handler.ServeHTTP(buffer, r)
				connection, _, e := w.(http.Hijacker).Hijack()
				if e == nil {
					connection.Close()
				}
				return
			}
			handler.ServeHTTP(w, r)
		})}
		t.Cleanup(func() { server.Close() })
		go server.Serve(listener)
	}
	gateway, e := New(dir, "gateway-test-server", "test", authority, &memoryStore{})
	if e != nil {
		t.Fatal(e)
	}
	if e = gateway.RefreshRegistry(t.Context()); e != nil {
		t.Fatal(e)
	}
	return gateway, authority, productStates
}
func rpc(t *testing.T, g *Gateway, key, method string, id any, params map[string]any) (int, map[string]any) {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
	req := httptest.NewRequest("POST", "http://gateway/mcp", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	protocol := "2025-11-25"
	if meta, ok := params["_meta"].(map[string]any); ok {
		if version, ok := meta[mcp.MetaKeyProtocolVersion].(string); ok {
			protocol = version
		}
	}
	req.Header.Set("MCP-Protocol-Version", protocol)
	req.Header.Set("Mcp-Method", method)
	req.Header.Set("Authorization", "Bearer "+key)
	res := httptest.NewRecorder()
	g.ServeHTTP(res, req)
	var value map[string]any
	decoder := json.NewDecoder(bytes.NewReader(res.Body.Bytes()))
	decoder.UseNumber()
	if e := decoder.Decode(&value); e != nil && res.Body.Len() != 0 {
		t.Fatal("non-JSON RPC response", res.Code, res.Body.String())
	}
	return res.Code, value
}
func result(t *testing.T, status int, v map[string]any) map[string]any {
	t.Helper()
	if status != 200 || v["error"] != nil {
		t.Fatal("RPC failed", status, v)
	}
	r, ok := v["result"].(map[string]any)
	if !ok {
		t.Fatal("missing RPC result", v)
	}
	return r
}
func toolNames(v map[string]any) map[string]bool {
	names := map[string]bool{}
	for _, tool := range v["tools"].([]any) {
		names[tool.(map[string]any)["name"].(string)] = true
	}
	return names
}
func TestPrivateViewsAliasCollisionsAndAuthorityCache(t *testing.T) {
	g, f, _ := fixture(t, "sample-one", "sample-two")
	s, v := rpc(t, g, "noxsync_admin", "tools/list", 1, map[string]any{})
	view := result(t, s, v)
	names := toolNames(view)
	if !names["sample-one__read"] || !names["sample-two__read"] || !names["sample-one__write"] || view["cacheScope"] != "private" {
		t.Fatal("invalid aggregate", view)
	}
	f.mu.Lock()
	before := f.issued
	f.mu.Unlock()
	s, v = rpc(t, g, "noxsync_admin", "tools/list", 2, map[string]any{})
	result(t, s, v)
	f.mu.Lock()
	after := f.issued
	f.mu.Unlock()
	if after != before {
		t.Fatal("valid delegations were issued again")
	}
	s, v = rpc(t, g, "noxsync_member", "tools/list", 3, map[string]any{})
	if toolNames(result(t, s, v))["sample-one__write"] {
		t.Fatal("member inherited admin tools")
	}
	s, v = rpc(t, g, "noxsync_member", "tools/call", 4, map[string]any{"name": "sample-one__write", "arguments": map[string]any{}})
	if v["error"] == nil {
		t.Fatal("unauthorized mutation succeeded")
	}
}
func TestUncertainWritesAreNeverRetried(t *testing.T) {
	g, _, products := fixture(t, "sample-one")
	products["sample-one"].drop.Store(true)
	s, v := rpc(t, g, "noxsync_admin", "tools/call", 1, map[string]any{"name": "sample-one__write", "arguments": map[string]any{}})
	r := result(t, s, v)
	if r["isError"] != true || products["sample-one"].writes.Load() != 1 {
		t.Fatal("write was retried or uncertainty was hidden", r, products["sample-one"].writes.Load())
	}
}
func TestCancellationReachesProductExecution(t *testing.T) {
	g, _, products := fixture(t, "sample-one")
	done := make(chan struct{})
	go func() {
		defer close(done)
		rpc(t, g, "noxsync_admin", "tools/call", 42, map[string]any{"name": "sample-one__wait", "arguments": map[string]any{}})
	}()
	select {
	case <-products["sample-one"].started:
	case <-time.After(5 * time.Second):
		t.Fatal("tool did not start")
	}
	body := []byte(`{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":42}}`)
	req := httptest.NewRequest("POST", "http://gateway/mcp", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer noxsync_admin")
	res := httptest.NewRecorder()
	g.ServeHTTP(res, req)
	if res.Code != 202 {
		t.Fatal("cancellation refused")
	}
	select {
	case <-products["sample-one"].stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("cancellation did not reach product")
	}
	<-done
}
func TestUnavailableProductDoesNotHideOtherProducts(t *testing.T) {
	g, _, _ := fixture(t, "sample-one", "sample-two")
	s, v := rpc(t, g, "noxsync_admin", "tools/list", 1, map[string]any{})
	result(t, s, v)
	if e := os.Remove(g.dir + "/sample-one.sock"); e != nil {
		t.Fatal(e)
	}
	g.mu.Lock()
	for _, entry := range g.sources {
		entry.expires = time.Time{}
	}
	g.mu.Unlock()
	s, v = rpc(t, g, "noxsync_admin", "tools/list", 2, map[string]any{})
	names := toolNames(result(t, s, v))
	if !names["sample-two__read"] {
		t.Fatal("one failed product hid a healthy source")
	}
}

func TestRestrictedServiceProbe(t *testing.T) {
	g, f, _ := fixture(t, "sample-one")
	r := f.registry.Products[0]
	id, e := g.authorize(t.Context(), "", r, "probe")
	if e != nil {
		t.Fatal(e)
	}
	session, closeSession, e := g.connect(t.Context(), r, id, nil, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer closeSession()
	if e = ping(t.Context(), session); e != nil {
		t.Fatal("ping probe", e)
	}
}

func TestRegistryPersistenceFailureAndCursorReconciliation(t *testing.T) {
	g, f, _ := fixture(t, "sample-one")
	if _, err := g.store.LoadRegistry(); err != nil {
		t.Fatal(err)
	}
	before := g.state.Registry.RegistryRevision
	f.mu.Lock()
	f.registry.RegistryRevision = "registry-2"
	f.registry.Products = []noxmcp.SuiteRegistrationRecord{}
	f.mu.Unlock()
	storage := g.store.(*memoryStore)
	storage.mu.Lock()
	storage.fail = true
	storage.mu.Unlock()
	if err := g.RefreshRegistry(t.Context()); err == nil {
		t.Fatal("failed persistence accepted")
	}
	if g.state.Registry.RegistryRevision != before || len(g.state.Registry.Products) != 1 {
		t.Fatal("partial registry committed")
	}
	storage.mu.Lock()
	storage.fail = false
	storage.mu.Unlock()
	f.mu.Lock()
	f.expiredCursor = true
	f.mu.Unlock()
	if err := g.poll(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(g.state.Registry.Products) != 0 || g.state.Registry.RegistryRevision != "registry-2" {
		t.Fatal("expired cursor did not reconcile")
	}
	restarted, err := New(g.dir, g.serverID, g.version, f, storage)
	if err != nil {
		t.Fatal(err)
	}
	if restarted.Ready() {
		t.Fatal("disk registry granted readiness after restart")
	}
}
func TestOutageCannotAdmitNewOrExpiredViewsAndDenialNeverFallsBack(t *testing.T) {
	g, f, _ := fixture(t, "sample-one")
	s, v := rpc(t, g, "noxsync_admin", "tools/list", 1, map[string]any{})
	result(t, s, v)
	f.mu.Lock()
	f.unavailable = true
	f.mu.Unlock()
	s, v = rpc(t, g, "noxsync_admin", "tools/list", 2, map[string]any{})
	result(t, s, v)
	s, _ = rpc(t, g, "noxsync_new", "tools/list", 3, map[string]any{})
	if s != 503 {
		t.Fatal("outage admitted a new key", s)
	}
	g.mu.Lock()
	for key, a := range g.admissions {
		a.expires = time.Time{}
		g.admissions[key] = a
	}
	g.mu.Unlock()
	s, _ = rpc(t, g, "noxsync_admin", "tools/list", 4, map[string]any{})
	if s != 503 {
		t.Fatal("expired view survived outage", s)
	}
	f.mu.Lock()
	f.unavailable = false
	f.invalid["noxsync_admin"] = true
	f.mu.Unlock()
	s, _ = rpc(t, g, "noxsync_admin", "tools/list", 5, map[string]any{})
	if s != 401 {
		t.Fatal("denied key fell back to cache", s)
	}
}
func TestRequestLimitsAndCancelledWorkDoNotReachExecutor(t *testing.T) {
	g, f, products := fixture(t, "sample-one")
	g.slots <- struct{}{}
	for len(g.slots) < cap(g.slots) {
		g.slots <- struct{}{}
	}
	s, _ := rpc(t, g, "noxsync_admin", "tools/list", 1, map[string]any{})
	if s != 503 {
		t.Fatal("capacity not bounded", s)
	}
	for len(g.slots) > 0 {
		<-g.slots
	}
	body := []byte(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"sample-one__write","arguments":{}}}`)
	req := httptest.NewRequest("POST", "http://gateway/mcp", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer noxsync_admin")
	req.Header.Set("Nox-Request-Id", requestID())
	req.Header.Set("Nox-Deadline-At", time.Now().Add(-time.Second).UTC().Format("2006-01-02T15:04:05.000Z"))
	req.Header.Set("Nox-Timeout-Ms", "10000")
	w := httptest.NewRecorder()
	g.ServeHTTP(w, req)
	f.mu.Lock()
	issued := f.issued
	f.mu.Unlock()
	if w.Code != 400 || issued != 0 || products["sample-one"].writes.Load() != 0 {
		t.Fatal("expired request admitted", w.Code)
	}
	g.limits.MaxPayloadBytes = 1024
	req = httptest.NewRequest("POST", "http://gateway/mcp", strings.NewReader(strings.Repeat("x", 1025)))
	req.Header.Set("Authorization", "Bearer noxsync_admin")
	w = httptest.NewRecorder()
	g.ServeHTTP(w, req)
	if w.Code != 413 {
		t.Fatal("payload not bounded", w.Code)
	}
}

func TestChangedSourceDefinitionCannotExecuteFromOldSnapshot(t *testing.T) {
	g, _, products := fixture(t, "sample-one")
	partition, _ := noxmcp.SuiteCredentialPartition("noxsync_admin", g.secret)
	snapshot, _, err := g.catalog(t.Context(), "noxsync_admin", partition)
	if err != nil {
		t.Fatal(err)
	}
	var executed atomic.Bool
	policy := map[string]any{"version": "1.0", "requiredScopes": []string{"nox:inspect"}, "argumentDependent": false}
	products["sample-one"].runtime.Server.AddTool(&mcp.Tool{Name: "read", Description: "Changed definition", InputSchema: map[string]any{"type": "object"}, Meta: mcp.Meta{"nox/authorization": policy}}, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		executed.Store(true)
		return &mcp.CallToolResult{}, nil
	})
	controls := noxmcp.SuiteRequestControls{RequestID: requestID(), DeadlineAt: time.Now().Add(20 * time.Second).UTC().Format("2006-01-02T15:04:05.000Z"), TimeoutMS: 20000}
	_, err = g.dispatch(t.Context(), "noxsync_admin", partition, snapshot, "sample-one__read", &mcp.CallToolParamsRaw{Name: "sample-one__read", Arguments: json.RawMessage(`{}`)}, controls)
	if err == nil || err.Error() != "STALE_DEFINITION" || executed.Load() {
		t.Fatal("old definition executed", err)
	}
}

func TestClientProtocolMetadataIsNotForwardedAsGatewayCapabilities(t *testing.T) {
	g, _, products := fixture(t, "sample-one")
	s, v := rpc(t, g, "noxsync_admin", "tools/call", 1, map[string]any{"name": "sample-one__write", "arguments": map[string]any{}, "_meta": map[string]any{mcp.MetaKeyProtocolVersion: "2025-11-25", mcp.MetaKeyClientCapabilities: map[string]any{}}})
	r := result(t, s, v)
	if r["isError"] == true || products["sample-one"].writes.Load() != 1 {
		t.Fatal("caller protocol context crossed the source hop", r)
	}
}
