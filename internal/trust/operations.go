package trust

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"time"

	"github.com/mapherez/nox-gateway/internal/backend"
	"github.com/mapherez/nox-gateway/internal/buildinfo"
	noxmcp "github.com/mapherez/nox-mcp/go"
)

// Operate is an explicit offline maintenance command: the daemon must release
// its data-directory lock first. No secret is accepted through command arguments.
func (e *Engine) Operate(ctx context.Context, kind string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if kind != "rotate" && kind != "revoke-credential" && kind != "revoke-service" {
		return ErrRecovery
	}
	if e.doc.Pending != nil {
		if e.doc.Pending.Kind != kind {
			return ErrRecovery
		}
		return e.reconcile(ctx)
	}
	if e.doc.Binding == nil || e.doc.Binding.Revoked {
		return ErrRecovery
	}
	view, err := e.readService(ctx)
	if err != nil {
		return e.fail(err)
	}
	revision := view.Service.CredentialRevision
	if kind == "revoke-service" {
		revision = view.Revision
	}
	e.authority.Store(nil)
	e.doc.Pending = &Operation{BackendOrigin: e.cfg.BackendURL, ExpiresAt: time.Now().Add(5 * time.Minute).UTC().Format("2006-01-02T15:04:05.000Z"), Kind: kind, RequestKey: rand.Text(), AckKey: rand.Text(), Revision: revision}
	if err = e.save(); err != nil {
		return err
	}
	return e.reconcile(ctx)
}

func (e *Engine) completeRotation(ctx context.Context) error {
	o := e.doc.Pending
	old := e.doc.Binding.Provisioning
	path := "/api/nox/services/" + backend.Resource(old.Service.ServiceID) + "/credentials"
	if o.Provisioning == nil {
		raw, status, err := e.client.Request(ctx, "POST", path, old.ServiceCredential, o.RequestKey, o.Revision, noxmcp.SuiteCredentialRotation{ContractVersion: buildinfo.ContractVersion, CredentialRevision: o.Revision})
		if err != nil {
			return e.fail(err)
		}
		var response struct {
			ContractVersion   string                      `json:"contractVersion"`
			Service           noxmcp.SuiteServiceIdentity `json:"service"`
			ServiceCredential string                      `json:"serviceCredential"`
			AcknowledgeBy     string                      `json:"acknowledgeBy"`
		}
		if status != 201 || decode(raw, &response) != nil || response.ContractVersion != buildinfo.ContractVersion || validateIdentity(response.Service) != nil || response.Service.ExpiresAt == nil || *response.Service.ExpiresAt != response.AcknowledgeBy || response.Service.ServiceID != old.Service.ServiceID || response.Service.AssociationID != old.Service.AssociationID || response.Service.CredentialID == old.Service.CredentialID || response.Service.CredentialRevision == old.Service.CredentialRevision {
			return ErrResponse
		}
		expiry, err := time.Parse(time.RFC3339Nano, response.AcknowledgeBy)
		if err != nil || !expiry.After(time.Now()) || expiry.After(time.Now().Add(305*time.Second)) {
			return ErrResponse
		}
		next := old
		next.Service = response.Service
		next.ServiceCredential = response.ServiceCredential
		if validateProvisioning(next, e.cfg.BackendURL, old.Issuer) != nil {
			return ErrResponse
		}
		o.Provisioning = &next
		o.ExpiresAt = response.AcknowledgeBy
		if err = e.save(); err != nil {
			return err
		}
	}
	if err := e.save(); err != nil {
		return err
	}
	identity, err := e.acknowledge(ctx, path+"/"+backend.Resource(o.Provisioning.Service.CredentialID)+"/ack", o.Provisioning, o.AckKey)
	if err != nil {
		return e.fail(err)
	}
	e.doc.Binding.Provisioning = *o.Provisioning
	e.doc.Binding.Provisioning.Service = identity
	e.doc.Pending = nil
	if err = e.save(); err != nil {
		return err
	}
	e.logger.Info("gateway_credential_rotated")
	return e.reconcile(ctx)
}

func (e *Engine) completeRevocation(ctx context.Context) error {
	o := e.doc.Pending
	binding := e.doc.Binding
	p := binding.Provisioning
	path := "/api/nox/services/" + backend.Resource(p.Service.ServiceID)
	if o.Kind == "revoke-credential" {
		path += "/credentials/" + backend.Resource(p.Service.CredentialID)
	}
	raw, status, err := e.client.Request(ctx, "DELETE", path, p.ServiceCredential, o.RequestKey, o.Revision, nil)
	var denied *backend.Failure
	if err != nil && !(errors.As(err, &denied) && denied.Denied) {
		return e.fail(err)
	}
	if err == nil {
		var response struct {
			Revoked bool `json:"revoked"`
		}
		if status != 200 || json.Unmarshal(raw, &response) != nil || !response.Revoked {
			return ErrResponse
		}
	}
	// If a previous response was lost, a subsequent denial still proves this
	// credential is unusable. Do not revive it or retry with another credential.
	binding.Revoked = true
	e.doc.Pending = nil
	if saveErr := e.save(); saveErr != nil {
		return saveErr
	}
	e.status.set("revoked")
	e.logger.Info("gateway_service_access_revoked")
	if err != nil && o.Kind == "revoke-service" {
		return ErrRecovery
	}
	return nil
}
