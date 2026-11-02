package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/mapherez/nox-gateway/internal/backend"
	"github.com/mapherez/nox-gateway/internal/transport"

	noxmcp "github.com/mapherez/nox-mcp/go"
)

func (g *Gateway) authorize(ctx context.Context, key string, r noxmcp.SuiteRegistrationRecord, kind string) (*noxmcp.VerifiedSuiteIdentity, error) {
	p, keys, e := g.trust.Authority()
	if e != nil {
		return nil, e
	}
	cap := r.Declaration.Capabilities.SuiteMcp
	if cap == nil || r.ProductID == "nox-gateway" || r.Connection.BaseUrl != transport.Origin(r.ProductID) {
		return nil, errors.New("INVALID_REGISTRATION")
	}
	audience, e := noxmcp.ResolveSuiteEndpoint(r.Connection.BaseUrl, cap.Path)
	if e != nil {
		return nil, e
	}
	partition := ""
	if kind == "probe" {
		partition, e = noxmcp.SuiteCredentialPartition(p.ServiceCredential, g.secret)
	} else {
		partition, e = noxmcp.SuiteCredentialPartition(key, g.secret)
	}
	if e != nil {
		return nil, e
	}
	binding := noxmcp.SuiteAuthorizationPartition{CredentialPartition: partition, Issuer: p.Issuer, Audience: audience, GatewayServiceID: p.Service.ServiceID, ServiceCredentialRevision: p.Service.CredentialRevision, ProductID: r.ProductID, AssociationID: r.AssociationID, RegistrationRevision: r.Revision, Kind: kind}
	return g.cache.Refresh(ctx, binding, func(ctx context.Context) (*noxmcp.VerifiedSuiteIdentity, error) {
		path := "/api/nox/authorizations"
		request := map[string]any{"contractVersion": "1.0", "productId": r.ProductID, "registrationRevision": r.Revision}
		if kind == "probe" {
			path = "/api/nox/service-authorizations"
		} else {
			request["apiKey"] = key
		}
		raw, _, e := g.trust.Request(ctx, "POST", path, "", request)
		if e != nil {
			return nil, e
		}
		trusted := map[string]noxmcp.SuiteSigningKey{}
		for _, k := range keys.Keys {
			if k.Kid != nil {
				trusted[*k.Kid] = k
			}
		}
		return noxmcp.VerifySuiteAuthorizationResponse(raw, noxmcp.SuiteVerificationOptions{Issuer: p.Issuer, Audience: audience, ProductID: r.ProductID, AssociationID: r.AssociationID, GatewayServiceID: p.Service.ServiceID, RegistrationRevision: r.Revision, ServiceCredentialRevision: p.Service.CredentialRevision, Keys: trusted, Kind: kind, Now: time.Now()})
	})
}

// An authenticated empty catalog still requires Backend to validate the human key.
// On an outage only previously verified non-empty views may use unexpired grants.
func (g *Gateway) visible(ctx context.Context, key, partition string) (map[string]bool, error) {
	// Reuse the verified non-empty admission only within its shortest grant.
	// Registry/authority events clear it; no live Backend round trip is required
	// for every tool invocation in an otherwise unchanged working session.
	g.mu.RLock()
	saved, ok := g.admissions[partition]
	g.mu.RUnlock()
	if ok && time.Now().Before(saved.expires) {
		return saved.products, nil
	}
	raw, _, e := g.humanRegistry(ctx, key)
	if e == nil {
		var body struct {
			Products []struct {
				ProductID string `json:"productId"`
			} `json:"products"`
		}
		if json.Unmarshal(raw, &body) != nil {
			return nil, errors.New("INVALID_BACKEND_RESPONSE")
		}
		ids := map[string]bool{}
		for _, p := range body.Products {
			ids[p.ProductID] = true
		}
		return ids, nil
	}
	var f *backend.Failure
	if errors.As(e, &f) && f.Retryable {
		g.mu.RLock()
		saved, ok := g.admissions[partition]
		g.mu.RUnlock()
		if ok && time.Now().Before(saved.expires) {
			return saved.products, nil
		}
	}
	return nil, e
}
func (g *Gateway) humanRegistry(ctx context.Context, key string) ([]byte, int, error) {
	// Reuse the restricted local client, with an explicit human credential only
	// for the Backend's role-filtered registry read. It never goes to a product.
	return g.trust.HumanRegistry(ctx, key)
}
