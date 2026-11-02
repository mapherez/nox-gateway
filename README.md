# NoX Gateway

Headless Go service for authorized NoX Suite MCP discovery and routing. Backend
owns identities, permissions, service trust and registration. Yard owns deployment
and initiates product registration. Products own their tools, data and domain rules.
Gateway consumes their declarations and metadata without a built-in product catalog.

## Install and connect

Backend and participating Suite services run on **one host**. The supplied Compose
mounts the local Docker volume `nox-suite-local`, shared with Backend and other
participating services. It creates no extra service. This is a local-volume channel,
not a network share; do not mount it into unrelated containers or frontend containers.
The container publishes only the native client port, default **5711 → 8080**.

After publishing an image, deploy `docker-compose.yml` through the existing deployment
manager or `docker compose up -d`. To build this checkout locally instead:

```sh
docker compose -f docker-compose.dev.yml up -d --build
```

In the existing **Backend dashboard → Connections → Gateway**, enter the Gateway's
native address, for example `http://host.example:5711` or an existing HTTPS origin,
then **verify and confirm**. Backend verifies same-host presence through the shared
socket volume and automatically delivers pairing. Gateway durably stores its proof
and issued service credential before ACK. Backend completes registration. Interrupted
setup is resumable through that dashboard; no pairing file or credential copying.

The supplied Backend Compose and Gateway Compose use the same local volume name.
If an installation customizes it, use the same `NOX_SUITE_LOCAL_VOLUME_NAME` in both
projects. URL acceptance alone never proves same-host presence or grants access.

**HTTP native URLs work. Certificates, CIDR lists, IP pins and manual protocol JSON
are not Suite setup requirements.** Optional HTTPS for browser/client access is a
separate deployment choice. Remote clients use the deployment's normal access path;
Suite service communication remains local even when clients connect remotely.

The Gateway adapter is implemented against published SDK **0.7.2**, contract **1.0**,
and tested against actual Backend **v1.1.1**. Yard adaptation, other participating
products, the CLI and the complete installed Suite journey remain subsequent work.
An existing product link in Backend is not automatically an enrolled MCP product.

## Client endpoint and diagnostics

Configure an MCP client with the Gateway's native **`/mcp`** URL and the existing
Backend personal API key as `Authorization: Bearer …`. Do not place keys in URLs.
No new Gateway account or product enrollment is required from the client.

| Endpoint | Purpose |
| --- | --- |
| `GET /healthz` | Process liveness, build and contract versions; 200 even before pairing. |
| `GET /readyz` | 200 after live Backend service verification and registry synchronization; otherwise 503 with a non-secret connection state. |
| `GET /nox/integration` | Public, non-secret declaration. Gateway never declares itself as an aggregate source. |
| `POST /mcp` | Authenticated conventional MCP and negotiated `nox/discovery`; an empty catalog still validates the key. |

`/nox/integration/pairing` exists **only on the local Unix listener**, not the TCP
listener. Client-facing TLS or forwarded headers cannot expose that receiver.
Logical service origins such as `http://nox-backend` select sockets inside the local
channel; they are neither DNS destinations nor addresses for a browser to visit.
There is no TCP fallback for service-to-service communication.

Product aliases follow the shared contract (`product-id__tool_name`, with its
collision handling). Discovery preserves schemas, policy, descriptors and provenance.
The Gateway supports MCP 2025-11-25 and 2026-07-28 JSON exchanges; advanced discovery
is negotiated, with conventional discovery fallback. Standalone SSE/subscriptions
are not advertised. Clients re-read catalogs with private TTL/fingerprint semantics;
there is no fabricated push-notification channel in JSON mode.

## Authorization, availability and execution

The personal key goes only to Backend for authentication/delegation. Products receive
short-lived, product-bound ES256 delegations, never personal keys. Authorization
caches are isolated by credential, authority, policy, association and registration.
Verified grants are reused within their five-minute maximum, minus the expiry safety
margin; on-demand renewal does not require repeated Google login during a long session.

Registry events invalidate affected authority and views. Expired cursors trigger a
full reconciliation. During a Backend outage only a previously admitted non-empty
view with unexpired verified grants can be reused; new keys and expired authority
fail closed. Explicit denial never falls back to cached authority. Restart always
requires live service/registry verification, even when definitions exist on disk.

Background checks use restricted **service probe** grants without a human key or
business tool calls. Each observation has a persistent Backend-issued producer epoch
and monotonic sequence. A failed product does not hide healthy products. Previously
verified definitions can remain marked unavailable within the same unexpired grant.
MCP 2026 checks use authenticated `server/discover`; legacy connections also use `ping`.

Dispatch rechecks the registration, association, current authorized definition and
fingerprints, then forwards the original tool name. One total deadline includes
authorization and discovery. Cancellation/disconnect/shutdown reaches the downstream
request. **Tool calls are never automatically retried.** A lost result after possible
execution reports `EXECUTION_OUTCOME_UNCONFIRMED`, `retryable: false` and unknown
outcome; a disconnect is not evidence that a write was rolled back.

## Configuration

Go execution requires Go **1.26+** on Linux/macOS. Docker needs no host Go/Node.
Node **24+** runs release tooling; Python **3.9+** runs verification scripts. The
binary does not load `.env`; Compose does. See [.env.example](.env.example).

| Variable | Default | Meaning |
| --- | --- | --- |
| `NOX_GATEWAY_LISTEN_ADDR` | `127.0.0.1:8080` | Native bind; image uses `0.0.0.0:8080`. |
| `NOX_GATEWAY_DATA_DIR` | `./data` | Private persistent state; image uses `/data`. |
| `NOX_GATEWAY_SUITE_SOCKET_DIR` | empty outside Docker | Shared same-host channel; image uses `/run/nox-suite`. |
| `NOX_GATEWAY_PUBLIC_URL` | empty | Optional native origin for browser Origin validation; no application path prefix. |
| `NOX_GATEWAY_LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error`. |
| `NOX_GATEWAY_SHUTDOWN_TIMEOUT_MS` | `10000` | Drain limit, 1–60000 ms. |
| `NOX_GATEWAY_CALL_TIMEOUT_MS` | `20000` | Total request budget, 10000–3600000 ms; downstream products may impose tighter limits. |
| `NOX_GATEWAY_MAX_CONCURRENT` | `32` | Maximum active client requests and downstream connections, 1–256. |
| `NOX_GATEWAY_MAX_PAYLOAD_BYTES` | `2097152` | Input/result and source-envelope bound, 1024–2097152 bytes; shared SDK bounds remain in force. |
| `NOX_GATEWAY_PROBE_INTERVAL_MS` | `15000` | Probe cadence, 1000–3600000 ms; Backend controls observation freshness independently. |
| `NOX_GATEWAY_TLS_CERT_FILE`, `NOX_GATEWAY_TLS_KEY_FILE` | empty | Optional direct native TLS; configure both with an HTTPS public origin. |

Compose also accepts `NOX_GATEWAY_PORT` (5711), `NOX_GATEWAY_IMAGE_TAG` (`latest`),
`NOX_GATEWAY_DATA_VOLUME_NAME` and `NOX_SUITE_LOCAL_VOLUME_NAME`. Production and dev
Compose use different private data volumes. For bare development:

```sh
go run ./cmd/nox-gateway
# For a bare same-host Suite, both processes must use the same private socket directory:
NOX_GATEWAY_SUITE_SOCKET_DIR=/absolute/shared-directory go run ./cmd/nox-gateway
```

A bare process without the local channel serves diagnostics and declaration but
cannot become ready. Containers set up the channel through their Compose volume.
No hostname, IP, DNS domain or certificate from a particular installation is embedded
in the service. See [optional native HTTPS](docs/https-setup.md) only if desired.

## Persistence and recovery

Back up the **complete private data volume** while the service is stopped. Files
are 0600 inside a 0700 directory. One process exclusively owns it; corrupt state or
changed trust fails without silently regenerating identity. `identity.json` preserves
the discovery server ID. `trust.enc` and its private master key preserve service
credentials and the recovery journal; keep them together. `registry.json` caches
non-secret registry and observation producer state; it is never authorization.
Personal API keys and delegation/probe tokens remain memory-only. Logs contain
sanitized lifecycle events, not request bodies, credentials or raw transport errors.

The shared socket volume is **not the private data volume**. Images use the common
Suite group 10001, Gateway user 10002, a setgid/sticky shared directory and 0660
sockets. Per-service locks prevent two processes from owning one socket. Restart
reclaims only a stale owned endpoint. Do not use `compose down -v` to update services.

Stop the daemon before explicit offline maintenance, then run the same binary with
its existing data/channel settings and `--service-operation=rotate`,
`revoke-credential` or `revoke-service`. These operations journal their protocol keys
and recover lost responses without issuing arbitrary new credentials. Normal setup
uses the Backend dashboard, not these maintenance commands. Old network/TLS service
bindings are rejected rather than silently converted into local associations.

## Verification and release

```sh
sh scripts/check.sh
docker build --build-arg VERSION=local-validation -t nox-gateway:local-validation .
python3 scripts/check-image.py --image nox-gateway:local-validation --version local-validation
```

Checks cover dependency lock, vet/race tests, release tooling, optional certificate
helper, process signals/recovery, authorization isolation, failure/cancellation and
packaging. All services/data created by the acceptance/image scripts are disposable.
They do not modify installed Backend/Yard services or the host trust store.

CI additionally builds the actual Backend at pinned **v1.1.1**, then requires the
[automatic local setup and MCP journey](scripts/check-backend.py). To repeat it with
independently built binaries:

```sh
python3 scripts/check-backend.py --backend /absolute/path/nox-backend --gateway "$PWD/bin/nox-gateway"
```

CI runs Linux minimum/stable Go, macOS stable Go, Linux amd64/arm64 cross-compilation,
Docker image/recreation and actual Backend acceptance. Generic SDK participants in
unit tests exercise protocol behavior; they are test fixtures, not product proposals.

`npm run release` prompts for a SemVer, validates Go and the built image, commits only
the version and pushes commit/tag atomically. It requires a clean committed tree,
a configured Git origin, Go, Node, Python and running Docker. `package.json` is private
release bookkeeping, not a Node runtime or npm package; `0.0.0` denotes no release
chosen yet. Local unversioned binaries report `dev`.

The tag workflow requires all CI jobs, validates the manifest/tag, publishes GHCR
images for **linux/amd64 and linux/arm64**, then creates a GitHub Release. Stable tags
also update `latest`; prereleases do not. Existing GitHub releases are not replaced.
No release is created by running the check scripts.

## References

- [Implementation record and scope](docs/implementation-plan.md)
- [Same-host integration](https://github.com/mapherez/nox-mcp/blob/master/Documents/SUITE_SAME_HOST_INTEGRATION.md)
- [Suite implementation plan](https://github.com/mapherez/nox-mcp/blob/master/Documents/NOX_SUITE_IMPLEMENTATION_PLAN.md)
- [Gateway contract](https://github.com/mapherez/nox-mcp/blob/master/Documents/GATEWAY_CONTRACT.md)
- [SDK integration](https://github.com/mapherez/nox-mcp/blob/master/Documents/INTEGRATION.md)
