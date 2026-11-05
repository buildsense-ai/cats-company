#!/usr/bin/env python3
"""Install an APNs key from stdin; never put private key material in prod.env."""
from __future__ import annotations

import base64
import os
from pathlib import Path
import re
import sys
import tempfile

KEYS = ("APNS_TEAM_ID", "APNS_KEY_ID", "APNS_BUNDLE_ID", "APNS_KEY_FILE")


def atomic_write(path: Path, data: bytes) -> None:
    fd, temporary = tempfile.mkstemp(prefix=f".{path.name}.", dir=path.parent)
    try:
        with os.fdopen(fd, "wb") as handle:
            handle.write(data)
            handle.flush()
            os.fsync(handle.fileno())
        os.chmod(temporary, 0o600)
        os.replace(temporary, path)
    finally:
        Path(temporary).unlink(missing_ok=True)


def synchronize(env_path: Path, secret_dir: Path, fields: list[str]) -> None:
    if len(fields) != 4:
        raise ValueError("expected four APNs values")
    team, key_id, bundle, encoded = (v.strip() for v in fields)
    configured = any((team, key_id, bundle, encoded))
    if configured:
        if not all(re.fullmatch(r"[A-Z0-9]{10}", v) for v in (team, key_id)):
            raise ValueError("invalid APNs team/key identifier")
        if not re.fullmatch(r"[A-Za-z0-9.-]{3,200}", bundle):
            raise ValueError("invalid APNs bundle identifier")
        try:
            private_key = base64.b64decode(encoded, validate=True)
        except ValueError as error:
            raise ValueError("invalid APNs key encoding") from error
        if not (private_key.startswith(b"-----BEGIN " + b"PRIVATE KEY-----\n")
                and private_key.rstrip().endswith(b"-----END " + b"PRIVATE KEY-----")):
            raise ValueError("expected a PKCS8 PEM private key")
    source = env_path.read_text(encoding="utf-8")
    lines = [line for line in source.splitlines() if line.partition("=")[0] not in KEYS]
    values = (team, key_id, bundle, "/run/catsco-secrets/apns.p8") if configured else ("", "", "", "")
    lines.extend(f"{key}={value}" for key, value in zip(KEYS, values, strict=True))
    if configured:
        secret_dir.mkdir(parents=True, exist_ok=True, mode=0o700)
        atomic_write(secret_dir / "apns.p8", private_key)
    atomic_write(env_path, ("\n".join(lines) + "\n").encode())


def main() -> None:
    if len(sys.argv) != 3:
        raise SystemExit("usage: sync-apns-env.py ENV_FILE SECRET_DIRECTORY")
    try:
        raw = sys.stdin.buffer.read(16385)
        parts = raw.split(b"\0")
        if len(raw) > 16384 or len(parts) != 5 or parts[-1] != b"":
            raise ValueError("expected four NUL-delimited APNs values")
        synchronize(Path(sys.argv[1]), Path(sys.argv[2]), [part.decode() for part in parts[:-1]])
    except (OSError, ValueError, UnicodeError):
        # Validation errors must never echo any input (including the key).
        raise SystemExit("failed to synchronize APNs configuration") from None


if __name__ == "__main__":
    main()
