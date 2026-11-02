#!/usr/bin/env python3
"""Portable local TLS setup. Copy this file into its final folder and run it.

Outputs, passphrase and temporary files stay beside the script, independent of
where the command is run. No packages, services, DNS or trust entries are installed.
Requires Python 3.9+ and OpenSSL/LibreSSL on macOS or Linux.
"""
import argparse
from contextlib import contextmanager
import fcntl
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


MAX_FILE_BYTES = 256 * 1024
GUIDE_NAME = "COMO-USAR-CERTIFICADOS.md"
GUIDE_HEADING = "# Certificados locais — homelab-ca.py\n"


def regular_file(path, private=False):
    try:
        info = path.lstat()
    except OSError:
        raise CAError("Falta um ficheiro necessário; restaura a pasta original em vez de recriar a CA.") from None
    if not stat.S_ISREG(info.st_mode) or info.st_size > MAX_FILE_BYTES:
        raise CAError("Os ficheiros geridos têm de ser ficheiros normais, sem links simbólicos.")
    if private and stat.S_IMODE(info.st_mode) & 0o077:
        raise CAError("Um ficheiro secreto tem permissões demasiado abertas; usa 0600 ou 0400.")


def private_dir(path):
    if not path.exists() and not path.is_symlink():
        path.mkdir(mode=0o700)
    info = path.lstat()
    if not stat.S_ISDIR(info.st_mode) or stat.S_IMODE(info.st_mode) & 0o077:
        raise CAError("As subpastas geridas têm de ser privadas (0700), sem links simbólicos.")


@contextmanager
def folder_lock(base):
    # Lock the existing folder descriptor: no lock/pid file is left behind.
    descriptor = os.open(base, os.O_RDONLY)
    try:
        try:
            fcntl.flock(descriptor, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError:
            raise CAError("Já há outra execução nesta pasta; espera que termine.") from None
        yield
    finally:
        os.close(descriptor)


def atomic_file(path, data, mode=0o600):
    if path.exists() or path.is_symlink():
        regular_file(path, private=(mode == 0o600))
    descriptor, name = tempfile.mkstemp(prefix="tls-write-", dir=path.parent)
    try:
        with os.fdopen(descriptor, "wb") as stream:
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


def openssl(arguments, work, passphrase=None, ok_codes=(0,)):
    read_fd = None
    try:
        if passphrase is not None:
            read_fd, write_fd = os.pipe()
            try:
                os.write(write_fd, passphrase.encode("ascii") + b"\n")
            finally:
                os.close(write_fd)
            arguments = [("fd:" + str(read_fd)) if str(item) == "{PASSFD}" else str(item) for item in arguments]
        process = subprocess.run(
            ["openssl", *map(str, arguments)],
            stdin=subprocess.DEVNULL, capture_output=True, cwd=work,
            pass_fds=(() if read_fd is None else (read_fd,)),
            env={**os.environ, "LC_ALL": "C", "RANDFILE": str(work / "openssl-random")},
            timeout=30,
        )
        if process.returncode not in ok_codes:
            raise CAError("O OpenSSL falhou. Confirma os ficheiros e a passphrase guardada; a CA existente foi preservada.")
        return process.stdout.decode("utf-8"), process.returncode
    finally:
        if read_fd is not None:
            os.close(read_fd)


def ca_paths(base):
    return base / "ca" / "root.key", base / "ca" / "root.crt", base / "ca" / "passphrase.txt"


def ensure_ca(base, work, check_only=False):
    key, certificate, password_file = ca_paths(base)
    existing = [path.exists() or path.is_symlink() for path in (key, certificate, password_file)]
    if any(existing) and not all(existing):
        raise CAError("A CA está incompleta. Restaura root.key, root.crt e passphrase.txt; não foi criada outra CA.")
    if not any(existing):
        if check_only:
            raise CAError("Ainda não existe uma CA nesta pasta.")
        private_dir(base / "ca")
        phrase = secrets.token_urlsafe(48)
        atomic_file(password_file, (phrase + "\n").encode("ascii"))
        openssl(["genpkey", "-algorithm", "EC", "-pkeyopt", "ec_paramgen_curve:P-256", "-pkeyopt", "ec_param_enc:named_curve", "-aes-256-cbc", "-pass", "{PASSFD}", "-out", work / "root.key"], work, phrase)
        openssl([
            "req", "-new", "-x509", "-sha256", "-days", "3650",
            "-key", work / "root.key", "-passin", "{PASSFD}",
            "-subj", "/CN=Homelab Internal CA", "-out", work / "root.crt",
            "-addext", "basicConstraints=critical,CA:TRUE,pathlen:0",
            "-addext", "keyUsage=critical,keyCertSign,cRLSign",
            "-addext", "subjectKeyIdentifier=hash",
        ], work, phrase)
        atomic_file(key, (work / "root.key").read_bytes())
        atomic_file(certificate, (work / "root.crt").read_bytes(), 0o644)
    else:
        private_dir(base / "ca")
        regular_file(key, private=True)
        regular_file(certificate)
        regular_file(password_file, private=True)
        phrase = password_file.read_text(encoding="ascii").rstrip("\r\n")
        if not re.fullmatch(r"[A-Za-z0-9_-]{64}", phrase):
            raise CAError("A passphrase guardada é inválida; restaura a original.")
    # A copied/restored folder must contain the matching CA key and certificate.
    actual, _ = openssl(["pkey", "-in", key, "-passin", "{PASSFD}", "-pubout"], work, phrase)
    expected, _ = openssl(["x509", "-in", certificate, "-noout", "-pubkey"], work)
    if actual != expected:
        raise CAError("A chave e o certificado da CA não correspondem; não foi criada outra CA.")
    openssl(["verify", "-check_ss_sig", "-CAfile", certificate, certificate], work)
    openssl(["x509", "-in", certificate, "-noout", "-checkend", "0"], work)
    return phrase


def normalize_names(values):
    names = sorted(set(name.lower() for value in values for name in re.split(r"[,\s]+", value.strip()) if name))
    if not names:
        raise CAError("Indica pelo menos um nome DNS.")
    for name in names:
        if len(name) > 253 or not re.fullmatch(r"(?:\*\.)?[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?(?:\.[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?)*", name):
            raise CAError("Nome DNS inválido: usa nomes como gateway.home.arpa ou *.home.arpa, sem URLs ou portas.")
        if all(label.isdigit() for label in name.split(".")):
            raise CAError("Este script emite nomes DNS; endereços IP precisam de certificados com IP SAN.")
    return names


def read_profile(out):
    path = out / "details.json"
    if not path.exists() and not path.is_symlink():
        return None
    regular_file(path)
    try:
        details = json.loads(path.read_text(encoding="utf-8"))
        if details["schemaVersion"] != 1 or not isinstance(details["dnsNames"], list) or not all(isinstance(n, str) for n in details["dnsNames"]):
            raise ValueError()
        normalize_names(details["dnsNames"])
    except (ValueError, KeyError, TypeError):
        raise CAError("O registo de nomes está inválido; restaura details.json.") from None
    return details


def choose_names(args, out):
    details = read_profile(out)
    if args.dns_name:
        return normalize_names(args.dns_name)
    defaults = details["dnsNames"] if details else ["*.home.arpa"]
    if args.check:
        return normalize_names(defaults)
    try:
        value = input("Nomes DNS, separados por vírgulas [" + ", ".join(defaults) + "]: ")
    except EOFError:
        raise CAError("Sem entrada interativa: indica os nomes com --dns-name '*.home.arpa'.") from None
    return normalize_names([value] if value.strip() else defaults)


def issue(base, out, names, phrase, work):
    key = out / "key.pem"
    if key.exists() or key.is_symlink():
        regular_file(key, private=True)
    elif any((out / name).exists() or (out / name).is_symlink() for name in ("cert.pem", "fullchain.pem", "details.json")):
        raise CAError("Falta a chave deste certificado. Restaura key.pem para preservar a identidade TLS.")
    if not key.exists():
        openssl(["genpkey", "-algorithm", "EC", "-pkeyopt", "ec_paramgen_curve:P-256", "-pkeyopt", "ec_param_enc:named_curve", "-out", work / "key.pem"], work)
        atomic_file(key, (work / "key.pem").read_bytes())
    openssl(["req", "-new", "-sha256", "-key", key, "-subj", "/CN=" + names[0], "-out", work / "request.csr"], work)
    extensions = "\n".join([
        "basicConstraints=critical,CA:FALSE", "keyUsage=critical,digitalSignature", "extendedKeyUsage=serverAuth",
        "subjectKeyIdentifier=hash", "authorityKeyIdentifier=keyid,issuer",
        "subjectAltName=" + ",".join("DNS:" + name for name in names), "",
    ])
    (work / "extensions.cnf").write_text(extensions, encoding="ascii")
    root_key, root_cert, _ = ca_paths(base)
    openssl([
        "x509", "-req", "-sha256", "-days", "90", "-in", work / "request.csr",
        "-CA", root_cert, "-CAkey", root_key, "-passin", "{PASSFD}", "-set_serial", "0x" + secrets.token_hex(16),
        "-extfile", work / "extensions.cnf", "-out", work / "cert.pem",
    ], work, phrase)
    openssl(["verify", "-purpose", "sslserver", "-CAfile", root_cert, work / "cert.pem"], work)
    actual, _ = openssl(["pkey", "-in", key, "-pubout"], work)
    expected, _ = openssl(["x509", "-in", work / "cert.pem", "-noout", "-pubkey"], work)
    if actual != expected:
        raise CAError("O certificado e a chave não correspondem.")
    leaf = (work / "cert.pem").read_bytes()
    atomic_file(out / "cert.pem", leaf, 0o644)
    atomic_file(out / "fullchain.pem", leaf + root_cert.read_bytes(), 0o644)
    atomic_file(out / "details.json", (json.dumps({"schemaVersion": 1, "dnsNames": names}, indent=2) + "\n").encode("utf-8"), 0o644)


def report(base, out, names, work):
    _, root_cert, _ = ca_paths(base)
    regular_file(out / "cert.pem")
    regular_file(out / "key.pem", private=True)
    openssl(["verify", "-purpose", "sslserver", "-CAfile", root_cert, out / "cert.pem"], work)
    actual, _ = openssl(["pkey", "-in", out / "key.pem", "-pubout"], work)
    expected, _ = openssl(["x509", "-in", out / "cert.pem", "-noout", "-pubkey"], work)
    if actual != expected:
        raise CAError("O certificado e a chave não correspondem.")
    expiry, _ = openssl(["x509", "-in", out / "cert.pem", "-noout", "-enddate"], work)
    fingerprint, _ = openssl(["x509", "-in", root_cert, "-noout", "-sha256", "-fingerprint"], work)
    _, due = openssl(["x509", "-in", out / "cert.pem", "-noout", "-checkend", str(30 * 86400)], work, ok_codes=(0, 1))
    relative = lambda path: path.relative_to(base).as_posix()
    return {
        "dnsNames": names, "rootCertificate": relative(root_cert), "rootFingerprint": fingerprint.strip(),
        "certificate": relative(out / "cert.pem"), "privateKey": relative(out / "key.pem"),
        "fullchain": relative(out / "fullchain.pem"), "passphraseFile": "ca/passphrase.txt",
        "expiresAt": expiry.strip().removeprefix("notAfter="), "renewalNeeded": due != 0,
        "instructions": GUIDE_NAME,
    }


def write_guide(base, result):
    text = GUIDE_HEADING + "\n" + f"Nomes: {', '.join(result['dnsNames'])}\n\nValidade: {result['expiresAt']}\n\n"
    text += f"Impressão digital da CA: `{result['rootFingerprint']}`\n\n"
    text += f"""## Nginx Proxy Manager

1. Abre SSL Certificates → Add SSL Certificate → Custom.
2. Certificate: `{result['certificate']}`.
3. Certificate Key: `{result['privateKey']}`.
4. Intermediate Certificate: vazio; esta CA assina o certificado diretamente.
5. Guarda, seleciona esse certificado no separador SSL do Proxy Host e valida
   o acesso HTTPS. Depois podes ativar Force SSL.

## Confiança nos dispositivos

Importa apenas `{result['rootCertificate']}` nos dispositivos que acedem aos sites.
No Mac: Acesso às Chaves / Keychain Access → login → importar o certificado →
abrir Homelab Internal CA → Trust → confiar para SSL. Confirma a impressão digital
acima. Repete em cada dispositivo; alguns browsers usam uma loja própria.
Nos serviços/containers, configura essa CA conforme o cliente TLS que utilizam.
Para NoX Gateway: NOX_GATEWAY_TLS_CA_FILE aponta para o certificado raiz.
Isto não cria entradas DNS, não altera o proxy e não expõe serviços à Internet.

## Ficheiros privados

`ca/root.key`: chave privada da CA, cifrada.
`ca/passphrase.txt`: passphrase gerada automaticamente; não a publiques.
`{result['privateKey']}`: chave privada do servidor; entrega-a apenas ao proxy/servidor.
Nunca importes root.key ou passphrase.txt no Nginx ou no Porta-Chaves.
A passphrase e a chave da CA ficam juntas: protege a pasta inteira.

## Renovar e mudar de máquina

O certificado dura 90 dias. Executa novamente o script para renovar; pode usar
os nomes guardados (Enter) ou novos nomes. Reutiliza a CA e a chave do servidor.
Volta a importar o certificado renovado no proxy e atualiza as cópias usadas pelos
serviços. A verificação avisa quando faltam menos de 30 dias.

Para continuar com a MESMA CA noutro computador, copia a PASTA TODA, preservando
as permissões. Os caminhos são relativos. Copiar só o script cria outra CA,
que precisará da sua própria configuração de confiança.
Subpastas privadas: 0700. Ficheiros secretos: 0600/0400.
Requer Python 3.9+ e OpenSSL/LibreSSL em macOS/Linux.

## Limpeza

Para remover a configuração, primeiro retira o certificado dos hosts e apaga-o
no proxy; depois remove a CA dos dispositivos onde a importaste. Podes então
apagar esta pasta. Apagar a pasta sozinha não remove a confiança já instalada.
Se já emparelhaste um Gateway, revoga a associação no Backend antes de remover
o estado do Gateway. Esta ferramenta não instala nada automaticamente fora
desta pasta. Não apagues volumes do Backend ou do Nginx para limpar certificados.
"""
    atomic_file(base / GUIDE_NAME, text.encode("utf-8"), 0o644)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--dns-name", action="append", help="nome DNS; repetir ou separar por vírgulas para vários nomes")
    parser.add_argument("--name", default="server", help="nome do conjunto de certificados (default: server)")
    parser.add_argument("--check", action="store_true", help="validar o certificado existente sem renovar")
    parser.add_argument("--json", action="store_true", help="resultado JSON, com caminhos relativos e sem segredos")
    args = parser.parse_args()
    if not re.fullmatch(r"[a-z0-9][a-z0-9-]{0,62}", args.name):
        parser.error("name tem de ser um identificador simples, em minúsculas")
    if args.json and not args.check and not args.dns_name:
        parser.error("usa --dns-name com --json para executar sem perguntas")
    if shutil.which("openssl") is None:
        parser.error("é necessário OpenSSL/LibreSSL; não foi instalado nada automaticamente")
    base = Path(__file__).resolve().parent
    out = base / "certificates" / args.name
    os.umask(0o077)
    try:
        with folder_lock(base):
            if not args.check:
                guide = base / GUIDE_NAME
                if guide.exists() or guide.is_symlink():
                    regular_file(guide)
                    if not guide.read_text(encoding="utf-8").startswith(GUIDE_HEADING):
                        raise CAError("Já existe um guia com esse nome que não foi criado por este script; foi preservado.")
            names = choose_names(args, out)
            if not args.check:
                private_dir(base / "certificates")
                private_dir(out)
            else:
                if read_profile(out) is None:
                    raise CAError("Não existe este conjunto de certificados nesta pasta.")
                if args.dns_name:
                    raise CAError("--check usa os nomes guardados; não alteres nomes durante a verificação.")
            with tempfile.TemporaryDirectory(prefix="tls-work-", dir=base) as temporary:
                work = Path(temporary)
                phrase = ensure_ca(base, work, check_only=args.check)
                if not args.check:
                    issue(base, out, names, phrase, work)
                result = report(base, out, names, work)
            if not args.check:
                write_guide(base, result)
        if args.json:
            print(json.dumps(result, ensure_ascii=False, indent=2))
        else:
            print("\nCertificados verificados." if args.check else "\nCertificados prontos. A CA e a passphrase foram guardadas automaticamente.")
            print("Pasta: " + str(base))
            print("Nomes: " + ", ".join(result["dnsNames"]))
            print("Certificado para o Nginx: " + result["certificate"])
            print("Chave para o Nginx: " + result["privateKey"])
            print("CA para os dispositivos: " + result["rootCertificate"])
            print("Instruções: " + GUIDE_NAME)
            print("Validade: " + result["expiresAt"])
            if result["renewalNeeded"]:
                print("É necessário renovar o certificado (validade inferior a 30 dias).")
            print("Para manter a mesma CA noutra máquina, copia a pasta inteira.")
        return 1 if args.check and result["renewalNeeded"] else 0
    except KeyboardInterrupt:
        print("\nOperação interrompida. Os ficheiros existentes foram preservados.", file=sys.stderr)
        return 130
    except (CAError, OSError, UnicodeError, subprocess.TimeoutExpired) as err:
        message = str(err) if isinstance(err, CAError) else "a operação local falhou; confirma permissões, ficheiros e OpenSSL"
        print("homelab-ca: " + message, file=sys.stderr)
        return 1


if __name__ == "__main__":
    sys.exit(main())
