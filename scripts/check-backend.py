"""Actual Backend/Gateway acceptance with disposable processes, data and Unix sockets."""

import argparse, base64, datetime, hashlib, hmac, http.client, json, os, secrets, signal, socket, sqlite3, subprocess, tempfile, time, urllib.error, urllib.request
from pathlib import Path


class GatewayUnavailable(Exception):
    """Discovery can be cancelled while an authoritative event is applied."""


class UnixHTTP(http.client.HTTPConnection):
    def __init__(self, directory, host):
        super().__init__(host, timeout=35)
        self.path = str(Path(directory) / (host + ".sock"))

    def connect(self):
        self.sock = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        self.sock.settimeout(self.timeout)
        self.sock.connect(self.path)


def call(base, path, method="GET", body=None, key="", headers=None, local=None):
    headers = {"Accept": "application/json", **(headers or {})}
    if key:
        headers["Authorization"] = "Bearer " + key
    data = None if body is None else json.dumps(body, separators=(",", ":")).encode()
    if data is not None:
        headers["Content-Type"] = "application/json"
    if local:
        c = UnixHTTP(local, "nox-backend")
        c.request(method, path, data, headers)
        r = c.getresponse()
        raw = r.read()
        status = r.status
        c.close()
    else:
        q = urllib.request.Request(
            base + path, data=data, headers=headers, method=method
        )
        try:
            r = urllib.request.build_opener(urllib.request.ProxyHandler({})).open(
                q, timeout=40
            )
        except urllib.error.HTTPError as e:
            r = e
        with r:
            status = r.status
            raw = r.read()
    return status, (
        (
            json.loads(raw)
            if raw.lstrip().startswith((b"{", b"["))
            else {"message": raw.decode(errors="replace")[:160]}
        )
        if raw
        else None
    )


def wait(base, path, predicate):
    end = time.monotonic() + 25
    while time.monotonic() < end:
        try:
            status, value = call(base, path)
            if predicate(status, value):
                return value
        except (OSError, ValueError):
            pass
        time.sleep(0.1)
    raise AssertionError("Timed out waiting for " + path)


def port():
    with socket.socket() as s:
        s.bind(("127.0.0.1", 0))
        return str(s.getsockname()[1])


def require(status, want, value):
    assert status == want, ("Unexpected HTTP status", status, want, value)
    return value


def run(args):
    with tempfile.TemporaryDirectory(prefix="ng-") as temp:
        root = Path(temp)
        channel = root / "local"
        channel.mkdir(mode=0o700)
        bdata = root / "backend"
        gdata = root / "gateway"
        burl = "http://127.0.0.1:" + port()
        gurl = "http://127.0.0.1:" + port()
        env = {k: v for k, v in os.environ.items() if not k.startswith("NOX_")}
        benv = {
            **env,
            "NOX_BACKEND_ADDR": burl[7:],
            "NOX_BACKEND_DATA_DIR": str(bdata),
            "NOX_BACKEND_PUBLIC_URL": burl,
            "NOX_BACKEND_ADMIN_EMAILS": "admin@example.invalid",
            "NOX_BACKEND_SUITE_SOCKET_DIR": str(channel),
            "NOX_BACKEND_SUITE_MCP_ENABLED": "true",
        }
        genv = {
            **env,
            "NOX_GATEWAY_LISTEN_ADDR": gurl[7:],
            "NOX_GATEWAY_DATA_DIR": str(gdata),
            "NOX_GATEWAY_SUITE_SOCKET_DIR": str(channel),
            "NOX_GATEWAY_PUBLIC_URL": gurl,
        }
        processes = []
        logs = []

        def start(binary, settings, name):
            log = (root / (name + ".log")).open("ab")
            logs.append(log)
            p = subprocess.Popen([binary], env=settings, stdout=log, stderr=log)
            processes.append(p)
            return p

        def stop(p):
            if p.poll() is None:
                p.send_signal(signal.SIGTERM)
                p.wait(timeout=15)
            if p in processes:
                processes.remove(p)

        try:
            b = start(args.backend, benv, "backend")
            wait(burl, "/v1/health", lambda s, v: s == 200)
            stop(b)
            # Seed only while the disposable Backend is stopped.
            db = sqlite3.connect(bdata / "nox-backend.db")
            admin = db.execute(
                "SELECT key_value FROM api_keys JOIN users ON users.id=api_keys.user_id WHERE email='admin@example.invalid'"
            ).fetchone()[0]
            now = datetime.datetime.now(datetime.timezone.utc).strftime(
                "%Y-%m-%dT%H:%M:%SZ"
            )
            member = "noxsync_" + secrets.token_urlsafe(32)
            db.execute(
                "INSERT INTO users(id,email,first_name,display_name,role,status,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?)",
                (
                    "test_member",
                    "member@example.invalid",
                    "",
                    "Member",
                    "USER",
                    "ACTIVE",
                    now,
                    now,
                ),
            )
            db.execute(
                "INSERT INTO api_keys(user_id,key_prefix,key_value,key_hash,created_at) VALUES(?,?,?,?,?)",
                (
                    "test_member",
                    "noxsync_",
                    member,
                    hashlib.sha256(member.encode()).hexdigest(),
                    now,
                ),
            )
            db.commit()
            db.close()
            b = start(args.backend, benv, "backend")
            wait(burl, "/v1/health", lambda s, v: s == 200)
            g = start(args.gateway, genv, "gateway")
            wait(gurl, "/healthz", lambda s, v: s == 200)
            s, v = call(gurl, "/nox/integration/pairing", method="POST", body={})
            require(s, 404, v)
            s, connection = call(
                burl,
                "/api/admin/connections/gateway",
                "PUT",
                {"origin": gurl},
                admin,
                {"If-None-Match": "*"},
            )
            require(s, 200, connection)
            s, attempt = call(
                burl,
                "/api/admin/connections/gateway/verify",
                "POST",
                {"revision": connection["revision"]},
                admin,
            )
            require(s, 200, attempt)
            s, setup = call(
                burl,
                "/api/admin/connections/gateway/connect",
                "POST",
                {"attemptId": attempt["attemptId"]},
                admin,
            )
            require(s, 200, setup)
            assert setup["state"] == "completed", setup
            wait(gurl, "/readyz", lambda s, v: s == 200)

            def rpc(method, params, key=admin):
                params = {
                    **params,
                    "_meta": {
                        "io.modelcontextprotocol/protocolVersion": "2026-07-28",
                        "io.modelcontextprotocol/clientCapabilities": {},
                        **params.get("_meta", {}),
                    },
                }
                s, v = call(
                    gurl,
                    "/mcp",
                    "POST",
                    {"jsonrpc": "2.0", "id": 1, "method": method, "params": params},
                    key,
                    {
                        "MCP-Protocol-Version": "2026-07-28",
                        "Accept": "application/json, text/event-stream",
                        "Mcp-Method": method,
                        **({"Mcp-Name": params["name"]} if "name" in params else {}),
                    },
                )
                if s == 503:
                    raise GatewayUnavailable()
                require(s, 200, v)
                return v

            # No source products: an empty catalog still authenticates its caller.
            s, v = call(
                gurl,
                "/mcp",
                "POST",
                {"jsonrpc": "2.0", "id": 1, "method": "tools/list", "params": {}},
            )
            require(s, 401, v)
            assert rpc("tools/list", {})["result"]["tools"] == []

            # Enroll the actual Backend's own existing Suite MCP adapter locally.
            def management(path, method="GET", body=None, key=admin, revision=None):
                headers = {"Idempotency-Key": secrets.token_hex(16)}
                if revision is not None:
                    headers["If-Match"] = json.dumps(revision)
                if (
                    method == "PUT" or path == "/api/nox/pairings"
                ) and revision is None:
                    headers["If-None-Match"] = "*"
                s, v = call("", path, method, body, key, headers, channel)
                assert s in (200, 201, 204), (path, s, v)
                return v

            intent = management(
                "/api/nox/pairings",
                "POST",
                {
                    "contractVersion": "1.0",
                    "productId": "nox-backend",
                    "purpose": "product",
                    "baseUrl": "http://nox-backend",
                },
            )
            declaration = call("", "/nox/integration", local=channel)[1]
            canonical = lambda v: json.dumps(
                v, sort_keys=True, separators=(",", ":"), ensure_ascii=False
            ).encode()
            b64 = lambda b: base64.urlsafe_b64encode(b).decode().rstrip("=")
            binding = {
                "contractVersion": "1.0",
                "pairingId": intent["pairingId"],
                "nonce": intent["nonce"],
                "productId": "nox-backend",
                "declarationFingerprint": b64(
                    hashlib.sha256(canonical(declaration)).digest()
                ),
                "baseUrl": "http://nox-backend",
            }
            proof = b64(
                hmac.new(
                    base64.urlsafe_b64decode(intent["secret"] + "=="),
                    canonical(binding),
                    hashlib.sha256,
                ).digest()
            )
            provision = management(
                "/api/nox/pairings/" + intent["pairingId"] + "/complete",
                "POST",
                {"contractVersion": "1.0", "binding": binding, "proof": proof},
                key="",
            )
            management(
                "/api/nox/pairings/" + intent["pairingId"] + "/ack",
                "POST",
                key=provision["serviceCredential"],
            )
            record = management(
                "/api/nox/products/nox-backend",
                "PUT",
                {"contractVersion": "1.0", "baseUrl": "http://nox-backend"},
            )
            end = time.monotonic() + 25
            tools = []
            while time.monotonic() < end:
                try:
                    result = rpc("tools/list", {})
                except GatewayUnavailable:
                    time.sleep(0.1)
                    continue
                tools = result.get("result", {}).get("tools", [])
                if any(t["name"] == "nox-backend__backend_health" for t in tools):
                    break
                time.sleep(0.1)
            assert any(
                t["name"] == "nox-backend__backend_health" for t in tools
            ), result
            catalog = rpc(
                "nox/tools/catalog",
                {
                    "_meta": {
                        "io.modelcontextprotocol/clientCapabilities": {
                            "extensions": {"nox/discovery": {"version": "1.0"}}
                        }
                    }
                },
            )
            assert catalog["result"]["cacheScope"] == "private", catalog
            assert result["result"]["cacheScope"] == "private", result
            response = rpc(
                "tools/call", {"name": "nox-backend__backend_health", "arguments": {}}
            )
            assert not response["result"].get("isError"), response
            created = rpc(
                "tools/call",
                {
                    "name": "nox-backend__backend_vault_create",
                    "arguments": {"name": "Gateway acceptance"},
                },
            )
            assert not created["result"].get("isError"), created
            member_tools = rpc("tools/list", {}, member)["result"]["tools"]
            assert all(
                t["name"] != "nox-backend__backend_vault_create" for t in member_tools
            )
            isolated = rpc(
                "tools/call",
                {"name": "nox-backend__backend_vault_list", "arguments": {}},
                member,
            )
            assert "Gateway acceptance" not in json.dumps(isolated)
            # Readiness observations must use the Gateway service probe, not a human key.
            end = time.monotonic() + 30
            observations = []
            while time.monotonic() < end:
                snapshot = management("/api/nox/products")
                observations = snapshot["observations"]
                if any(
                    o["observation"]["source"] == "gateway"
                    and o["observation"]["state"] == "ready"
                    for o in observations
                ):
                    break
                time.sleep(0.2)
            assert any(
                o["observation"]["source"] == "gateway"
                and o["observation"]["state"] == "ready"
                for o in observations
            ), observations
            # Restart keeps identity but no personal-key/delegation cache on disk.
            identity = (gdata / "identity.json").read_bytes()
            stop(g)
            g = start(args.gateway, genv, "gateway")
            wait(gurl, "/readyz", lambda s, v: s == 200)
            assert (gdata / "identity.json").read_bytes() == identity
            assert not rpc(
                "tools/call", {"name": "nox-backend__backend_health", "arguments": {}}
            )["result"].get("isError")
            # Confirm outage reuses existing valid authority without extending it.
            stop(b)
            assert rpc("tools/list", {})["result"][
                "tools"
            ], "valid private view disappeared during a bounded outage"
            b = start(args.backend, benv, "backend")
            wait(burl, "/v1/health", lambda s, v: s == 200)
            # Role/status changes reach the Gateway through authority events.
            s, v = call(
                burl,
                "/api/admin/users/test_member/status",
                "PUT",
                {"status": "DISABLED"},
                admin,
            )
            require(s, 200, v)
            end = time.monotonic() + 25
            while time.monotonic() < end:
                s, v = call(
                    gurl,
                    "/mcp",
                    "POST",
                    {
                        "jsonrpc": "2.0",
                        "id": 1,
                        "method": "tools/list",
                        "params": {
                            "_meta": {
                                "io.modelcontextprotocol/protocolVersion": "2026-07-28",
                                "io.modelcontextprotocol/clientCapabilities": {},
                            }
                        },
                    },
                    member,
                    {
                        "MCP-Protocol-Version": "2026-07-28",
                        "Accept": "application/json, text/event-stream",
                        "Mcp-Method": "tools/list",
                    },
                )
                if s in (401, 403):
                    break
                time.sleep(0.1)
            assert s in (401, 403), (s, v)
            # Removing membership removes the product without uninstalling it.
            management(
                "/api/nox/products/nox-backend", "DELETE", revision=record["revision"]
            )
            end = time.monotonic() + 25
            while time.monotonic() < end:
                try:
                    result = rpc("tools/list", {})
                except GatewayUnavailable:
                    time.sleep(0.1)
                    continue
                if result["result"]["tools"] == []:
                    break
                time.sleep(0.1)
            assert result["result"]["tools"] == [], result
            stop(g)
            stop(b)
            for path in gdata.iterdir():
                if path.is_file():
                    raw = path.read_bytes()
                    assert admin.encode() not in raw and member.encode() not in raw, (
                        path.name,
                        "personal key persisted",
                    )
            for path in root.glob("*.log"):
                raw = path.read_bytes()
                assert admin.encode() not in raw and member.encode() not in raw, (
                    "personal key logged",
                    path.name,
                )
            print(
                "PASS: actual Backend/Gateway automatic local setup, authenticated empty catalog, authorized discovery/calls, role and vault isolation, restart, invalidation, removal and secret-free state/logs."
            )
        finally:
            for p in reversed(processes):
                if p.poll() is None:
                    p.send_signal(signal.SIGTERM)
                    try:
                        p.wait(timeout=15)
                    except subprocess.TimeoutExpired:
                        p.kill()
                        p.wait(timeout=5)
            for log in logs:
                log.close()


if __name__ == "__main__":
    parser = argparse.ArgumentParser()
    parser.add_argument("--backend", required=True)
    parser.add_argument("--gateway", required=True)
    run(parser.parse_args())
