package trust

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"github.com/mapherez/nox-gateway/internal/backend"
	"github.com/mapherez/nox-gateway/internal/buildinfo"
	"github.com/mapherez/nox-gateway/internal/config"
	"github.com/mapherez/nox-gateway/internal/securefile"
	"github.com/mapherez/nox-gateway/internal/transport"
	noxmcp "github.com/mapherez/nox-mcp/go"
)

type Engine struct {
	cfg            config.Config
	store          Store
	client         *backend.Client
	doc            Document
	declaration    json.RawMessage
	fingerprint    string
	tlsFingerprint string
	logger         *slog.Logger
	status         status
}

func New(c config.Config, store Store, declaration json.RawMessage, pin string, logger *slog.Logger) (*Engine, error) {
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
	e := &Engine{cfg: c, store: store, doc: doc, declaration: declaration, fingerprint: fingerprint, tlsFingerprint: pin, logger: logger}
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
	if c.PairingFile != "" {
		if err = e.preparePairing(); err != nil {
			return nil, err
		}
	}
	if e.doc.Pending != nil {
		e.status.set("provisioning")
		if e.doc.Pending.BackendOrigin != c.BackendURL || e.doc.Pending.BackendPin != c.BackendTLSPin {
			return nil, ErrBinding
		}
		if e.doc.Pending.Kind == "pairing" {
			b := e.doc.Pending.Proof.Binding
			if b.BaseUrl != c.PublicURL || b.DeclarationFingerprint != fingerprint || b.TlsPublicKeyFingerprint != pin {
				return nil, ErrBinding
			}
		}
	} else if b := e.doc.Binding; b != nil {
		if b.PublicURL != c.PublicURL || b.TLSFingerprint != pin || b.Provisioning.BackendOrigin != c.BackendURL || b.BackendPin != c.BackendTLSPin {
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

func (e *Engine) preparePairing() error {
	raw, err := securefile.Read(e.cfg.PairingFile, true, noxmcp.SuiteMaxPayloadBytes)
	if err != nil {
		return err
	}
	intent, err := noxmcp.LoadSuitePairingIntent(raw)
	if err != nil {
		return ErrRecovery
	}
	if intent.PairingID == e.doc.LastPairingID {
		return nil
	}
	if intent.ProductID != buildinfo.ProductID || intent.Purpose != "gateway" || intent.BaseUrl != e.cfg.PublicURL || intent.ManagedBy != nil {
		return ErrBinding
	}
	if e.doc.Pending != nil {
		if e.doc.Pending.Kind == "pairing" && e.doc.Pending.Proof.Binding.PairingID == intent.PairingID {
			return nil
		}
		expires, err := time.Parse(time.RFC3339Nano, e.doc.Pending.ExpiresAt)
		if err != nil || time.Now().Before(expires) {
			return ErrRecovery
		}
	}
	expires, err := time.Parse(time.RFC3339Nano, intent.ExpiresAt)
	if err != nil || !expires.After(time.Now()) || expires.After(time.Now().Add(605*time.Second)) {
		return ErrRecovery
	}
	secret, err := base64.RawURLEncoding.DecodeString(intent.Secret)
	if err != nil {
		return ErrRecovery
	}
	binding := noxmcp.SuitePairingBinding{ContractVersion: buildinfo.ContractVersion, PairingID: intent.PairingID, Nonce: intent.Nonce, ProductID: buildinfo.ProductID, DeclarationFingerprint: e.fingerprint, BaseUrl: e.cfg.PublicURL, TlsPublicKeyFingerprint: e.tlsFingerprint}
	proof, err := noxmcp.CreateSuitePairingProof(secret, binding)
	if err != nil {
		return ErrRecovery
	}
	e.doc.Pending = &Operation{BackendOrigin: e.cfg.BackendURL, BackendPin: e.cfg.BackendTLSPin, Kind: "pairing", RequestKey: rand.Text(), AckKey: rand.Text(), ExpiresAt: intent.ExpiresAt, Proof: &noxmcp.SuitePairingProof{ContractVersion: buildinfo.ContractVersion, Binding: binding, Proof: proof}}
	return e.save() // The exact proof and idempotency keys are durable before HTTP.
}

// Run retries only recoverable management exchanges with their original durable
// keys. It never retries a tool call. Non-retryable failures require reconciliation.
func (e *Engine) Run(ctx context.Context) {
	if e.client == nil || (e.doc.Binding == nil && e.doc.Pending == nil) {
		return
	}
	delay := time.Second
	for ctx.Err() == nil {
		err := e.Reconcile(ctx)
		if err == nil {
			return
		}
		if ctx.Err() != nil {
			return
		}
		var failure *backend.Failure
		retry := errors.As(err, &failure) && failure.Retryable
		if !retry && e.Status() != "revoked" && e.Status() != "authorizationDenied" {
			e.status.set("recoveryRequired")
		}
		e.logger.Warn("gateway_trust_not_ready", "status", e.Status())
		if !retry {
			return
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		if delay < 30*time.Second {
			delay *= 2
			if delay > 30*time.Second {
				delay = 30 * time.Second
			}
		}
	}
}

func (e *Engine) Reconcile(ctx context.Context) error {
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
	e.status.set("paired")
	e.logger.Info("gateway_service_verified")
	return nil
}

func (e *Engine) fail(err error) error {
	var failure *backend.Failure
	if errors.As(err, &failure) {
		if failure.Denied && e.doc.Pending == nil && e.doc.Binding != nil {
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
	e.doc.Binding = &Binding{PublicURL: e.cfg.PublicURL, TLSFingerprint: e.tlsFingerprint, BackendPin: e.cfg.BackendTLSPin, Provisioning: *p}
	e.doc.LastPairingID = p.PairingID
	e.doc.Pending = nil
	if err = e.save(); err != nil {
		return err
	}
	e.logger.Info("gateway_pairing_acknowledged")
	return e.Reconcile(ctx)
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
