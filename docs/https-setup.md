# HTTPS deployment and reversible local certificate setup

Gateway consumes deployment-provided TLS. It does not operate a certificate
authority, install root certificates, configure DNS, or manage a reverse proxy.
Any installation can use its own valid HTTPS origins, private/public CA, or an
explicitly provisioned Backend SPKI pin. `home.arpa` is an example of an internal
DNS deployment, not a Gateway domain restriction. No public Internet exposure or
Cloudflare Tunnel is required for internal HTTPS.

## Choose a transport boundary

- **Direct TLS:** configure `NOX_GATEWAY_PUBLIC_URL`, `NOX_GATEWAY_TLS_CERT_FILE`
  and `NOX_GATEWAY_TLS_KEY_FILE`. Mount the leaf certificate/chain and its private
  key; the key must be a regular owner-private file (0600/0400). The certificate
  must be valid for the configured hostname. Same-key renewal is reloaded on new
  TLS handshakes; a new key requires explicit trust reconciliation.
- **TLS reverse proxy:** configure the HTTPS public origin, exact trusted
  immediate proxy IPs in `NOX_GATEWAY_TRUSTED_PROXY_CIDRS`, and the actual external
  leaf certificate in `NOX_GATEWAY_PUBLIC_TLS_CERT_FILE`. The proxy terminates TLS
  and overwrites `X-Forwarded-Proto` with its verified scheme. Restrict the
  plaintext internal hop to the trusted boundary. A client-supplied header or
  a private IP alone never establishes HTTPS trust.

For outgoing Backend communication, set its canonical HTTPS origin in
`NOX_GATEWAY_BACKEND_URL`, explicitly approved networks/ports in
`NOX_GATEWAY_SUITE_ALLOWED_CIDRS` and `NOX_GATEWAY_SUITE_ALLOWED_PORTS`, and an extra
CA bundle with `NOX_GATEWAY_TLS_CA_FILE` when needed. An administrator-provisioned
`NOX_GATEWAY_BACKEND_TLS_PIN` is an alternative explicit SPKI trust mode. It is
not an automatic fallback after failed PKI validation. Redirects and environment
HTTP proxies are rejected/ignored for credential-bearing requests.

## Optional home.arpa certificate helper

`scripts/homelab-ca.py` is optional deployment tooling using existing local
OpenSSL/LibreSSL and Python, independent of the Gateway executable. It creates
an encrypted root private key, root certificate and named leaf outputs. It
supports only internal `home.arpa` issuance; the Gateway itself accepts any
contract-valid configured HTTPS origin and certificates from other tools/CAs.
No additional application or permanent CA service is installed by the helper.

Pick **one private directory outside every Git repository**, for example
`~/nox-suite-tls`. Keep the helper and CA data there on the issuing host. With
`homelab-ca.py` already copied into that directory:

```sh
python3 ~/nox-suite-tls/homelab-ca.py --directory ~/nox-suite-tls/ca init
python3 ~/nox-suite-tls/homelab-ca.py --directory ~/nox-suite-tls/ca issue --name home-arpa --dns-name '*.home.arpa'
```

Enter a passphrase of at least 12 characters interactively and keep it in your
own password manager. Never send it in chat, put it in command arguments, or
upload the root private key to a proxy. `init` refuses to overwrite an existing
CA. If an earlier setup exists, reuse its CA and original leaf key rather than
creating a duplicate.

The resulting directory is:

```text
nox-suite-tls/
  homelab-ca.py
  ca/
    root.key                     encrypted CA signing key; keep private
    root.crt                     root certificate for client trust
    issued/home-arpa/
      key.pem                    private server key for the proxy
      cert.pem                   server certificate
      fullchain.pem              certificate plus root
```

`*.home.arpa` covers names such as `gateway.home.arpa` and `yard.home.arpa`,
but not the bare `home.arpa` or names with multiple labels such as
`api.gateway.home.arpa`. Supply separate `--dns-name` arguments if needed.
The CA certificate lasts ten years; leaf certificates last **90 days**.

## Nginx Proxy Manager and client trust

1. Transfer only `root.crt`, `cert.pem` and `key.pem` to one private import folder
   on the computer running the browser. Keep mode 0600 on `key.pem`. The root
   private key stays on the issuing host.
2. In NPM, open **SSL Certificates → Add SSL Certificate → Custom**. Use a clear
   name such as `Homelab home.arpa`; import `cert.pem` as Certificate and `key.pem`
   as Certificate Key. This CA signs leaves directly, so there is no intermediate
   certificate to import. NPM stores its own managed copy.
3. Edit each intended Proxy Host, select that certificate under **SSL**, and
   enable **Force SSL** when the HTTPS check succeeds. Existing HTTP upstream
   destinations can stay on the explicitly trusted internal hop. Use port 443
   on the proxy and normal internal DNS resolution to that proxy.
4. On each client, import **only `root.crt`** into its trust store. On macOS,
   use **Keychain Access → login → File → Import Items**, then open the imported
   `Homelab Internal CA`, expand Trust and trust it for SSL. User-level trust
   limits the change to that user's account. Authenticate the local change when
   macOS requests it. Other browsers/platforms may use their own trust store.
5. Confirm the root certificate fingerprint against the helper's initialization
   output before trusting it. Open a configured HTTPS hostname and check that
   its certificate is accepted without bypassing a warning. Check through the
   normal remote network access as well when applicable.

Changing DNS resolution, importing a certificate, or trusting a root does not
expose a service publicly. Keep the existing firewall/Twingate access policy.
The temporary browser import folder can be deleted after successful import;
NPM and the client's trust store now contain their managed copies.

## Renewal

The helper uses **manual renewal**. Run the same `issue` command before the
90-day certificate expires; it reuses the existing `key.pem` so the SPKI pin does
not change. Import the renewed certificate into the same NPM certificate entry
and update any Gateway public-certificate file mount/copy. A root change is not
needed for normal renewal.

```sh
python3 ~/nox-suite-tls/homelab-ca.py --directory ~/nox-suite-tls/ca check --name home-arpa
```

`check` reports whether renewal is due within the next 30 days and returns a
nonzero exit status when due. There is no automatic renewal or notification
service installed. Keep the issuer directory and passphrase while certificates
are in use; they are deployment configuration needed for subsequent renewals.

## Backend trust boundary and pairing follow HTTPS

HTTPS accepted by a browser is one deployment check. Backend Suite routes also
require their configured trusted ingress, approved outgoing targets and canonical
origin; a padlock alone does not complete this configuration. Inspect the actual
immediate proxy peer rather than trusting an entire Docker subnet by assumption.
Keep native/direct product access and existing data intact.

Once HTTPS and the Backend boundary are verified, export the administrator's
Gateway pairing intent to a private file and configure `NOX_GATEWAY_PAIRING_FILE`.
The executable persists its proof/operation keys before sending and stores the
issued service credential before acknowledging. The CA root and Backend's
Suite signing keys have separate purposes. Browser trust does not replace
Backend service trust or authenticate an MCP call.

The configured Backend origin must agree with its provisioning response and
`NOX_BACKEND_PUBLIC_URL`. Review the existing Google OAuth callback before
changing that setting; the callback must remain authorized by Google. HTTPS setup
and pairing are not yet certification of registry/aggregation/execution points.

## Removal and cleanup

Folder deletion alone does not remove installed certificate trust. To undo this
setup completely:

1. Restore the previous SSL settings on affected Proxy Hosts (including Force
   SSL), then remove the unused custom certificate from NPM. Remove newly created
   proxy hosts/DNS entries if they were only for this setup.
2. Remove the specific imported root certificate from each client's trust store,
   including the login keychain on every Mac where it was imported.
3. If a Gateway was paired, revoke its service association in Backend and stop
   it before removing its private state. Deleting local state does not revoke a
   Backend credential or unregister a product.
4. Delete the private issuing directory only after retiring the certificates it
   issued; remove any remaining browser import folder and transferred helper
   copy. Existing NPM/Backend containers and their unrelated volumes remain.

Do not delete NPM's shared data directory or the Backend database/volume to clean
up this certificate setup. The optional helper creates no launch agent, daemon,
cron entry, global package installation, or system trust changes automatically.

## References

- [Nginx Proxy Manager: custom SSL certificates](https://nginxproxymanager.com/guide/)
- [Apple: add certificates to a keychain](https://support.apple.com/guide/keychain-access/kyca2431/mac)
- [Google OAuth redirect URI rules](https://developers.google.com/identity/protocols/oauth2/web-server#redirect-uri-validation-rules)
