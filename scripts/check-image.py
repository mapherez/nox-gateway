"""Check only disposable Gateway image containers and volumes."""

import argparse, json, secrets, subprocess, time, urllib.error, urllib.request


def docker(*args):
    return subprocess.check_output(["docker", *args], text=True).strip()


def request(base, path):
    try:
        r = urllib.request.build_opener(urllib.request.ProxyHandler({})).open(
            base + path, timeout=3
        )
    except urllib.error.HTTPError as e:
        r = e
    with r:
        return r.status, json.load(r)


def run(args):
    name = "nox-gateway-check-" + secrets.token_hex(6)
    local = name + "-local"
    data = name + "-data"
    containers = []
    try:
        docker("volume", "create", local)
        docker("volume", "create", data)

        def start():
            c = docker(
                "run",
                "-d",
                "--rm",
                "-p",
                "127.0.0.1::8080",
                "--mount",
                "type=volume,source=" + local + ",target=/run/nox-suite",
                "--mount",
                "type=volume,source=" + data + ",target=/data",
                args.image,
            )
            containers.append(c)
            base = "http://" + docker("port", c, "8080/tcp")
            end = time.monotonic() + 30
            while time.monotonic() < end:
                try:
                    status, health = request(base, "/healthz")
                    if status == 200:
                        return c, base, health
                except (OSError, ValueError):
                    pass
                time.sleep(0.1)
            raise AssertionError("Packaged Gateway did not start")

        c, base, health = start()
        assert (
            health["productVersion"] == args.version
            and health["contractVersion"] == "1.0"
        )
        assert request(base, "/readyz")[0] == 503
        assert request(base, "/mcp")[0] == 401
        assert request(base, "/nox/integration/pairing")[0] == 404
        docker("exec", c, "/usr/local/bin/nox-gateway", "--healthcheck")
        identity = docker("exec", c, "cat", "/data/identity.json")
        docker(
            "exec",
            c,
            "/bin/sh",
            "-c",
            'test -S /run/nox-suite/nox-gateway.sock && test "$(id -u)" != 0',
        )
        docker("stop", c)
        containers.remove(c)
        c, base, health = start()
        assert docker("exec", c, "cat", "/data/identity.json") == identity
        print(
            "PASS: image version, non-root startup, local socket, private state/recreation, healthcheck and rejected anonymous MCP/TCP pairing."
        )
    finally:
        for c in containers:
            subprocess.run(
                ["docker", "rm", "-f", c],
                stdout=subprocess.DEVNULL,
                stderr=subprocess.DEVNULL,
                check=False,
            )
        for v in [data, local]:
            subprocess.run(
                ["docker", "volume", "rm", v],
                stdout=subprocess.DEVNULL,
                stderr=subprocess.DEVNULL,
                check=False,
            )


if __name__ == "__main__":
    p = argparse.ArgumentParser()
    p.add_argument("--image", required=True)
    p.add_argument("--version", required=True)
    run(p.parse_args())
