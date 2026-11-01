# NoX Gateway

Independent Go service for NoX Suite MCP aggregation and authorized routing.
The Backend remains the authority for identity, service trust, permissions, and
product registration. Products own their tools, data, and domain rules. Gateway
must work with any conforming product without a built-in product catalog.

## Implementation status

Implementation point **1 of 8** supplies the process foundation: configuration,
private persistent discovery identity, exclusive local state, JSON lifecycle
logs, diagnostics, cancellation/shutdown, tests, and CI. No frontend is required.

**This is not yet a functioning MCP Gateway.** HTTPS/pairing, registry/events,
authorization, product connections, aggregation, and execution follow the
[implementation sequence](docs/implementation-plan.md). `/readyz` deliberately
returns 503 until integrated admission can be implemented and validated. No
personal API key, service credential, or Backend URL is accepted at this point.

## Local development

Requires Go **1.26.0 or newer**, Linux or macOS. Python 3.10+ runs the binary
smoke check; Make is an optional convenience. No Node, Rust, Docker, sibling
checkout, local SDK replacement, or schema generation is needed for point 1.
The published dependency is pinned to NoX MCP **v0.7.0**, Suite contract **1.0**.

```sh
go mod download
go run ./cmd/nox-gateway
```

The default bind is `127.0.0.1:8080`, for **local HTTP diagnostics only**. No
public exposure or Cloudflare Tunnel is needed. Do not send credentials to
this point's HTTP listener. Internal `home.arpa` HTTPS is part of point 2.

In another terminal:

```sh
curl -i http://127.0.0.1:8080/healthz
curl -i http://127.0.0.1:8080/readyz
```

- `/healthz`: 200, process liveness and build/contract versions.
- `/readyz`: 503, `GATEWAY_NOT_CONFIGURED`; this is expected at point 1.
- Only GET/HEAD diagnostics exist. `/mcp` and `/nox/integration` are not mounted.

For signal testing, build and run the executable directly:

```sh
make build
./bin/nox-gateway --version
./bin/nox-gateway
```

Ctrl+C or SIGTERM stops the listener, cancels active request contexts, and drains
handlers within the configured deadline. A handler that ignores cancellation
has its connection closed at that bound and the process reports failed drain.
Use the compiled executable for process supervision, rather than the `go run`
wrapper.

## Configuration

Settings are environment variables. `.env.example` is a reference; the binary
**does not automatically load `.env` files**. Existing explicitly set empty
values are rejected, not silently replaced by defaults.

| Variable | Default | Meaning |
| --- | --- | --- |
| `NOX_GATEWAY_LISTEN_ADDR` | `127.0.0.1:8080` | Bind IP/localhost and numeric port; `:0` selects an OS-assigned port. |
| `NOX_GATEWAY_DATA_DIR` | `./data` | Private local state directory, relative to the working directory unless absolute. |
| `NOX_GATEWAY_LOG_LEVEL` | `info` | `debug`, `info`, `warn`, or `error`. |
| `NOX_GATEWAY_SHUTDOWN_TIMEOUT_MS` | `10000` | Maximum drain time, 1–60000 ms. |

For example, to avoid a port already in use:

```sh
NOX_GATEWAY_LISTEN_ADDR=127.0.0.1:8088 ./bin/nox-gateway
```

## Local state and logs

The service creates its data directory with mode 0700 and files with mode 0600.
Existing directories/files must already be private. The final data-directory
component and the managed files cannot be symlinks. One running Gateway owns
that directory at a time; a second process fails before listening. The OS
releases the lock even after an abrupt process death.

`identity.json` contains a schema version and random persistent discovery
`serverId`. It is written using file sync, atomic rename, and directory sync.
Ordinary restart preserves it. Invalid or newer identity state causes startup
to fail without overwriting it. Do not delete identity state to solve a startup
error: inspect permissions or restore the original state. Preserve the complete
data directory across future container recreation and backups. It is local
Gateway state, not a second authoritative product registry.

Lifecycle logs are JSON on stderr. Requests, headers, arguments, configuration
dumps, raw transport errors, and file contents are not logged. No credentials
are stored at point 1. Future personal keys and delegations remain memory-only;
service trust persistence belongs to point 2.

## Checks and CI

```sh
make check
# Or, without Make:
sh scripts/check.sh
```

Runs formatting checks, `go mod tidy -diff`, module verification, vet, race tests,
a binary build, and real HTTP/process smoke checks. Tests use disposable local
state and listeners; they do not contact or modify installed Suite services.

GitHub CI runs the same checks on Linux (minimum and stable Go) and macOS
(stable Go), plus Linux amd64/arm64 cross-compilation. Workflows run on pull
requests, pushes to main/master, and manual dispatch. CI is prepared locally;
remote results only exist after this repository is pushed to GitHub.

Docker/Compose and release automation are explicitly point 8 deliverables.
No release version, Git commit, tag, remote, or publication is created by this
foundation work. Local builds identify as `dev`.

## Contract baseline

- [Suite implementation plan](https://github.com/mapherez/nox-mcp/blob/master/Documents/NOX_SUITE_IMPLEMENTATION_PLAN.md)
- [Product integration proposal](https://github.com/mapherez/nox-mcp/blob/master/Documents/PRODUCT_INTEGRATION_PROPOSAL.md)
- [Gateway contract](https://github.com/mapherez/nox-mcp/blob/master/Documents/GATEWAY_CONTRACT.md)
- [Released SDK integration](https://github.com/mapherez/nox-mcp/blob/master/Documents/INTEGRATION.md)

The SDK supplies shared contracts/mechanisms; this repository implements the
Gateway host. CLI, Portal, and other product adaptations remain separate work.
