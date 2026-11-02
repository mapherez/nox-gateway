// Package trust consumes Backend service provisioning. It never issues identity.
package trust

import (
	"bytes"
	"crypto/elliptic"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math/big"
	"strings"
	"sync/atomic"
	"time"

	"github.com/mapherez/nox-gateway/internal/buildinfo"
	noxmcp "github.com/mapherez/nox-mcp/go"
)

var (
	ErrRecovery = errors.New("service trust requires explicit recovery or a new administrator-authorized pairing")
	ErrBinding  = errors.New("service trust does not match the configured Backend, Gateway origin, or TLS key")
	ErrResponse = errors.New("Backend provisioning or trust response failed validation")
	ErrClock    = errors.New("clock moved backwards; synchronize the clock before using service trust")
)

type Store interface {
	LoadTrust() ([]byte, error)
	SaveTrust([]byte) error
}

type KeySet struct {
	ContractVersion string                   `json:"contractVersion"`
	Issuer          string                   `json:"issuer"`
	Revision        string                   `json:"revision"`
	Keys            []noxmcp.SuiteSigningKey `json:"keys"`
}

type Binding struct {
	Denied              bool                     `json:"denied"`
	PublicURL           string                   `json:"publicUrl"`
	TLSFingerprint      string                   `json:"tlsFingerprint"`
	BackendPin          string                   `json:"backendPin"`
	Provisioning        noxmcp.SuiteProvisioning `json:"provisioning"`
	AssociationRevision string                   `json:"associationRevision"`
	SigningKeys         KeySet                   `json:"signingKeys"`
	Revoked             bool                     `json:"revoked"`
}

func (Binding) String() string     { return "GatewayBinding(secrets redacted)" }
func (b Binding) GoString() string { return b.String() }

type Operation struct {
	BackendOrigin string                    `json:"backendOrigin"`
	BackendPin    string                    `json:"backendPin"`
	Kind          string                    `json:"kind"`
	RequestKey    string                    `json:"requestKey"`
	AckKey        string                    `json:"ackKey"`
	ExpiresAt     string                    `json:"expiresAt,omitempty"`
	Proof         *noxmcp.SuitePairingProof `json:"proof,omitempty"`
	Provisioning  *noxmcp.SuiteProvisioning `json:"provisioning,omitempty"`
	Revision      string                    `json:"revision,omitempty"`
}

func (Operation) String() string     { return "GatewayOperation(secrets redacted)" }
func (o Operation) GoString() string { return o.String() }

type Document struct {
	SchemaVersion int        `json:"schemaVersion"`
	UpdatedAt     int64      `json:"updatedAt"`
	Binding       *Binding   `json:"binding,omitempty"`
	Pending       *Operation `json:"pending,omitempty"`
	LastPairingID string     `json:"lastPairingId,omitempty"`
}

func (Document) String() string     { return "GatewayTrust(secrets redacted)" }
func (d Document) GoString() string { return d.String() }

type status struct{ value atomic.Value }

func (s *status) set(v string) { s.value.Store(v) }
func (s *status) get() string  { v, _ := s.value.Load().(string); return v }

func decode(raw []byte, target any) error {
	if _, err := noxmcp.ParseSuiteJSON(raw); err != nil {
		return ErrResponse
	}
	if err := json.Unmarshal(raw, target); err != nil {
		return ErrResponse
	}
	return nil
}

func validateIdentity(service noxmcp.SuiteServiceIdentity) error {
	raw, err := json.Marshal(service)
	if err != nil {
		return ErrResponse
	}
	if _, err = noxmcp.LoadSuiteServiceIdentity(raw); err != nil {
		return ErrResponse
	}
	if service.ProductID != buildinfo.ProductID || len(service.Grants) != 5 {
		return ErrResponse
	}
	required := map[string]bool{"registry.read": true, "authorization.delegate": true, "events.read": true, "mcp.report": true, "mcp.probe": true}
	for _, grant := range service.Grants {
		if !required[grant] {
			return ErrResponse
		}
		delete(required, grant)
	}
	if len(required) != 0 {
		return ErrResponse
	}
	return nil
}

func validateProvisioning(p noxmcp.SuiteProvisioning, origin, issuer string) error {
	raw, err := json.Marshal(p)
	if err != nil {
		return ErrResponse
	}
	if _, err = noxmcp.LoadSuiteProvisioning(raw); err != nil {
		return ErrResponse
	}
	if p.BackendOrigin != origin || p.SigningKeysUri != origin+"/api/nox/keys" || strings.ContainsAny(p.ServiceCredential, " \t\r\n") || (issuer != "" && p.Issuer != issuer) {
		return ErrResponse
	}
	return validateIdentity(p.Service)
}

func validateKeys(keys KeySet, issuer string) error {
	if keys.ContractVersion != buildinfo.ContractVersion || keys.Issuer != issuer || keys.Revision == "" || len(keys.Keys) > 256 {
		return ErrResponse
	}
	seen := map[string]bool{}
	for _, key := range keys.Keys {
		b, err := json.Marshal(key)
		if err != nil {
			return ErrResponse
		}
		if _, err = noxmcp.LoadSuiteSigningKey(b); err != nil {
			return ErrResponse
		}
		if key.Kid == nil || *key.Kid == "" || seen[*key.Kid] || (key.Alg != nil && *key.Alg != "ES256") || (key.Use != nil && *key.Use != "sig") {
			return ErrResponse
		}
		if _, private := key.Extra["d"]; private {
			return ErrResponse
		}
		seen[*key.Kid] = true
		x, err := base64.RawURLEncoding.DecodeString(key.X)
		if err != nil || len(x) != 32 {
			return ErrResponse
		}
		y, err := base64.RawURLEncoding.DecodeString(key.Y)
		if err != nil || len(y) != 32 {
			return ErrResponse
		}
		if !elliptic.P256().IsOnCurve(new(big.Int).SetBytes(x), new(big.Int).SetBytes(y)) {
			return ErrResponse
		}
	}
	return nil
}

func loadDocument(raw []byte) (Document, error) {
	d := Document{SchemaVersion: 1}
	if raw == nil {
		return d, nil
	}
	if _, err := noxmcp.ParseSuiteJSON(raw); err != nil {
		return d, ErrRecovery
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&d); err != nil || d.SchemaVersion != 1 {
		return d, ErrRecovery
	}
	if d.UpdatedAt > time.Now().Add(5*time.Second).UnixMilli() {
		return d, ErrClock
	}
	if d.Binding != nil {
		b := d.Binding
		origin, err := noxmcp.NormalizeSuiteOrigin(b.PublicURL)
		if err != nil || origin != b.PublicURL || validateProvisioning(b.Provisioning, b.Provisioning.BackendOrigin, "") != nil {
			return d, ErrRecovery
		}
	}
	if d.Pending != nil {
		o := d.Pending
		if o.RequestKey == "" || o.AckKey == "" {
			return d, ErrRecovery
		}
		switch o.Kind {
		case "pairing":
			if o.Proof == nil || o.ExpiresAt == "" {
				return d, ErrRecovery
			}
			b, _ := json.Marshal(o.Proof)
			if _, err := noxmcp.LoadSuitePairingProof(b); err != nil {
				return d, ErrRecovery
			}
		case "rotate", "revoke-credential", "revoke-service":
			if d.Binding == nil || o.Revision == "" {
				return d, ErrRecovery
			}
		default:
			return d, ErrRecovery
		}
		if o.Provisioning != nil && validateProvisioning(*o.Provisioning, o.Provisioning.BackendOrigin, "") != nil {
			return d, ErrRecovery
		}
	}
	return d, nil
}
