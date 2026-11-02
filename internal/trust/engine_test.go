package trust

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mapherez/nox-gateway/internal/app"
	"github.com/mapherez/nox-gateway/internal/config"
	"github.com/mapherez/nox-gateway/internal/state"
	"github.com/mapherez/nox-gateway/internal/transport"
	noxmcp "github.com/mapherez/nox-mcp/go"
)

type authorityFixture struct {
	mu             sync.Mutex
	server         *httptest.Server
	config         config.Config
	dir            string
	intent         noxmcp.SuitePairingIntent
	secret         []byte
	provision      noxmcp.SuiteProvisioning
	rotated        *noxmcp.SuiteProvisioning
	completeCount  int
	rotationCount  int
	ackCount       int
	completeKey    string
	rotationKey    string
	ackKeys        map[string]string
	active         bool
	revoked        bool
	dropComplete   bool
	dropAck        bool
	dropRotate     bool
	unavailable    bool
	alterProvision func(*noxmcp.SuiteProvisioning)
	beforeAck      func() bool
}

func fixture(t *testing.T) *authorityFixture {
	t.Helper()
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		t.Fatal(err)
	}
	f := &authorityFixture{secret: secret, ackKeys: map[string]string{}}
	f.intent = noxmcp.SuitePairingIntent{ContractVersion: "1.0", PairingID: "pairing_test", ProductID: "nox-gateway", Purpose: "gateway", BaseUrl: "http://nox-gateway", Nonce: base64.RawURLEncoding.EncodeToString(secret), Secret: base64.RawURLEncoding.EncodeToString(secret), ExpiresAt: time.Now().Add(10 * time.Minute).UTC().Format("2006-01-02T15:04:05.000Z")}
	expiry := f.intent.ExpiresAt
	f.provision = noxmcp.SuiteProvisioning{ContractVersion: "1.0", PairingID: f.intent.PairingID, Issuer: "urn:nox:backend:test-authority", ServiceCredential: "noxsvc_" + base64.RawURLEncoding.EncodeToString(secret), Service: noxmcp.SuiteServiceIdentity{ContractVersion: "1.0", ProductID: "nox-gateway", ServiceID: "service_test", AssociationID: "association_test", CredentialID: "credential_test", CredentialRevision: "credential_revision_test", Grants: []string{"registry.read", "authorization.delegate", "events.read", "mcp.report", "mcp.probe"}, ExpiresAt: &expiry}}
	local, err := os.MkdirTemp("", "ng-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(local) })
	listener, cleanup, err := transport.Listen(local, "nox-backend")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cleanup)
	f.server = httptest.NewUnstartedServer(http.HandlerFunc(f.handle))
	f.server.Listener.Close()
	f.server.Listener = listener
	f.server.Start()
	f.server.URL = "http://nox-backend"
	t.Cleanup(f.server.Close)
	f.provision.BackendOrigin = f.server.URL
	f.provision.SigningKeysUri = f.server.URL + "/api/nox/keys"
	f.dir = filepath.Join(t.TempDir(), "state")
	f.config = config.Config{BackendURL: "http://nox-backend", SocketDir: local}
	return f
}

func (f *authorityFixture) handle(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if f.unavailable {
		w.WriteHeader(503)
		io.WriteString(w, `{"error":{"message":"PRIVATE_RESPONSE"}}`)
		return
	}
	send := func(status int, v any) { w.WriteHeader(status); _ = json.NewEncoder(w).Encode(v) }
	deny := func() { send(401, map[string]any{"error": map[string]string{"message": "PRIVATE_RESPONSE"}}) }
	drop := func() {
		conn, _, err := w.(http.Hijacker).Hijack()
		if err == nil {
			conn.Close()
		}
	}
	switch r.URL.Path {
	case "/api/nox/pairings/pairing_test/complete":
		raw, _ := io.ReadAll(r.Body)
		proof, err := noxmcp.LoadSuitePairingProof(raw)
		if err != nil || !noxmcp.VerifySuitePairingProof(f.secret, proof.Binding, proof.Proof) || proof.Binding.BaseUrl != f.intent.BaseUrl || proof.Binding.ProductID != "nox-gateway" {
			deny()
			return
		}
		if r.Header.Get("Authorization") != "" || r.Header.Get("Idempotency-Key") == "" {
			deny()
			return
		}
		if f.completeKey != "" && f.completeKey != r.Header.Get("Idempotency-Key") {
			send(409, map[string]bool{"conflict": true})
			return
		}
		if f.completeKey == "" {
			f.completeKey = r.Header.Get("Idempotency-Key")
			f.completeCount++
		}
		if f.dropComplete {
			f.dropComplete = false
			drop()
			return
		}
		p := f.provision
		if f.alterProvision != nil {
			f.alterProvision(&p)
		}
		send(200, p)
	case "/api/nox/pairings/pairing_test/ack", "/api/nox/services/service_test/credentials/credential_rotated/ack":
		p := f.provision
		if strings.Contains(r.URL.Path, "credential_rotated") {
			if f.rotated == nil {
				if f.provision.Service.CredentialID != "credential_rotated" {
					deny()
					return
				}
			} else {
				p = *f.rotated
			}
		}
		if f.revoked || r.Header.Get("Authorization") != "Bearer "+p.ServiceCredential || r.Header.Get("Idempotency-Key") == "" {
			deny()
			return
		}
		if f.beforeAck != nil && !f.beforeAck() {
			send(500, map[string]bool{"notPersisted": true})
			return
		}
		previous := f.ackKeys[r.URL.Path]
		if previous != "" && previous != r.Header.Get("Idempotency-Key") {
			send(409, map[string]bool{"conflict": true})
			return
		}
		if previous == "" {
			f.ackKeys[r.URL.Path] = r.Header.Get("Idempotency-Key")
			f.ackCount++
		}
		f.active = true
		p.Service.ExpiresAt = nil
		if f.rotated != nil && strings.Contains(r.URL.Path, "credential_rotated") {
			f.provision = p
			f.rotated = nil
		} else {
			f.provision.Service.ExpiresAt = nil
		}
		if f.dropAck {
			f.dropAck = false
			drop()
			return
		}
		send(200, map[string]any{"acknowledged": true, "service": p.Service})
	case "/api/nox/services/service_test":
		if f.revoked || r.Header.Get("Authorization") != "Bearer "+f.provision.ServiceCredential {
			deny()
			return
		}
		if r.Method == "DELETE" {
			f.revoked = true
			send(200, map[string]bool{"revoked": true})
			return
		}
		if !f.active {
			deny()
			return
		}
		send(200, map[string]any{"contractVersion": "1.0", "serviceId": "service_test", "productId": "nox-gateway", "associationId": "association_test", "baseUrl": f.intent.BaseUrl, "purpose": "gateway", "state": "active", "revision": "association_revision_test", "service": f.provision.Service, "credentialState": "active"})
	case "/api/nox/keys":
		send(200, map[string]any{"contractVersion": "1.0", "issuer": f.provision.Issuer, "revision": "keys_test", "keys": []any{}})
	case "/api/nox/services/service_test/credentials":
		if f.revoked || r.Header.Get("Authorization") != "Bearer "+f.provision.ServiceCredential || r.Header.Get("If-Match") != `"`+f.provision.Service.CredentialRevision+`"` {
			deny()
			return
		}
		key := r.Header.Get("Idempotency-Key")
		if f.rotationKey != "" && key != f.rotationKey {
			send(409, map[string]bool{"conflict": true})
			return
		}
		if f.rotationKey == "" {
			f.rotationKey = key
			f.rotationCount++
			p := f.provision
			p.Service.CredentialID = "credential_rotated"
			p.Service.CredentialRevision = "credential_revision_rotated"
			p.ServiceCredential = "noxsvc_" + strings.Repeat("R", 43)
			expiry := time.Now().Add(5 * time.Minute).UTC().Format("2006-01-02T15:04:05.000Z")
			p.Service.ExpiresAt = &expiry
			f.rotated = &p
		}
		if f.dropRotate {
			f.dropRotate = false
			drop()
			return
		}
		send(201, map[string]any{"contractVersion": "1.0", "service": f.rotated.Service, "serviceCredential": f.rotated.ServiceCredential, "acknowledgeBy": *f.rotated.Service.ExpiresAt})
	case "/api/nox/services/service_test/credentials/credential_test", "/api/nox/services/service_test/credentials/credential_rotated":
		if f.revoked || r.Method != "DELETE" || r.Header.Get("Authorization") != "Bearer "+f.provision.ServiceCredential {
			deny()
			return
		}
		f.revoked = true
		send(200, map[string]bool{"revoked": true})
	default:
		send(404, map[string]bool{"missing": true})
	}
}

func (f *authorityFixture) open(t *testing.T, store Store) *Engine {
	t.Helper()
	declaration, err := app.Declaration("test-build")
	if err != nil {
		t.Fatal(err)
	}
	e, err := New(f.config, store, declaration, slog.New(slog.NewJSONHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	if err := e.prepareIntent(f.intent); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(e.Close)
	return e
}
func (f *authorityFixture) store(t *testing.T) *state.Store {
	t.Helper()
	s, err := state.Open(f.dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}
func (f *authorityFixture) counts() (int, int, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.completeCount, f.ackCount, f.rotationCount
}

func TestPairingPersistsBeforeACKAndRestartsWithoutIssuingAnotherCredential(t *testing.T) {
	f := fixture(t)
	store := f.store(t)
	f.beforeAck = func() bool {
		raw, err := store.LoadTrust()
		return err == nil && strings.Contains(string(raw), f.provision.ServiceCredential)
	}
	e := f.open(t, store)
	if err := e.Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	if e.Status() != "paired" {
		t.Fatal("pairing did not become verified")
	}
	complete, acks, _ := f.counts()
	if complete != 1 || acks != 1 {
		t.Fatal("unexpected duplicate provisioning")
	}
	e.Close()
	store.Close()
	restarted := f.open(t, f.store(t))
	if restarted.Status() != "unverified" {
		t.Fatal("restart claimed fresh authority before verification")
	}
	if err := restarted.Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	complete, acks, _ = f.counts()
	if complete != 1 || acks != 1 {
		t.Fatal("restart issued another credential")
	}
}

func TestLostCompletionAndAcknowledgmentRecoverAcrossRestart(t *testing.T) {
	for _, stage := range []string{"complete", "ack"} {
		t.Run(stage, func(t *testing.T) {
			f := fixture(t)
			if stage == "complete" {
				f.dropComplete = true
			} else {
				f.dropAck = true
			}
			store := f.store(t)
			e := f.open(t, store)
			if err := e.Reconcile(t.Context()); err == nil {
				t.Fatal("lost response was reported as success")
			}
			e.Close()
			store.Close()
			restarted := f.open(t, f.store(t))
			if err := restarted.Reconcile(t.Context()); err != nil {
				t.Fatal(err)
			}
			complete, acks, _ := f.counts()
			if complete != 1 || acks != 1 || restarted.Status() != "paired" {
				t.Fatal("recovery duplicated or failed provisioning")
			}
		})
	}
}

type failingStore struct {
	Store
	writes atomic.Int32
	failAt int32
}

func (f *failingStore) SaveTrust(raw []byte) error {
	if f.writes.Add(1) == f.failAt {
		return errors.New("injected persistence failure")
	}
	return f.Store.SaveTrust(raw)
}

func TestPersistenceFailureCannotAcknowledgeAnUnsavedCredential(t *testing.T) {
	f := fixture(t)
	store := f.store(t)
	fault := &failingStore{Store: store, failAt: 2}
	e := f.open(t, fault)
	if err := e.Reconcile(t.Context()); err == nil {
		t.Fatal("injected persistence failure was hidden")
	}
	_, acks, _ := f.counts()
	if acks != 0 {
		t.Fatal("ACK was sent before credential persistence")
	}
	if err := e.Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	_, acks, _ = f.counts()
	if acks != 1 {
		t.Fatal("persisted recovery did not ACK")
	}
}

func TestUnboundProvisioningCannotBeSavedOrAcknowledged(t *testing.T) {
	for _, change := range []string{"backend", "issuer-uri", "product", "purpose-grants", "pairing", "expiry", "credential-header"} {
		t.Run(change, func(t *testing.T) {
			f := fixture(t)
			f.alterProvision = func(p *noxmcp.SuiteProvisioning) {
				switch change {
				case "backend":
					p.BackendOrigin = "https://other.home.arpa"
				case "issuer-uri":
					p.SigningKeysUri = "https://other.home.arpa/api/nox/keys"
				case "product":
					p.Service.ProductID = "unrelated-product"
				case "purpose-grants":
					p.Service.Grants = append([]string(nil), p.Service.Grants...)
					p.Service.Grants[0] = "registry.manage-associated"
				case "pairing":
					p.PairingID = "another_pairing"
				case "expiry":
					p.Service.ExpiresAt = nil
				case "credential-header":
					p.ServiceCredential = strings.Repeat("X", 43) + "\r\nInjected: true"
				}
			}
			e := f.open(t, f.store(t))
			if err := e.Reconcile(t.Context()); err == nil {
				t.Fatal("accepted mismatched provisioning")
			}
			_, acks, _ := f.counts()
			if acks != 0 {
				t.Fatal("acknowledged an unbound credential")
			}
		})
	}
}

func TestRotationRecoversLostResponsesAndKeepsOneNewGeneration(t *testing.T) {
	for _, stage := range []string{"normal", "rotate", "ack"} {
		t.Run(stage, func(t *testing.T) {
			f := fixture(t)
			store := f.store(t)
			e := f.open(t, store)
			if err := e.Reconcile(t.Context()); err != nil {
				t.Fatal(err)
			}
			f.mu.Lock()
			if stage == "rotate" {
				f.dropRotate = true
			}
			if stage == "ack" {
				f.dropAck = true
			}
			f.mu.Unlock()
			err := e.Operate(t.Context(), "rotate")
			if stage == "normal" && err != nil {
				t.Fatal(err)
			}
			if stage != "normal" {
				if err == nil {
					t.Fatal("lost rotation response reported success")
				}
				e.Close()
				store.Close()
				e = f.open(t, f.store(t))
				if err = e.Reconcile(t.Context()); err != nil {
					t.Fatal(err)
				}
			}
			_, acks, rotations := f.counts()
			if acks != 2 || rotations != 1 || e.doc.Binding.Provisioning.Service.CredentialID != "credential_rotated" || e.Status() != "paired" {
				t.Fatal("rotation recovery duplicated or lost the new generation")
			}
		})
	}
}

func TestRevocationAndBackendOutageDoNotEraseOrReviveAuthority(t *testing.T) {
	f := fixture(t)
	store := f.store(t)
	e := f.open(t, store)
	if err := e.Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.unavailable = true
	f.mu.Unlock()
	if err := e.Reconcile(t.Context()); err == nil || e.Status() != "backendUnavailable" || e.doc.Binding.Revoked {
		t.Fatal("outage was treated as revocation or success")
	}
	if _, _, err := e.Authority(); err != nil {
		t.Fatal("outage erased process-verified authority")
	}
	f.mu.Lock()
	f.unavailable = false
	f.revoked = true
	f.mu.Unlock()
	if err := e.Reconcile(t.Context()); err == nil || e.Status() != "authorizationDenied" || !e.doc.Binding.Denied {
		t.Fatal("explicit denial did not invalidate local service access")
	}
	if _, _, err := e.Authority(); err == nil {
		t.Fatal("denial left usable authority")
	}
	e.Close()
	store.Close()
	restarted := f.open(t, f.store(t))
	if restarted.Status() != "authorizationDenied" {
		t.Fatal("restart revived a denied credential")
	}
	if err := restarted.Reconcile(t.Context()); err == nil || restarted.Status() != "authorizationDenied" {
		t.Fatal("revoked credential became usable")
	}
}

func TestPendingOperationCannotChangeBackendOrTLSBinding(t *testing.T) {
	f := fixture(t)
	store := f.store(t)
	e := f.open(t, store)
	e.Close()
	declaration, _ := app.Declaration("test-build")
	for _, change := range []string{"backend", "declaration"} {
		c := f.config
		decl := declaration
		switch change {
		case "backend":
			c.BackendURL = "https://different.home.arpa"
		case "declaration":
			decl, _ = app.Declaration("changed-build")
		}
		other, err := New(c, store, decl, slog.New(slog.NewJSONHandler(io.Discard, nil)))
		if other != nil {
			other.Close()
		}
		if err == nil {
			t.Fatalf("pending proof silently changed %s", change)
		}
	}
}

func TestCancelledPairingDoesNotDispatchAndLogsAreRedacted(t *testing.T) {
	f := fixture(t)
	e := f.open(t, f.store(t))
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := e.Reconcile(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled exchange was dispatched")
	}
	complete, acks, _ := f.counts()
	if complete != 0 || acks != 0 {
		t.Fatal("cancelled exchange reached authority")
	}
	for _, v := range []any{e.doc, *e.doc.Pending, f.provision, f.intent} {
		text := fmt.Sprintf("%v %#v", v, v)
		if strings.Contains(text, f.intent.Secret) || strings.Contains(text, f.provision.ServiceCredential) {
			t.Fatal("ordinary formatting exposed trust secrets")
		}
	}
}

func TestSigningKeysRejectPrivateMaterialAndChangedIssuer(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	kid, alg, use := "key_test", "ES256", "sig"
	pub := noxmcp.SuiteSigningKey{Kty: "EC", Crv: "P-256", X: base64.RawURLEncoding.EncodeToString(key.X.FillBytes(make([]byte, 32))), Y: base64.RawURLEncoding.EncodeToString(key.Y.FillBytes(make([]byte, 32))), Kid: &kid, Alg: &alg, Use: &use}
	keys := KeySet{ContractVersion: "1.0", Issuer: "urn:test", Revision: "revision_test", Keys: []noxmcp.SuiteSigningKey{pub}}
	if err := validateKeys(keys, "urn:test"); err != nil {
		t.Fatal(err)
	}
	if err := validateKeys(keys, "urn:other"); err == nil {
		t.Fatal("untrusted issuer accepted")
	}
	duplicate := keys
	duplicate.Keys = append(append([]noxmcp.SuiteSigningKey(nil), keys.Keys...), pub)
	if err := validateKeys(duplicate, "urn:test"); err == nil {
		t.Fatal("duplicate signing key accepted")
	}
	keys.Keys[0].Extra = map[string]json.RawMessage{"d": json.RawMessage(`"private"`)}
	if err := validateKeys(keys, "urn:test"); err == nil {
		t.Fatal("private signing material accepted")
	}
}

func TestClockRollbackBlocksManagementBeforeDispatch(t *testing.T) {
	f := fixture(t)
	e := f.open(t, f.store(t))
	e.doc.UpdatedAt = time.Now().Add(time.Minute).UnixMilli()
	if err := e.Reconcile(t.Context()); !errors.Is(err, ErrClock) {
		t.Fatal("rollback admitted service management")
	}
	complete, acks, _ := f.counts()
	if complete != 0 || acks != 0 {
		t.Fatal("rollback reached the Backend")
	}
}

func TestVerifiedAuthorityDoesNotWaitForReconciliationLock(t *testing.T) {
	f := fixture(t)
	e := f.open(t, f.store(t))
	if err := e.Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	// Network reconciliation owns this lock. Cached, verified authority must
	// remain readable so a slow Backend cannot stall all unrelated requests.
	e.mu.Lock()
	done := make(chan error, 1)
	go func() { _, _, err := e.Authority(); done <- err }()
	select {
	case err := <-done:
		e.mu.Unlock()
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		e.mu.Unlock()
		<-done
		t.Fatal("authority read waited for network reconciliation")
	}
}
