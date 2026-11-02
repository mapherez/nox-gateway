# Optional native HTTPS and reversible certificate helper

Suite pairing and service communication use the same-host local channel. **No
certificate setup is required for integration.** This page is only for an operator
who independently wants HTTPS on native browser/client addresses. See the main
[README](../README.md) for normal installation and automatic Backend setup.

Gateway does not manage DNS, a reverse proxy or a certificate authority. Native
HTTPS can terminate at an existing proxy forwarding HTTP to the Gateway's published
port, with `NOX_GATEWAY_PUBLIC_URL` set to its external HTTPS origin for browser
Origin validation. Forwarded headers do not establish Suite service trust.

Alternatively configure direct native TLS with `NOX_GATEWAY_PUBLIC_URL`,
`NOX_GATEWAY_TLS_CERT_FILE` and `NOX_GATEWAY_TLS_KEY_FILE`. Keep the private key 0600
or 0400. The certificate must match the configured hostname. Same-key renewal is
reloaded at handshake; a new key requires an explicit Gateway restart. This native
certificate is not part of the local pairing binding.

## Optional portable certificate helper

`scripts/homelab-ca.py` is a **standalone file** using existing Python 3.9+ and
OpenSSL/LibreSSL on macOS/Linux. Copy only that file into the folder where you
want to keep the CA and certificates, outside Git repositories. The folder must
already exist; the script never creates or changes permissions on the base folder.

Run the copied script with one command:

```sh
python3 /path/to/your-folder/homelab-ca.py
```

It asks only for DNS names, for example `*.home.arpa` or a comma-separated list.
Enter accepts the displayed default: `*.home.arpa` initially, or the saved names
on later runs. It automatically creates a strong random passphrase, an encrypted
CA key, the CA certificate, the server key/certificate, and a usage/cleanup guide.
The passphrase is saved in a private file and is not printed or put in process
arguments. No package, service, system trust entry, DNS or proxy setup is installed.

For unattended use, or another separately named certificate set:

```sh
python3 /path/to/your-folder/homelab-ca.py --dns-name '*.home.arpa'
python3 /path/to/your-folder/homelab-ca.py --name other --dns-name service.internal.example
```

DNS names are configurable and are not restricted to home.arpa. This helper
accepts DNS names, not IP SANs. The Gateway independently accepts any contract-valid
native HTTP/HTTPS origin and certificates supplied by other tools or authorities.

The script always writes **beside its own physical file**, even when launched
from another working directory. Temporary OpenSSL files are also created there
and removed after each normal or failed run. An OS-held directory lock prevents
simultaneous runs without leaving a lock file. No absolute paths are saved in the
certificate-set metadata or generated instructions.

```text
your-folder/
  homelab-ca.py
  COMO-USAR-CERTIFICADOS.md        generated import/renewal/cleanup instructions
  ca/                            private subfolder (0700)
    passphrase.txt               automatically generated secret (0600)
    root.key                     encrypted CA signing key (0600)
    root.crt                     CA certificate for client trust
  certificates/server/           default certificate set, private subfolder
    key.pem                      private server key (0600)
    cert.pem                     certificate for the proxy
    fullchain.pem                certificate plus root
    details.json                 DNS names, relative/portable state
```

Existing CA material and server keys are reused on reruns. Missing CA files,
missing existing server keys, mismatched keys, unsafe secret permissions and
symlinks cause an error without silently creating replacement authority.
The generated passphrase and encrypted CA key live together: protect the entire
folder. Only `root.crt` is installed on clients; only the server certificate/key
are supplied to the proxy. Never upload `root.key` or `passphrase.txt` there.

To continue using the **same CA on another machine, copy the entire folder**,
preserving its private file permissions. Copying only the script creates a new
CA and therefore requires establishing client trust in that new CA.

`*.home.arpa` covers names such as `gateway.home.arpa` and `yard.home.arpa`,
but not bare `home.arpa` or names with multiple labels such as
`api.gateway.home.arpa`. Include additional names when needed. The root lasts ten
years; server certificates last **90 days**.

## Nginx Proxy Manager and client trust

1. Transfer only `root.crt`, `cert.pem` and `key.pem` to one private import folder
   on the computer running the browser. Keep mode 0600 on `key.pem`. The root
   private key stays on the issuing host.
2. In NPM, open **SSL Certificates → Add SSL Certificate → Custom**. Use a clear
   name such as `Homelab home.arpa`; import `certificates/server/cert.pem` as Certificate and `certificates/server/key.pem`
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

Run the same script again and accept the saved names (Enter). It automatically
uses the saved passphrase and preserves the CA/server keys, then issues a fresh
90-day certificate. Re-import the renewed certificate into the same NPM entry
and update any direct native Gateway certificate mount. Same-key renewal preserves
the current native TLS key. The script does not install a scheduled renewal or notification job.

To inspect the current certificate without renewing:

```sh
python3 /path/to/your-folder/homelab-ca.py --check
```

`--check` reports whether renewal is due within 30 days and returns nonzero when
due. Add `--name other` for another named certificate set. Keep the folder while
its certificates are in use: it contains the authority needed for later renewals.

## Removal and cleanup

Folder deletion alone does not remove installed certificate trust. To undo this
setup completely:

1. Restore the previous SSL settings on affected Proxy Hosts (including Force
   SSL), then remove the unused custom certificate from NPM. Remove newly created
   proxy hosts/DNS entries if they were only for this setup.
2. Remove the specific imported root certificate from each client's trust store,
   including the login keychain on every Mac where it was imported.
3. Removing optional native HTTPS does not require unpairing Gateway or deleting
   its state. Restore its HTTP native URL in Backend configuration if it changed.
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
