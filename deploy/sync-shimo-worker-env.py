#!/usr/bin/env python3
"""Synchronize the optional Shimo browser Worker settings into an env file.

Worker credentials arrive over the deployment runner's standard input.  An
all-empty payload is a no-op, so existing deployments are not changed until
the operator provisions the three Worker values.  A complete payload enables
the ``shimo`` Compose profile and keeps the Worker off ordinary deployments.
"""

from __future__ import annotations

import argparse
import base64
import binascii
import os
import re
import sys
import tempfile
from pathlib import Path
from typing import BinaryIO
from urllib.parse import urlparse


WORKER_TOKEN = "CATSCO_SHIMO_WORKER_TOKEN"
SESSION_KEY = "SHIMO_WORKER_SESSION_KEY"
WORKER_URL = "CATSCO_SHIMO_WORKER_URL"
PROFILES = "COMPOSE_PROFILES"
ENABLED = "SHIMO_WORKER_ENABLED"
HEX_KEY = re.compile(r"^[0-9a-fA-F]{64}$")


def _valid_session_key(value: str) -> bool:
    if HEX_KEY.fullmatch(value):
        return True
    try:
        decoded = base64.b64decode(value, validate=True)
    except (ValueError, binascii.Error):
        return False
    return len(decoded) == 32


def normalize_values(token: str, session_key: str, worker_url: str, enabled: str) -> tuple[str, str, str, bool]:
    raw = ((token, WORKER_TOKEN), (session_key, SESSION_KEY), (worker_url, WORKER_URL), (enabled, ENABLED))
    for value, name in raw:
        if "\n" in value or "\r" in value or "\0" in value:
            raise ValueError(f"{name} must be a single-line value")
    token, session_key, worker_url, enabled = (value.strip() for value, _ in raw)
    if enabled not in {"", "0", "1"}:
        raise ValueError(f"{ENABLED} must be 0 or 1")
    if enabled == "0":
        return "", "", "", False
    if not any((token, session_key, worker_url)) and enabled == "":
        return "", "", "", False
    if enabled != "1":
        raise ValueError(f"{ENABLED}=1 is required when Worker credentials are provided")
    if len(token) < 32:
        raise ValueError(f"{WORKER_TOKEN} must contain at least 32 characters")
    if not _valid_session_key(session_key):
        raise ValueError(f"{SESSION_KEY} must be 64 hex characters or base64 for 32 bytes")
    parsed = urlparse(worker_url)
    if parsed.scheme not in {"http", "https"} or not parsed.hostname or parsed.query or parsed.fragment:
        raise ValueError(f"{WORKER_URL} must be an http(s) URL without query or fragment")
    return token, session_key, worker_url.rstrip("/"), True


def read_values(stream: BinaryIO) -> tuple[str, str, str, str]:
    parts = stream.read().split(b"\0")
    if len(parts) != 5 or parts[-1] != b"":
        raise ValueError("expected exactly four NUL-delimited Shimo Worker values")
    try:
        decoded = [part.decode("utf-8") for part in parts[:-1]]
    except UnicodeDecodeError as error:
        raise ValueError("Shimo Worker values must be valid UTF-8") from error
    normalize_values(*decoded)
    # Keep the wire representation for update_file/render. The normalized
    # boolean is an internal convenience and must not cross this boundary.
    return decoded[0].strip(), decoded[1].strip(), decoded[2].strip(), decoded[3].strip()


def render(source: str, token: str, session_key: str, worker_url: str, enabled: str) -> str:
    token, session_key, worker_url, active = normalize_values(token, session_key, worker_url, enabled)
    if not active:
        # Explicitly disabled payloads remove stale Worker credentials and the
        # profile, while an entirely empty legacy payload remains a no-op.
        if enabled.strip() == "":
            return source.replace("\ufeff", "").replace("\r\n", "\n")
        lines: list[str] = []
        for raw_line in source.replace("\ufeff", "").replace("\r\n", "\n").splitlines():
            key = raw_line.partition("=")[0].strip() if "=" in raw_line else ""
            if key in {WORKER_TOKEN, SESSION_KEY, WORKER_URL, ENABLED}:
                continue
            if key == PROFILES:
                profiles = [item.strip() for item in raw_line.partition("=")[2].split(",") if item.strip()]
                profiles = [item for item in profiles if item != "shimo"]
                lines.append(f"{PROFILES}={','.join(profiles)}")
            else:
                lines.append(raw_line)
        return "\n".join(lines) + "\n"

    updates = {WORKER_TOKEN: token, SESSION_KEY: session_key, WORKER_URL: worker_url, ENABLED: "1"}
    lines: list[str] = []
    seen: set[str] = set()
    current_profiles = ""
    for raw_line in source.replace("\ufeff", "").replace("\r\n", "\n").splitlines():
        key = ""
        value = ""
        if "=" in raw_line and not raw_line.lstrip().startswith("#"):
            key, _, value = raw_line.partition("=")
            key = key.strip()
        if key in updates:
            if key not in seen:
                lines.append(f"{key}={updates[key]}")
                seen.add(key)
            continue
        if key == PROFILES:
            current_profiles = value.strip()
            if PROFILES not in seen:
                profiles = [item.strip() for item in current_profiles.split(",") if item.strip()]
                if "shimo" not in profiles:
                    profiles.append("shimo")
                lines.append(f"{PROFILES}={','.join(profiles)}")
                seen.add(PROFILES)
            continue
        lines.append(raw_line)

    for key, value in updates.items():
        if key not in seen:
            lines.append(f"{key}={value}")
    if PROFILES not in seen:
        lines.append(f"{PROFILES}=shimo")
    return "\n".join(lines) + "\n"


def update_file(env_file: Path, token: str, session_key: str, worker_url: str, enabled: str) -> None:
    source = env_file.read_text(encoding="utf-8", errors="replace")
    rendered = render(source, token, session_key, worker_url, enabled)
    temporary: Path | None = None
    try:
        with tempfile.NamedTemporaryFile(
            mode="w", encoding="utf-8", newline="\n", dir=env_file.parent,
            prefix=f".{env_file.name}.", suffix=".tmp", delete=False,
        ) as handle:
            temporary = Path(handle.name)
            handle.write(rendered)
            handle.flush()
            os.fsync(handle.fileno())
        os.chmod(temporary, 0o600)
        os.replace(temporary, env_file)
        os.chmod(env_file, 0o600)
    finally:
        if temporary is not None:
            temporary.unlink(missing_ok=True)


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("env_file", type=Path)
    args = parser.parse_args()
    try:
        update_file(args.env_file, *read_values(sys.stdin.buffer))
    except (OSError, ValueError) as error:
        raise SystemExit(f"failed to synchronize Shimo Worker environment: {error}") from error


if __name__ == "__main__":
    main()
