#!/usr/bin/env python3
"""Create/renew a private home.arpa CA and NPM certificates using local OpenSSL.

Run outside the repository data tree. The CA private key is encrypted, never
uploaded to NPM, and never printed. Renewal reuses the leaf key to preserve pins.
"""
import argparse
import getpass
import json
import os
from pathlib import Path
import re
import secrets
import shutil
import stat
import subprocess
import sys
import tempfile


class CAError(Exception):
    pass


def private_dir(path):
    path.mkdir(parents=True, exist_ok=True, mode=0o700)
    mode = path.lstat().st_mode
    if not stat.S_ISDIR(mode) or stat.S_IMODE(mode) & 0o077:
        raise CAError("CA directories must be private (0700) and cannot be symlinks")


def private_file(path):
    info = path.lstat()
    if not stat.S_ISREG(info.st_mode) or stat.S_IMODE(info.st_mode) & 0o077:
        raise CAError("Secret input must be a regular private file (0600/0400)")
    if info.st_size > 65536:
        raise CAError("Secret input exceeds its size limit")


def password(args, new=False):
    if args.password_file:
        private_file(args.password_file)
        value = args.password_file.read_text().rstrip("\r\n")
    else:
        value = getpass.getpass("CA key passphrase: ")
        if new and value != getpass.getpass("Repeat CA key passphrase: "):
            raise CAError("Passphrases did not match")
    if len(value) < 12 or len(value.encode()) > 1024 or "\n" in value or "\r" in value or "\x00" in value:
        raise CAError("Use a passphrase of 12 or more characters without control characters")
    return value


def openssl(arguments, passphrase=None, ok_codes=(0,)):
    read_fd = None
    try:
        if passphrase is not None:
            read_fd, write_fd = os.pipe()
            try:
                os.write(write_fd, passphrase.encode() + b"\n")
            finally:
                os.close(write_fd)
            arguments = [str(item).replace("{PASSFD}", "fd:" + str(read_fd)) for item in arguments]
        process = subprocess.run(
            ["openssl", *map(str, arguments)],
            stdin=subprocess.DEVNULL, capture_output=True,
            pass_fds=(() if read_fd is None else (read_fd,)),
            env={**os.environ, "LC_ALL": "C"}, timeout=30,
        )
        if process.returncode not in ok_codes:
            raise CAError("OpenSSL failed; check the passphrase and certificate/key files")
        return process.stdout.decode("utf-8"), process.returncode
    finally:
        if read_fd is not None:
            os.close(read_fd)


def atomic_file(path, data, mode=0o600):
    if path.is_symlink() or (path.exists() and not path.is_file()):
        raise CAError("Refusing to replace a non-regular output file")
    fd, name = tempfile.mkstemp(prefix=".install-", dir=path.parent)
    try:
        with os.fdopen(fd, "wb") as stream:
            stream.write(data)
            stream.flush()
            os.fsync(stream.fileno())
        os.chmod(name, mode)
        os.replace(name, path)
        directory = os.open(path.parent, os.O_RDONLY)
        try:
            os.fsync(directory)
        finally:
            os.close(directory)
    finally:
        if os.path.exists(name):
            os.unlink(name)


def initialize(args):
    base = args.directory
    private_dir(base)
    key, certificate = base / "root.key", base / "root.crt"
    if key.exists() or certificate.exists() or key.is_symlink() or certificate.is_symlink():
        raise CAError("CA already exists or is incomplete; restore it instead of creating a replacement")
    phrase = password(args, new=True)
    with tempfile.TemporaryDirectory(prefix=".ca-", dir=base) as work:
        work = Path(work)
        openssl(["genpkey", "-algorithm", "EC", "-pkeyopt", "ec_paramgen_curve:P-256", "-pkeyopt", "ec_param_enc:named_curve", "-aes-256-cbc", "-pass", "{PASSFD}", "-out", work / "root.key"], phrase)
        openssl([
            "req", "-new", "-x509", "-sha256", "-days", "3650",
            "-key", work / "root.key", "-passin", "{PASSFD}",
            "-subj", "/CN=Homelab Internal CA", "-out", work / "root.crt",
            "-addext", "basicConstraints=critical,CA:TRUE,pathlen:0",
            "-addext", "keyUsage=critical,keyCertSign,cRLSign",
            "-addext", "subjectKeyIdentifier=hash",
        ], phrase)
        # Install the key first. An interruption must never silently mint a new CA.
        atomic_file(key, (work / "root.key").read_bytes())
        atomic_file(certificate, (work / "root.crt").read_bytes(), 0o644)
    fingerprint, _ = openssl(["x509", "-in", certificate, "-noout", "-sha256", "-fingerprint"])
    print(json.dumps({"rootCertificate": str(certificate), "rootFingerprint": fingerprint.strip()}, indent=2))


def names_for(args):
    names = sorted(set(args.dns_name))
    for name in names:
        if not re.fullmatch(r"(?:\*\.)?(?:[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?\.)*home\.arpa", name) or len(name) > 253:
            raise CAError("Supply lowercase home.arpa DNS names; a leading *. wildcard is supported")
    return names


def issue(args):
    base = args.directory
    private_dir(base)
    private_file(base / "root.key")
    if not (base / "root.crt").is_file() or (base / "root.crt").is_symlink():
        raise CAError("Restore the original CA certificate before issuing certificates")
    names = names_for(args)
    phrase = password(args)
    out = base / "issued" / args.name
    private_dir(out.parent)
    private_dir(out)
    key = out / "key.pem"
    if key.exists() or key.is_symlink():
        private_file(key)
    elif (out / "cert.pem").exists() or (out / "fullchain.pem").exists():
        raise CAError("Leaf key is missing; restore it instead of silently changing a pinned key")
    with tempfile.TemporaryDirectory(prefix=".issue-", dir=base) as work:
        work = Path(work)
        if not key.exists():
            openssl(["genpkey", "-algorithm", "EC", "-pkeyopt", "ec_paramgen_curve:P-256", "-pkeyopt", "ec_param_enc:named_curve", "-out", work / "key.pem"])
            atomic_file(key, (work / "key.pem").read_bytes())
        openssl(["req", "-new", "-sha256", "-key", key, "-subj", "/CN=" + names[0], "-out", work / "request.csr"])
        extensions = "\n".join([
            "basicConstraints=critical,CA:FALSE", "keyUsage=critical,digitalSignature",
            "extendedKeyUsage=serverAuth", "subjectKeyIdentifier=hash", "authorityKeyIdentifier=keyid,issuer",
            "subjectAltName=" + ",".join("DNS:" + name for name in names), "",
        ])
        (work / "extensions.cnf").write_text(extensions)
        openssl([
            "x509", "-req", "-sha256", "-days", "90", "-in", work / "request.csr",
            "-CA", base / "root.crt", "-CAkey", base / "root.key", "-passin", "{PASSFD}",
            "-set_serial", "0x" + secrets.token_hex(16), "-extfile", work / "extensions.cnf", "-out", work / "cert.pem",
        ], phrase)
        openssl(["verify", "-CAfile", base / "root.crt", work / "cert.pem"])
        leaf = (work / "cert.pem").read_bytes()
        atomic_file(out / "cert.pem", leaf, 0o644)
        atomic_file(out / "fullchain.pem", leaf + (base / "root.crt").read_bytes(), 0o644)
    print(json.dumps({
        "dnsNames": names, "certificate": str(out / "cert.pem"),
        "privateKey": str(key), "fullchain": str(out / "fullchain.pem"),
        "rootCertificate": str(base / "root.crt"),
        "renewal": "Run the same issue command before expiry; it reuses key.pem. Re-upload the renewed certificate in NPM.",
    }, indent=2))


def check(args):
    certificate = args.directory / "issued" / args.name / "cert.pem"
    text, _ = openssl(["x509", "-in", certificate, "-noout", "-enddate", "-sha256", "-fingerprint"])
    _, code = openssl(["x509", "-in", certificate, "-noout", "-checkend", str(args.warn_days * 86400)], ok_codes=(0, 1))
    print(json.dumps({"certificate": str(certificate), "details": text.strip(), "renewalNeeded": code != 0}, indent=2))
    return code


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--directory", required=True, type=Path)
    parser.add_argument("--password-file", type=Path, help="optional private file for noninteractive local use; never put a passphrase in arguments")
    commands = parser.add_subparsers(dest="command", required=True)
    commands.add_parser("init")
    issue_parser = commands.add_parser("issue")
    issue_parser.add_argument("--name", required=True)
    issue_parser.add_argument("--dns-name", action="append", required=True)
    check_parser = commands.add_parser("check")
    check_parser.add_argument("--name", required=True)
    check_parser.add_argument("--warn-days", type=int, default=30)
    args = parser.parse_args()
    args.directory = args.directory.expanduser().absolute()
    if getattr(args, "name", None) and not re.fullmatch(r"[a-z0-9][a-z0-9-]{0,62}", args.name):
        parser.error("name must be a simple lowercase filename label")
    if getattr(args, "warn_days", 30) < 0:
        parser.error("warn-days must be nonnegative")
    if shutil.which("openssl") is None:
        parser.error("OpenSSL or LibreSSL is required")
    os.umask(0o077)
    try:
        if args.command == "init":
            initialize(args)
        elif args.command == "issue":
            issue(args)
        else:
            return check(args)
    except (CAError, OSError, subprocess.TimeoutExpired) as err:
        print("homelab-ca: " + (str(err) if isinstance(err, CAError) else "local operation failed; check files and OpenSSL"), file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
