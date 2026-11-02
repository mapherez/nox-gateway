package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/mapherez/nox-gateway/internal/transport"

	noxmcp "github.com/mapherez/nox-mcp/go"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type sourceEntry struct {
	source     noxmcp.SuiteGatewaySource
	cache      *noxmcp.DiscoveryCache
	expires    time.Time
	generation uint64
	size       int
}
type authTransport struct {
	base       http.RoundTripper
	token      string
	controls   *noxmcp.SuiteRequestControls
	timeout    time.Duration
	maxPayload int
}

func (t authTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	q := r.Clone(r.Context())
	q.Header = q.Header.Clone()
	q.Header.Set("Authorization", "Bearer "+t.token)
	if t.controls != nil {
		c, e := noxmcp.SuiteRequestBudget(*t.controls, time.Now(), t.timeout, 0)
		if e != nil {
			return nil, e
		}
		q.Header.Set("Nox-Request-Id", c.RequestID)
		q.Header.Set("Nox-Deadline-At", c.DeadlineAt)
		q.Header.Set("Nox-Timeout-Ms", strconv.FormatInt(c.TimeoutMS, 10))
	}
	q.GetBody = nil // Never replay an uncertain mutation, even with idempotency metadata.
	res, e := t.base.RoundTrip(q)
	if e != nil {
		return nil, e
	}
	raw, e := io.ReadAll(io.LimitReader(res.Body, int64(t.maxPayload)+1))
	res.Body.Close()
	if e != nil || len(raw) > t.maxPayload {
		return nil, errors.New("PAYLOAD_LIMIT")
	}
	res.Body = io.NopCloser(bytes.NewReader(raw))
	return res, nil
}
func (g *Gateway) connect(ctx context.Context, r noxmcp.SuiteRegistrationRecord, id *noxmcp.VerifiedSuiteIdentity, controls *noxmcp.SuiteRequestControls) (*mcp.ClientSession, func(), error) {
	select {
	case g.network <- struct{}{}:
	case <-ctx.Done():
		return nil, nil, ctx.Err()
	}
	release := func() { <-g.network }
	t, e := transport.NewLocal(g.dir, r.Connection.BaseUrl)
	if e != nil {
		release()
		return nil, nil, e
	}
	httpClient := &http.Client{Transport: authTransport{base: t.HTTP.Transport, token: id.AccessToken(), controls: controls, timeout: g.limits.CallTimeout, maxPayload: g.limits.MaxPayloadBytes}, CheckRedirect: t.HTTP.CheckRedirect}
	client := mcp.NewClient(&mcp.Implementation{Name: "nox-gateway", Version: g.version}, nil)
	if e = noxmcp.RegisterDiscoveryClient(client); e != nil {
		t.Close()
		release()
		return nil, nil, e
	}
	session, e := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: r.Connection.BaseUrl + r.Declaration.Capabilities.SuiteMcp.Path, HTTPClient: httpClient, MaxRetries: -1, DisableStandaloneSSE: true}, nil)
	if e != nil {
		t.Close()
		release()
		return nil, nil, errors.New("PRODUCT_UNREACHABLE")
	}
	return session, func() { session.Close(); t.Close(); release() }, nil
}
func (g *Gateway) source(ctx context.Context, partition string, r noxmcp.SuiteRegistrationRecord, id *noxmcp.VerifiedSuiteIdentity, force bool) (noxmcp.SuiteGatewaySource, error) {
	view, e := noxmcp.SuiteAuthorizationViewKey(id, partition, "1.0")
	if e != nil {
		return noxmcp.SuiteGatewaySource{}, e
	}
	revision, _ := id.Context()["serviceCredentialRevision"].(string)
	key := view + ":" + revision + ":" + r.ProductID + ":" + r.Revision
	g.mu.RLock()
	old := g.sources[key]
	generation := g.generation
	g.mu.RUnlock()
	if !force && old != nil && old.generation == generation && time.Now().Before(old.expires) && time.Now().Before(id.ExpiresAt()) {
		return old.source, nil
	}
	session, closeSession, e := g.connect(ctx, r, id, nil)
	if e != nil {
		if old != nil && old.generation == generation && time.Now().Before(id.ExpiresAt()) {
			copy := old.source
			copy.Definitions = slices.Clone(copy.Definitions)
			for i := range copy.Definitions {
				v := "unavailable"
				copy.Definitions[i].Availability = &v
			}
			return copy, nil
		}
		return noxmcp.SuiteGatewaySource{}, e
	}
	defer closeSession()
	result := noxmcp.SuiteGatewaySource{ContractVersion: "1.0", Registration: r, ViewID: view, Definitions: []noxmcp.SuiteSourceDefinition{}}
	serverInfo := session.InitializeResult()
	result.ServerID = r.ProductID + ":" + r.AssociationID
	cache, e := noxmcp.NewDiscoveryCache(r.Connection.BaseUrl, view)
	if e != nil {
		return result, e
	}
	if old != nil && old.generation == generation {
		cache = old.cache
	}
	advanced := false
	if serverInfo.Capabilities != nil {
		_, advanced = serverInfo.Capabilities.Extensions[noxmcp.DiscoveryExtension]
	}
	if advanced {
		request := noxmcp.DiscoverySessionRequest(session)
		if old != nil && old.generation == generation {
			result.ServerID = old.source.ServerID
			result.ViewID = old.source.ViewID
		}
		if e = cache.Sync(ctx, func(c context.Context, method string, params noxmcp.JSON) (noxmcp.JSON, error) {
			value, err := request(c, method, params)
			if err == nil && method == "nox/tools/catalog" {
				if id, ok := value["serverId"].(string); ok {
					result.ServerID = id
				}
				if id, ok := value["viewId"].(string); ok {
					result.ViewID = id
				}
				if presentation, ok := value["presentation"]; ok {
					b, _ := json.Marshal(presentation)
					var fields map[string]json.RawMessage
					json.Unmarshal(b, &fields)
					result.Presentation = &fields
				}
			}
			return value, err
		}); e != nil {
			return result, e
		}
		for _, def := range cache.Definitions() {
			raw, _ := json.Marshal(def)
			var d noxmcp.SuiteSourceDefinition
			if json.Unmarshal(raw, &d) != nil {
				return result, errors.New("INVALID_DISCOVERY")
			}
			result.Definitions = append(result.Definitions, d)
		}
	} else {
		for tool, e := range session.Tools(ctx, nil) {
			if e != nil {
				return result, e
			}
			raw, _ := json.Marshal(tool)
			var d noxmcp.SuiteSourceDefinition
			json.Unmarshal([]byte(`{"tool":`+string(raw)+`}`), &d)
			result.Definitions = append(result.Definitions, d)
		}
	}
	claimScope, _ := id.Claims()["scope"].(string)
	scopes := strings.Fields(claimScope)
	filtered := result.Definitions[:0]
	for _, def := range result.Definitions {
		if def.Tool.Meta == nil {
			continue
		}
		raw, ok := (*def.Tool.Meta)["nox/authorization"]
		if !ok {
			continue
		}
		if _, e = noxmcp.LoadSuiteDocument("policy", raw); e != nil {
			continue
		}
		var policy noxmcp.SuitePolicy
		if json.Unmarshal(raw, &policy) != nil {
			continue
		}
		if _, e = noxmcp.RequireSuiteScopes(scopes, policy, nil); e == nil {
			filtered = append(filtered, def)
		}
	}
	result.Definitions = filtered
	raw, _ := json.Marshal(result)
	if len(raw) > g.limits.MaxPayloadBytes {
		return result, errors.New("PAYLOAD_LIMIT")
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.generation != generation {
		return result, errors.New("AUTHORIZATION_INVALIDATED")
	}
	size := len(raw)
	for _, v := range g.sources {
		size += v.size
	}
	if len(g.sources) >= 128 || size > 8<<20 {
		g.sources = map[string]*sourceEntry{}
	}
	g.sources[key] = &sourceEntry{source: result, cache: cache, expires: minTime(time.Now().Add(30*time.Second), id.ExpiresAt()), generation: generation, size: len(raw)}
	return result, nil
}
func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}
