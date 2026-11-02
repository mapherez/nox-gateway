# Gateway implementation sequence and correction record

This record implements the existing NoX Suite plan and the owner's explicit
same-host clarification. It does not replace or modify the original MCP plan.
Backend is the authority, Yard remains the deployment/registration initiator,
and Gateway is a headless Go router for any conforming present or future product.

## Fixed Suite correction points

The owner authorized these **two existing correction points together**, before
returning to Yard. Their original names and numbering are retained:

- **6. Adaptar o Gateway ao mesmo processo de configuração.**
- **9. Retomar os restantes pontos do Gateway.**

Execution order is **6 + 9 → 7 → 8 → 10 → 11 → 12**. Points 7/8 concern Yard and
its lifecycle; point 10 adapts participating products, point 11 implements the
CLI and point 12 validates the complete Suite journey/delivery. Completing Gateway
source and local acceptance does not mean those other components are implemented.

## Earlier Gateway work breakdown

These are the Gateway repository's earlier eight deliverables, not a renumbering
of the Suite correction sequence. HTTPS-only service transport and manual pairing
files in the old point 2/8 descriptions were superseded by the same-host correction.
The optional certificate helper remains independent; it is not a setup requirement.

| Point | Gateway deliverable | Current implementation |
| --- | --- | --- |
| 1 | Go executable, SDK, configuration, persistent discovery identity, private/exclusive state, diagnostics, lifecycle, tests and CI. | Implemented; SDK upgraded to published 0.7.2. |
| 2 | Secure Backend pairing, resumable provisioning, persisted credential before ACK, rotation/revocation, restart recovery and declaration. | Implemented over the shared local socket channel, delivered automatically from Backend configuration. No manual certificate/pairing file workflow. |
| 3 | Backend snapshot, long-poll events, atomic persistence/invalidation, cursors/reconciliation, removal/replacement. | Implemented; cached registry is not authority on restart. |
| 4 | Personal-key admission, verified product delegations, bounded private caches, expiry/renewal/revocation/outage behavior. | Implemented; explicit denial has no stale fallback. |
| 5 | Product connections, restricted service probes, observations, persistent sequence/epoch, reconnect and source isolation. | Implemented; authenticated stateless discovery for MCP 2026 and legacy ping. |
| 6 | Conventional/incremental private discovery, aliases, references, fingerprints, client MCP, authenticated empty catalogs, no self-aggregation. | Implemented with the published SDK; JSON exchanges/private TTL and re-read semantics, no standalone SSE subscription advertisement. |
| 7 | Authorized dispatch, definition/association checks, shrinking deadline, capacity/payload bounds, cancellation and uncertain writes without retries. | Implemented; native client cancellation is preserved across MCP context boundaries. |
| 8 | Docker/Compose, persistence, documentation, release tooling/workflows, packaging checks and actual Backend integration. | Implemented locally; GitHub CI/publication and installed deployment are external results, not claimed by local tests. |

## Local acceptance boundary

The real-service acceptance uses independent Backend **v1.1.1** and Gateway binaries
with temporary state and sockets. It exercises the native Backend **save URL → verify
→ confirm** flow, automatic pairing, authenticated empty catalog, actual Backend MCP
tools, private discovery, administrator/member tool differences, domain data isolation,
service-only readiness observations, restart identity, bounded outage reuse, account
disable, registry removal and absence of personal secrets from Gateway files/logs.

The Backend's own existing MCP adapter supplies a real source in that acceptance.
This does not make Backend/Yard a pilot design or hardcode them in Gateway routing.
Generic SDK-based protocol test participants separately exercise colliding names,
user separation, cancellation, dropped write responses, failed sources, cursor
recovery, persistence failure and request limits. They are test fixtures only.

`sh scripts/check.sh` is the required local source/process check. The image check
creates only disposable Docker containers/volumes. CI requires the actual Backend
journey instead of silently skipping it, runs the minimum/stable Go and OS matrix,
cross-compiles both deployment architectures, and validates the packaged image.
The release workflow depends on that CI before publication.

The original point-1 checks (signals, crash recovery, exclusive state, persistent
identity and sanitized logs) remain part of the required suite. Optional CA-helper
tests remain separate from service trust. No installed Backend/Yard data, homelab
proxy configuration or system trust store is changed by these checks.

The complete installed Suite journey awaits Yard/product/CLI adaptation and point 12.

## Verification recorded on November 2, 2026

- `sh scripts/check.sh`: passed on macOS arm64 with Go 1.27.3 (format, dependency
  lock/module verification, vet/race tests, release tooling, optional CA helper,
  binary lifecycle/signal/crash-recovery smoke).
- Go 1.26.0: full race/vet checks passed; subsequently changed Gateway/trust/command
  layers were checked again. Linux amd64 and arm64 cross-compilation passed.
- Required actual Backend acceptance passed with Backend source at
  `4a4b72b4c3a1cfb85c581f42bb3351d8a4880cea` (v1.1.1), including service probes and
  removal while discovery was active. Only idempotent discovery convergence is
  retried by the harness; business calls are never retried.
- Final local image/recreation checks, both Compose configurations, local document
  links, `git diff --check` and `actionlint 1.7.7` passed.

These results complete local implementation/verification for correction points
6 + 9. GitHub CI, image publication and the owner's installed Gateway check are
still required delivery evidence. No release, commit, tag, push or homelab deployment
was performed by the agent; temporary acceptance containers/volumes are removed.
