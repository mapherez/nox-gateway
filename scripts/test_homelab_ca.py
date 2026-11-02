import json
from pathlib import Path
import socket
import ssl
import threading
import subprocess
import sys
import tempfile
import unittest

SCRIPT = Path(__file__).with_name("homelab-ca.py")


class HomelabCATest(unittest.TestCase):
    def run_ca(self, *args, success=True):
        result = subprocess.run([sys.executable, str(SCRIPT), "--directory", str(self.base), "--password-file", str(self.password), *args], capture_output=True, text=True, timeout=30)
        self.assertNotIn("TEST_CA_PASSPHRASE", result.stdout + result.stderr)
        self.assertEqual(result.returncode == 0, success, result.stdout + result.stderr)
        return json.loads(result.stdout) if success else result

    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix="nox-ca-test-")
        self.addCleanup(self.temp.cleanup)
        self.base = Path(self.temp.name) / "ca"
        self.password = Path(self.temp.name) / "password"
        self.password.write_text("TEST_CA_PASSPHRASE_FOR_DISPOSABLE_TEST_ONLY")
        self.password.chmod(0o600)

    def test_issue_renew_and_expiry_checks_preserve_ca_and_leaf_keys(self):
        root = self.run_ca("init")
        self.assertTrue(root["rootFingerprint"].startswith("sha256 Fingerprint=") or "Fingerprint=" in root["rootFingerprint"])
        root_key = (self.base / "root.key").read_bytes()
        self.assertIn(b"ENCRYPTED PRIVATE KEY", root_key)
        args = ("issue", "--name", "home-arpa", "--dns-name", "*.home.arpa")
        issued = self.run_ca(*args)
        leaf_key = Path(issued["privateKey"]).read_bytes()
        first = Path(issued["certificate"]).read_bytes()
        self.run_ca(*args)
        self.assertEqual(root_key, (self.base / "root.key").read_bytes())
        self.assertEqual(leaf_key, Path(issued["privateKey"]).read_bytes())
        self.assertNotEqual(first, Path(issued["certificate"]).read_bytes())
        self.run_ca("init", success=False)
        self.assertFalse(self.run_ca("check", "--name", "home-arpa")["renewalNeeded"])
        self.run_ca("check", "--name", "home-arpa", "--warn-days", "100", success=False)
        server_context = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
        server_context.load_cert_chain(issued["certificate"], issued["privateKey"])
        client_context = ssl.create_default_context(cafile=str(self.base / "root.crt"))
        for host in ("gateway.home.arpa", "yard.home.arpa", "backend.home.arpa"):
            server, client = socket.socketpair()
            server.settimeout(5)
            client.settimeout(5)
            failures = []
            def serve():
                try:
                    with server_context.wrap_socket(server, server_side=True) as stream:
                        stream.sendall(b"ok")
                except Exception as error:
                    failures.append(type(error).__name__)
            thread = threading.Thread(target=serve, daemon=True)
            thread.start()
            with client_context.wrap_socket(client, server_hostname=host) as stream:
                self.assertEqual(stream.recv(2), b"ok")
            thread.join(5)
            self.assertFalse(thread.is_alive())
            self.assertEqual(failures, [])

    def test_missing_key_unsafe_files_and_invalid_names_are_rejected(self):
        self.run_ca("init")
        args = ("issue", "--name", "gateway", "--dns-name", "gateway.home.arpa")
        issued = self.run_ca(*args)
        Path(issued["privateKey"]).unlink()
        self.run_ca(*args, success=False)
        self.run_ca("issue", "--name", "bad", "--dns-name", "gateway.example.com", success=False)
        self.password.chmod(0o644)
        self.run_ca("issue", "--name", "private", "--dns-name", "gateway.home.arpa", success=False)


if __name__ == "__main__":
    unittest.main()
