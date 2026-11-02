package trust

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mapherez/nox-gateway/internal/backend"
	"github.com/mapherez/nox-gateway/internal/buildinfo"
	"github.com/mapherez/nox-gateway/internal/config"
	"github.com/mapherez/nox-gateway/internal/transport"
	noxmcp "github.com/mapherez/nox-mcp/go"
)

type authoritySnapshot struct{ raw []byte }

type Engine struct {
	mu          sync.Mutex
	authority   atomic.Pointer[authoritySnapshot]
	cfg         config.Config
	store       Store
	client      *backend.Client
	doc         Document
	fingerprint string
	logger      *slog.Logger
	status      status
}

func New(c config.Config, store Store, declaration json.RawMessage, logger *slog.Logger) (*Engine, error) {
	if c.BackendURL != "" && c.BackendURL != "http://nox-backend" {
		return nil, ErrBinding
	}
	raw, err := store.LoadTrust()
	if err != nil {
		return nil, err
	}
	doc, err := loadDocument(raw)
	if err != nil {
		return nil, err
	}
	value, err := noxmcp.ParseSuiteJSON(declaration)
	if err != nil {
		return nil, ErrResponse
	}
	fingerprint, err := noxmcp.Fingerprint(value)
	if err != nil {
		return nil, ErrResponse
	}
	e := &Engine{cfg: c, store: store, doc: doc, fingerprint: fingerprint, logger: logger}
	e.status.set("notPaired")
	if c.BackendURL == "" {
		if doc.Binding != nil || doc.Pending != nil {
			return nil, ErrBinding
		}
		return e, nil
	}
	t, err := transport.NewClient(c)
	if err != nil {
		return nil, err
	}
	e.client = backend.New(c.BackendURL, t)
	ok := false
	defer func() {
		if !ok {
			e.Close()
		}
	}()
	if e.doc.Pending != nil {
		e.status.set("provisioning")
		if e.doc.Pending.BackendOrigin != c.BackendURL || e.doc.Pending.BackendPin != "" {
			return nil, ErrBinding
		}
		if e.doc.Pending.Kind == "pairing" {
			b := e.doc.Pending.Proof.Binding
			if b.BaseUrl != "http://nox-gateway" || b.DeclarationFingerprint != fingerprint || b.TlsPublicKeyFingerprint != nil {
				return nil, ErrBinding
			}
		}
	} else if b := e.doc.Binding; b != nil {
		if b.PublicURL != "http://nox-gateway" || b.TLSFingerprint != "" || b.Provisioning.BackendOrigin != c.BackendURL || b.BackendPin != "" {
			return nil, ErrBinding
		}
		if b.Revoked {
			e.status.set("revoked")
		} else if b.Denied {
			e.status.set("authorizationDenied")
		} else {
			e.status.set("unverified")
		}
	}
	ok = true
	return e, nil
}

func (e *Engine) Close() {
	if e.client != nil {
		e.client.Close()
	}
}
func (e *Engine) Status() string { return e.status.get() }
func (e *Engine) save() error {
	now := time.Now().UnixMilli()
	if e.doc.UpdatedAt > now+5000 {
		return ErrClock
	}
	e.doc.UpdatedAt = now
	raw, err := json.Marshal(e.doc)
	if err != nil {
		return ErrRecovery
	}
	return e.store.SaveTrust(raw)
}

// Receive is called only by the same-host pairing receiver. It durably records
// the exact proof/operation keys before any completion or acknowledgement.
func (e *Engine) Receive(ctx context.Context, intent noxmcp.SuitePairingIntent) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := e.prepareIntent(intent); err != nil {
		return err
	}
	return e.reconcile(ctx)
}
func (e *Engine) prepareIntent(intent noxmcp.SuitePairingIntent) error {
	if e.client == nil {
		return ErrRecovery
	}
	raw, err := json.Marshal(intent)
	if err != nil {
		return ErrRecovery
	}
	if _, err = noxmcp.LoadSuitePairingIntent(raw); err != nil {
		return ErrRecovery
	}
	if intent.ProductID != buildinfo.ProductID || intent.Purpose != "gateway" || intent.BaseUrl != "http://nox-gateway" || intent.ManagedBy != nil {
		return ErrBinding
	}
	if intent.PairingID == e.doc.LastPairingID {
		return nil
	}
	if e.doc.Pending != nil {
		if e.doc.Pending.Kind != "pairing" || e.doc.Pending.Proof.Binding.PairingID != intent.PairingID {
			return ErrRecovery
		}
		return nil
	}
	expires, err := noxmcp.ParseSuiteTimestamp(intent.ExpiresAt)
	if err != nil || !expires.After(time.Now()) || expires.After(time.Now().Add(605*time.Second)) {
		return ErrRecovery
	}
	secret, err := base64.RawURLEncoding.DecodeString(intent.Secret)
	if err != nil {
		return ErrRecovery
	}
	binding := noxmcp.SuitePairingBinding{ContractVersion: "1.0", PairingID: intent.PairingID, Nonce: intent.Nonce, ProductID: buildinfo.ProductID, DeclarationFingerprint: e.fingerprint, BaseUrl: "http://nox-gateway"}
	proof, err := noxmcp.CreateSuitePairingProof(secret, binding)
	if err != nil {
		return ErrRecovery
	}
	e.authority.Store(nil)
	e.doc.Pending = &Operation{BackendOrigin: "http://nox-backend", Kind: "pairing", RequestKey: rand.Text(), AckKey: rand.Text(), ExpiresAt: intent.ExpiresAt, Proof: &noxmcp.SuitePairingProof{ContractVersion: "1.0", Binding: binding, Proof: proof}}
	if err = e.save(); err != nil {
		return err
	}
	e.status.set("provisioning")
	return nil
}

// Authority returns a private copy only after live verification in this process.
func (e *Engine) Authority() (noxmcp.SuiteProvisioning, KeySet, error) {
	snapshot := e.authority.Load()
	if snapshot == nil {
		return noxmcp.SuiteProvisioning{}, KeySet{}, ErrRecovery
	}
	var b Binding
	if json.Unmarshal(snapshot.raw, &b) != nil {
		return noxmcp.SuiteProvisioning{}, KeySet{}, ErrRecovery
	}
	return b.Provisioning, b.SigningKeys, nil
}
func (e *Engine) Request(ctx context.Context, method, path, key string, body any) ([]byte, int, error) {
	p, _, err := e.Authority()
	if err != nil {
		return nil, 0, err
	}
	return e.client.Request(ctx, method, path, p.ServiceCredential, key, "", body)
}

func (e *Engine) Reconcile(ctx context.Context) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	err := e.reconcile(ctx)
	if err != nil {
		var failure *backend.Failure
		if !errors.As(err, &failure) || !failure.Retryable {
			e.authority.Store(nil)
		}
	}
	return err
}
func (e *Engine) reconcile(ctx context.Context) error {
	if e.doc.UpdatedAt > time.Now().Add(5*time.Second).UnixMilli() {
		return ErrClock
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if e.client == nil {
		return ErrRecovery
	}
	if e.doc.Pending != nil {
		switch e.doc.Pending.Kind {
		case "pairing":
			return e.completePairing(ctx)
		case "rotate":
			return e.completeRotation(ctx)
		case "revoke-credential", "revoke-service":
			return e.completeRevocation(ctx)
		}
	}
	if e.doc.Binding == nil {
		return nil
	}
	if e.doc.Binding.Revoked {
		e.status.set("revoked")
		return nil
	}
	_, err := e.readService(ctx)
	if err != nil {
		return e.fail(err)
	}
	if err = e.refreshKeys(ctx, e.doc.Binding.Provisioning.Issuer); err != nil {
		return e.fail(err)
	}
	e.doc.Binding.Denied = false
	if err = e.save(); err != nil {
		return err
	}
	raw, err := json.Marshal(e.doc.Binding)
	if err != nil {
		return ErrResponse
	}
	e.authority.Store(&authoritySnapshot{raw: raw})
	if e.status.get() != "paired" {
		e.logger.Info("gateway_service_verified")
	}
	e.status.set("paired")
	return nil
}

func (e *Engine) fail(err error) error {
	var failure *backend.Failure
	if errors.As(err, &failure) {
		if failure.Denied && e.doc.Pending == nil && e.doc.Binding != nil {
			e.authority.Store(nil)
			e.doc.Binding.Denied = true
			if saveErr := e.save(); saveErr != nil {
				return saveErr
			}
			e.status.set("authorizationDenied")
		} else if failure.Retryable {
			e.status.set("backendUnavailable")
		}
	}
	return err
}

func (e *Engine) expectedIssuer() string {
	if e.doc.Binding != nil {
		return e.doc.Binding.Provisioning.Issuer
	}
	return ""
}

func (e *Engine) completePairing(ctx context.Context) error {
	o := e.doc.Pending
	if o.Provisioning == nil {
		expires, err := time.Parse(time.RFC3339Nano, o.ExpiresAt)
		if err != nil || !time.Now().Before(expires) {
			return ErrRecovery
		}
		raw, status, err := e.client.Request(ctx, "POST", "/api/nox/pairings/"+backend.Resource(o.Proof.Binding.PairingID)+"/complete", "", o.RequestKey, "", o.Proof)
		if err != nil {
			return e.fail(err)
		}
		p, err := noxmcp.LoadSuiteProvisioning(raw)
		if err != nil || status != 200 || p.PairingID != o.Proof.Binding.PairingID || validateProvisioning(p, e.cfg.BackendURL, e.expectedIssuer()) != nil || p.Service.ExpiresAt == nil || *p.Service.ExpiresAt != o.ExpiresAt {
			return ErrResponse
		}
		o.Provisioning = &p
		if err = e.save(); err != nil {
			return err
		} // Never ACK before persistence succeeds.
	}
	if err := e.save(); err != nil {
		return err
	}
	p := o.Provisioning
	identity, err := e.acknowledge(ctx, "/api/nox/pairings/"+backend.Resource(p.PairingID)+"/ack", p, o.AckKey)
	if err != nil {
		return e.fail(err)
	}
	// Persist completed binding first. A following GET outage must not discard an
	// ACK that already committed; restart can safely verify the saved association.
	p.Service = identity
	e.doc.Binding = &Binding{PublicURL: "http://nox-gateway", Provisioning: *p}
	e.doc.LastPairingID = p.PairingID
	e.doc.Pending = nil
	if err = e.save(); err != nil {
		return err
	}
	e.logger.Info("gateway_pairing_acknowledged")
	return e.reconcile(ctx)
}

func (e *Engine) acknowledge(ctx context.Context, path string, p *noxmcp.SuiteProvisioning, key string) (noxmcp.SuiteServiceIdentity, error) {
	var out struct {
		Acknowledged bool                        `json:"acknowledged"`
		Service      noxmcp.SuiteServiceIdentity `json:"service"`
	}
	raw, status, err := e.client.Request(ctx, "POST", path, p.ServiceCredential, key, "", nil)
	if err != nil {
		return out.Service, err
	}
	if status != 200 || decode(raw, &out) != nil || !out.Acknowledged || validateIdentity(out.Service) != nil || !sameIdentity(out.Service, p.Service) || out.Service.ExpiresAt != nil {
		return out.Service, ErrResponse
	}
	return out.Service, nil
}

func sameIdentity(a, b noxmcp.SuiteServiceIdentity) bool {
	if a.ServiceID != b.ServiceID || a.CredentialID != b.CredentialID || a.CredentialRevision != b.CredentialRevision || a.AssociationID != b.AssociationID || a.ProductID != b.ProductID {
		return false
	}
	aa, _ := json.Marshal(a.Grants)
	bb, _ := json.Marshal(b.Grants)
	return string(aa) == string(bb)
}

type serviceRead struct {
	ContractVersion string                      `json:"contractVersion"`
	ServiceID       string                      `json:"serviceId"`
	ProductID       string                      `json:"productId"`
	AssociationID   string                      `json:"associationId"`
	BaseURL         string                      `json:"baseUrl"`
	Purpose         string                      `json:"purpose"`
	State           string                      `json:"state"`
	Revision        string                      `json:"revision"`
	Service         noxmcp.SuiteServiceIdentity `json:"service"`
	CredentialState string                      `json:"credentialState"`
}

func (e *Engine) readService(ctx context.Context) (serviceRead, error) {
	b := e.doc.Binding
	var out serviceRead
	raw, status, err := e.client.Request(ctx, "GET", "/api/nox/services/"+backend.Resource(b.Provisioning.Service.ServiceID), b.Provisioning.ServiceCredential, "", "", nil)
	if err != nil {
		return out, err
	}
	if status != 200 || decode(raw, &out) != nil || out.ContractVersion != buildinfo.ContractVersion || out.State != "active" || out.CredentialState != "active" || out.Purpose != "gateway" || out.BaseURL != b.PublicURL || out.ProductID != buildinfo.ProductID || out.ServiceID != b.Provisioning.Service.ServiceID || out.AssociationID != b.Provisioning.Service.AssociationID || out.Revision == "" || validateIdentity(out.Service) != nil || !sameIdentity(out.Service, b.Provisioning.Service) || out.Service.ExpiresAt != nil {
		return out, ErrResponse
	}
	b.AssociationRevision = out.Revision
	return out, nil
}

func (e *Engine) refreshKeys(ctx context.Context, issuer string) error {
	raw, status, err := e.client.Request(ctx, "GET", "/api/nox/keys", "", "", "", nil)
	if err != nil {
		return err
	}
	var keys KeySet
	if status != 200 || decode(raw, &keys) != nil || validateKeys(keys, issuer) != nil {
		return ErrResponse
	}
	e.doc.Binding.SigningKeys = keys
	return nil
}

// HumanRegistry authenticates a client even when it has no Suite MCP products.
func (e *Engine) HumanRegistry(ctx context.Context, key string) ([]byte, int, error) {
	if _, _, err := e.Authority(); err != nil {
		return nil, 0, err
	}
	return e.client.Request(ctx, "GET", "/api/nox/products", key, "", "", nil)
}
