# Gateway implementation sequence

This sequence implements the existing NoX Suite contract. It is not a set of
provisional product versions and does not reopen approved decisions. Implement,
validate, and stop after each numbered point for the owner's OK to continue.
Tests and CI evolve with every point; delivery checks are not postponed to the end.

| Point | Deliverable | Status |
| --- | --- | --- |
| 1 | Go executable, published SDK v0.7.0, configuration, persistent discovery identity, private/exclusive state, diagnostics, lifecycle logs, cancellation/shutdown, tests and CI. | Implemented; local verification recorded below. |
| 2 | HTTPS and secure Backend pairing, trusted target validation, resumable provisioning and persisted credentials before ACK, credential rotation/revocation, restart recovery, `/nox/integration`. Include the owner's internal CA, certificate issuance/renewal, Nginx Proxy Manager `home.arpa` hosts, client/container trust, and validation from LAN/Twingate with the owner. | Pending. |
| 3 | Backend registry snapshot, long-poll events, transactional application/invalidation, cursors/reconciliation, safe removal/replacement handling. Backend remains authoritative. | Pending. |
| 4 | Personal-key admission and verified per-product delegation, bounded memory caches, user/key/view isolation, on-demand renewal, revocation and outage behavior. | Pending. |
| 5 | Trusted product connections, user-independent restricted probes, readiness observations, persistent sequence/epoch handling, reconnects and failure isolation. | Pending. |
| 6 | Conventional/incremental private discovery, aggregation, collision-safe aliases, auxiliary references, fingerprints/snapshots, client MCP endpoint and negotiated notifications, authenticated empty catalogs, no self-aggregation. | Pending. |
| 7 | Authorized dispatch to original tools, definition/association checks, shrinking total deadline, global capacity/payload bounds, cancellation, correct results/errors and uncertain-write handling without automatic tool retries. | Pending. |
| 8 | Docker/Compose/persistence, HTTPS deployment documentation, version/release scripts and workflows, required packaging checks and installed validation, real Backend integration plus generic conforming protocol participants and failure paths. | Pending. |

## Approved deployment and ownership

Gateway is a headless Go service, without a React/Vite frontend. Native Backend
administration provides the foundation connection configuration. One Backend
administers the environment. Product IDs and tools are discovered through the
contract; no product-specific routes, business schemas, or catalog allowlist.

Gateway and Yard use internal HTTPS `home.arpa` origins. Remote access uses
Twingate; no public URL or Cloudflare Tunnel is required for them. Point 2
includes actual homelab HTTPS configuration with the owner, not only code support.
No homelab configuration or certificate installation has been performed at point 1.
Deployment origins have no application prefixes such as `/apps/name`.

The Backend retains authority for users, roles, API keys, registry and service
grants. Products execute tools and enforce domain ownership. Personal API keys
never reach products. Delegations reuse authority for at most five minutes,
with the contract's expiry safety margin, invalidation and outage rules; idle
multi-hour sessions do not repeat Google login. Persisted Gateway data grants
no authorization after restart.

## Point 1 completion and verification

Implemented executable `cmd/nox-gateway`, SDK pin without local replacements,
validated settings, JSON lifecycle logs and static/sanitized failure messages.
Only local diagnostics are mounted. `/healthz` is liveness; `/readyz` remains
503 until the implemented Gateway can actually admit integrated traffic.

Local storage contains only the persistent discovery identity and OS-held
process lock. Atomic identity creation, strict bounded parsing, private modes,
restart preservation and failure without regeneration are covered. Service
credentials, registry persistence and discovery/authorization caches follow
their own implementation points.

Required local validation is `sh scripts/check.sh`: configuration/error
redaction, strict/corrupt identity handling, permission/symlink/lock checks,
HTTP method and readiness semantics, active cancellation, bounded drain,
executable signals, crash/restart identity recovery, and no request-secret logs.
CI repeats this on Linux/macOS; deployment targets cross-compile for Linux
amd64/arm64. Remote CI, packaging and installed integration are distinct evidence.

Local results on **2026-11-01**:

- Full `scripts/check.sh` passed on macOS arm64 with Go **1.27.3** and **1.26.0**:
  formatting, unchanged dependency lock, module verification, vet, 13 top-level
  race tests with table/subtest coverage, binary build and real process smoke.
- The binary smoke passed SIGTERM/SIGINT shutdown, SIGKILL recovery, unchanged
  identity after restart, exclusion of a concurrent second process, expected
  HTTP health/readiness responses, and absence of request secrets from logs.
- Linux **amd64** and **arm64** builds passed with Go 1.26.0 and CGO disabled.
  These are cross-compilation checks, not claims of Linux runtime execution.
- `actionlint` **1.7.7** accepted the workflow; Python syntax and local document
  links were checked. Remote GitHub CI has not been run from this local repo.

No installed Backend/Yard data, homelab proxy configuration, or certificates
were changed. Point 1 ends here; point 2 awaits the owner's OK.

## Subsequent acceptance

Each point records the concrete behavior delivered, commands executed and any
remaining external validation before stopping. Step 8 must exercise the actual
implemented Gateway against Backend over trusted HTTPS; protocol participants
must be identified as test fixtures, not invented Suite products or claims that
unadapted products already work. Adaptations of Yard/other products, CLI and
Portal are separate scopes under the same approved contracts.
