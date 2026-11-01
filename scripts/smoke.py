#!/usr/bin/env python3
"""Exercise the actual local diagnostic binary with disposable private state."""
import argparse
import json
import os
from pathlib import Path
import signal
import subprocess
import tempfile
import time
import urllib.error
import urllib.request


def require(condition, message):
    if not condition:
        raise RuntimeError(message)


class Gateway:
    def __init__(self, binary, env, log_path):
        self.log_path = log_path
        self.log = log_path.open("w")
        self.process = subprocess.Popen(
            [str(binary)], env=env, stdin=subprocess.DEVNULL,
            stdout=subprocess.DEVNULL, stderr=self.log,
        )
        self.address = None

    def wait_listening(self):
        deadline = time.monotonic() + 10
        while time.monotonic() < deadline:
            require(self.process.poll() is None, "Gateway exited before listening")
            for line in self.log_path.read_text().splitlines():
                try:
                    entry = json.loads(line)
                except json.JSONDecodeError:
                    continue  # A buffered log line may not yet be complete.
                if entry.get("msg") == "gateway_listening":
                    self.address = entry["address"]
                    return
            time.sleep(0.025)
        raise RuntimeError("Gateway did not listen within the startup deadline")

    def close(self):
        if self.process.poll() is None:
            self.process.kill()
        self.process.wait(timeout=5)
        self.log.close()


def request(address, path):
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
    req = urllib.request.Request(
        "http://" + address + path,
        headers={"Authorization": "Bearer SMOKE_PERSONAL_SECRET"},
    )
    try:
        response = opener.open(req, timeout=3)
    except urllib.error.HTTPError as err:
        response = err
    with response:
        return response.status, json.load(response)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", required=True, type=Path)
    args = parser.parse_args()
    binary = args.binary.resolve(strict=True)
    # Never inherit a user's Gateway deployment settings into disposable tests.
    env = {k: v for k, v in os.environ.items() if not k.startswith("NOX_GATEWAY_")}
    env.update({"NOX_GATEWAY_LISTEN_ADDR": "127.0.0.1:0", "NOX_GATEWAY_LOG_LEVEL": "debug"})
    version = subprocess.run([str(binary), "--version"], env=env, capture_output=True, text=True, timeout=5)
    require(version.returncode == 0 and "Suite 1.0" in version.stdout, "Version command failed")
    with tempfile.TemporaryDirectory(prefix="nox-gateway-smoke-") as temporary:
        temp = Path(temporary)
        state = temp / "state"
        env["NOX_GATEWAY_DATA_DIR"] = str(state)
        identity = None
        # Normal stop, forced process death, and another clean stop all preserve
        # discovery identity and release the OS-owned directory lock.
        for index, stop_signal in enumerate((signal.SIGTERM, signal.SIGKILL, signal.SIGINT)):
            gateway = Gateway(binary, env, temp / f"process-{index}.log")
            try:
                gateway.wait_listening()
                current = json.loads((state / "identity.json").read_text())
                if identity is None:
                    identity = current
                require(current == identity, "Restart changed persistent discovery identity")
                status, body = request(gateway.address, "/healthz")
                require(status == 200 and body["productId"] == "nox-gateway" and body["status"] == "ok", "Liveness failed")
                status, body = request(gateway.address, "/readyz")
                require(status == 503 and body["code"] == "GATEWAY_NOT_CONFIGURED", "Unconfigured Gateway claimed readiness")
                status, body = request(gateway.address, "/mcp?api_key=SMOKE_QUERY_SECRET")
                require(status == 404 and body["code"] == "NOT_FOUND", "Unimplemented MCP endpoint is exposed")
                if index == 0:
                    second = subprocess.run([str(binary)], env=env, capture_output=True, text=True, timeout=5)
                    require(second.returncode != 0 and "already in use" in second.stderr, "Second process acquired live state")
                gateway.process.send_signal(stop_signal)
                result = gateway.process.wait(timeout=5)
                require(result == (-signal.SIGKILL if stop_signal == signal.SIGKILL else 0), "Incorrect shutdown exit status")
            finally:
                gateway.close()
            log = gateway.log_path.read_text()
            require("SMOKE_PERSONAL_SECRET" not in log and "SMOKE_QUERY_SECRET" not in log, "Request secrets leaked into logs")
            if stop_signal != signal.SIGKILL:
                entries = [json.loads(line) for line in log.splitlines()]
                require(entries[-1]["msg"] == "gateway_stopped", "Clean stop was not logged")
    print("Binary smoke passed: diagnostics, exclusive state, signals, crash recovery, persistent identity, safe logs.")


if __name__ == "__main__":
    main()
