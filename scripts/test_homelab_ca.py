import fcntl
import json
import os
from pathlib import Path
import shutil
import socket
import ssl
import stat
import subprocess
import sys
import tempfile
import threading
import unittest

SCRIPT = Path(__file__).with_name("homelab-ca.py")


class HomelabCATest(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix="nox-ca-test-")
        self.addCleanup(self.temp.cleanup)
        self.parent = Path(self.temp.name)
        self.base = self.parent / "folder with spaces"
        self.base.mkdir(mode=0o755)
        self.script = self.base / SCRIPT.name
        shutil.copy2(SCRIPT, self.script)
        self.cwd = self.parent / "different working directory"
        self.cwd.mkdir()
        self.unused_random = self.parent / "must-not-create-random-file"

    def run_ca(self, *args, success=True, text=None, machine=True):
        arguments = [*args]
        if machine:
            arguments.append("--json")
        result = subprocess.run(
            [sys.executable, str(self.script), *arguments], input=text,
            cwd=self.cwd, env={**os.environ, "RANDFILE": str(self.unused_random)},
            capture_output=True, text=True, timeout=30,
        )
        self.assertEqual(result.returncode == 0, success, result.stdout + result.stderr)
        phrase_file = self.base / "ca/passphrase.txt"
        if phrase_file.exists():
            phrase = phrase_file.read_text().strip()
            self.assertNotIn(phrase, result.stdout + result.stderr)
        self.assertEqual(list(self.cwd.iterdir()), [], "script wrote into the calling working directory")
        self.assertFalse(self.unused_random.exists(), "OpenSSL wrote outside the script folder")
        self.assertFalse(list(self.base.rglob("tls-work-*")), "temporary work was not removed")
        self.assertFalse(list(self.base.rglob("tls-write-*")), "temporary output was not removed")
        if success and machine:
            return json.loads(result.stdout)
        return result

    def issue(self, *args, **kwargs):
        return self.run_ca("--dns-name", "*.home.arpa", *args, **kwargs)

    def snapshot(self):
        return {p.relative_to(self.base).as_posix(): p.read_bytes() for p in self.base.rglob("*") if p.is_file()}

    def test_single_run_generates_everything_next_to_script_without_chmod_of_base(self):
        before = stat.S_IMODE(self.base.stat().st_mode)
        result = self.issue()
        self.assertEqual(result["dnsNames"], ["*.home.arpa"])
        self.assertEqual(stat.S_IMODE(self.base.stat().st_mode), before)
        self.assertEqual(set(p.name for p in self.base.iterdir()), {SCRIPT.name, "ca", "certificates", "COMO-USAR-CERTIFICADOS.md"})
        phrase = (self.base / "ca/passphrase.txt").read_text().strip()
        self.assertRegex(phrase, r"^[A-Za-z0-9_-]{64}$")
        self.assertIn(b"ENCRYPTED PRIVATE KEY", (self.base / "ca/root.key").read_bytes())
        for name in ("ca/root.key", "ca/passphrase.txt", "certificates/server/key.pem"):
            self.assertEqual(stat.S_IMODE((self.base / name).stat().st_mode), 0o600)
        for name in ("ca", "certificates", "certificates/server"):
            self.assertEqual(stat.S_IMODE((self.base / name).stat().st_mode), 0o700)
        for value in (result["rootCertificate"], result["certificate"], result["privateKey"], result["fullchain"], result["instructions"]):
            self.assertFalse(Path(value).is_absolute())
            self.assertTrue((self.base / value).is_file())
        guide = (self.base / result["instructions"]).read_text()
        self.assertIn("Nginx Proxy Manager", guide)
        self.assertIn("Keychain Access", guide)
        self.assertIn("Limpeza", guide)
        self.assertNotIn(str(self.base), guide)
        self.assertFalse(list(self.base.rglob("__pycache__")))

    def test_interactive_mode_only_requests_dns_names_and_reuses_saved_defaults(self):
        result = self.run_ca(machine=False, text="*.home.arpa, service.demo.test\n")
        self.assertIn("Nomes DNS", result.stdout)
        self.assertNotIn("passphrase:", result.stdout.lower())
        check = self.run_ca("--check")
        self.assertEqual(check["dnsNames"], ["*.home.arpa", "service.demo.test"])
        root = (self.base / "ca/root.crt").read_bytes()
        self.run_ca(machine=False, text="\n")
        self.assertEqual((self.base / "ca/root.crt").read_bytes(), root)
        self.assertEqual(self.run_ca("--check")["dnsNames"], check["dnsNames"])

    def test_renewal_reuses_root_passphrase_and_leaf_key(self):
        self.issue()
        before = self.snapshot()
        self.issue()
        for name in ("ca/root.key", "ca/root.crt", "ca/passphrase.txt", "certificates/server/key.pem"):
            self.assertEqual(before[name], (self.base / name).read_bytes())
        self.assertNotEqual(before["certificates/server/cert.pem"], (self.base / "certificates/server/cert.pem").read_bytes())
        self.assertNotIn(before["ca/passphrase.txt"].strip(), (self.base / "COMO-USAR-CERTIFICADOS.md").read_bytes())

    def test_copying_entire_folder_preserves_ca_and_uses_only_new_location(self):
        self.issue()
        old_base = self.base
        original = self.snapshot()
        new_base = self.parent / "another computer" / "copied folder"
        new_base.parent.mkdir()
        shutil.copytree(old_base, new_base)
        self.base = new_base
        self.script = new_base / SCRIPT.name
        result = self.issue()
        self.assertNotIn(str(old_base), json.dumps(result))
        for name in ("ca/root.key", "ca/root.crt", "ca/passphrase.txt", "certificates/server/key.pem"):
            self.assertEqual(original[name], (new_base / name).read_bytes())
            self.assertEqual(original[name], (old_base / name).read_bytes())
        self.assertEqual(original["certificates/server/cert.pem"], (old_base / "certificates/server/cert.pem").read_bytes())
        self.assertNotIn(str(old_base), (new_base / "COMO-USAR-CERTIFICADOS.md").read_text())

    def test_named_sets_can_use_different_domains_under_the_same_ca(self):
        self.issue()
        original = self.snapshot()
        other = self.run_ca("--name", "other", "--dns-name", "service.internal.example", "--dns-name", "other.home.arpa")
        self.assertEqual(other["dnsNames"], ["other.home.arpa", "service.internal.example"])
        self.assertEqual(other["privateKey"], "certificates/other/key.pem")
        self.assertEqual(original["ca/root.crt"], (self.base / "ca/root.crt").read_bytes())
        self.assertEqual(original["certificates/server/cert.pem"], (self.base / "certificates/server/cert.pem").read_bytes())

    def test_check_does_not_modify_persisted_files(self):
        self.issue()
        before = self.snapshot()
        result = self.run_ca("--check")
        self.assertFalse(result["renewalNeeded"])
        self.assertEqual(before, self.snapshot())

    def test_missing_ca_material_and_missing_leaf_key_are_not_regenerated(self):
        for missing in ("ca/root.key", "ca/root.crt", "ca/passphrase.txt", "certificates/server/key.pem"):
            with self.subTest(missing=missing):
                self.issue()
                path = self.base / missing
                original = path.read_bytes()
                path.unlink()
                before = self.snapshot()
                self.issue(success=False)
                self.assertFalse(path.exists())
                self.assertEqual(before, self.snapshot())
                path.write_bytes(original)
                path.chmod(0o644 if missing.endswith(".crt") else 0o600)

    def test_tampered_passphrase_and_public_secret_permissions_fail_without_overwrite(self):
        self.issue()
        phrase = self.base / "ca/passphrase.txt"
        original = phrase.read_bytes()
        phrase.write_text("X" * 64 + "\n")
        before = self.snapshot()
        self.issue(success=False)
        self.assertEqual(before, self.snapshot())
        phrase.write_bytes(original)
        phrase.chmod(0o644)
        self.issue(success=False)
        self.assertEqual(phrase.read_bytes(), original)

    def test_symlinks_and_unrelated_instruction_files_are_not_overwritten(self):
        unrelated = self.base / "COMO-USAR-CERTIFICADOS.md"
        unrelated.write_text("my own notes\n")
        self.issue(success=False)
        self.assertEqual(unrelated.read_text(), "my own notes\n")
        unrelated.unlink()
        self.issue()
        key = self.base / "ca/root.key"
        outside = self.parent / "external-private-key"
        key.rename(outside)
        key.symlink_to(outside)
        original = outside.read_bytes()
        self.issue(success=False)
        self.assertEqual(original, outside.read_bytes())
        self.assertTrue(key.is_symlink())

    def test_bad_dns_and_traversal_fail_before_generating_files(self):
        before = self.snapshot()
        for name in ("https://gateway.home.arpa", "gateway.home.arpa:443", "bad;command", "../escape", "foo.*.home.arpa", "192.168.10.10"):
            with self.subTest(name=name):
                self.run_ca("--dns-name", name, success=False)
                self.assertEqual(before, self.snapshot())
        self.run_ca("--name", "../escape", "--dns-name", "*.home.arpa", success=False)
        self.assertEqual(before, self.snapshot())

    def test_two_processes_cannot_mutate_same_folder_at_once(self):
        descriptor = os.open(self.base, os.O_RDONLY)
        try:
            fcntl.flock(descriptor, fcntl.LOCK_EX | fcntl.LOCK_NB)
            self.issue(success=False)
            self.assertEqual(list(self.base.iterdir()), [self.script])
        finally:
            os.close(descriptor)

    def test_generated_certificate_is_trusted_for_requested_names(self):
        result = self.issue()
        server_context = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
        server_context.load_cert_chain(self.base / result["certificate"], self.base / result["privateKey"])
        client_context = ssl.create_default_context(cafile=str(self.base / result["rootCertificate"]))
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


if __name__ == "__main__":
    unittest.main()
