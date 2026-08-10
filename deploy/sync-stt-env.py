#!/usr/bin/env python3
"""Atomically synchronize the Volcengine streaming STT API key."""

from __future__ import annotations

import argparse
import os
import sys
import tempfile
from pathlib import Path
from typing import BinaryIO


ENABLED_KEY = "CATSCO_STT_ENABLED"
API_KEY = "VOLCENGINE_STT_API_KEY"
LEGACY_KEYS = (
    "VOLCENGINE_STT_APP_ID",
    "VOLCENGINE_STT_ACCESS_TOKEN",
    "VOLCENGINE_STT_CLUSTER",
)
MANAGED_KEYS = (ENABLED_KEY, API_KEY) + LEGACY_KEYS


def normalize_value(api_key: str) -> str | None:
    if "\n" in api_key or "\r" in api_key or "\0" in api_key:
        raise ValueError(f"{API_KEY} must be a single-line value")
    normalized = api_key.strip()
    return normalized or None


def read_value(stream: BinaryIO) -> str | None:
    parts = stream.read().split(b"\0")
    if len(parts) != 2 or parts[-1] != b"":
        raise ValueError("expected exactly one NUL-delimited STT API key")
    try:
        decoded = parts[0].decode("utf-8")
    except UnicodeDecodeError as error:
        raise ValueError("STT API key must be valid UTF-8") from error
    return normalize_value(decoded)


def render(source: str, api_key: str) -> str:
    value = normalize_value(api_key)
    updates = {ENABLED_KEY: "1" if value else "0"}
    if value:
        updates[API_KEY] = value

    lines: list[str] = []
    seen: set[str] = set()
    for raw_line in source.replace("\ufeff", "").replace("\r\n", "\n").splitlines():
        key = ""
        if "=" in raw_line and not raw_line.lstrip().startswith("#"):
            key = raw_line.partition("=")[0]
        if key in MANAGED_KEYS:
            if key in updates and key not in seen:
                lines.append(f"{key}={updates[key]}")
                seen.add(key)
            continue
        lines.append(raw_line)

    for key, value in updates.items():
        if key not in seen:
            lines.append(f"{key}={value}")
    return "\n".join(lines) + "\n"


def update_file(env_file: Path, api_key: str) -> None:
    value = normalize_value(api_key)
    if not env_file.is_file():
        raise FileNotFoundError(f"missing env file: {env_file}")
    source = env_file.read_text(encoding="utf-8", errors="replace")
    rendered = render(source, value or "")

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
    finally:
        if temporary is not None:
            temporary.unlink(missing_ok=True)


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("env_file", type=Path)
    args = parser.parse_args()
    try:
        update_file(args.env_file, read_value(sys.stdin.buffer) or "")
    except (OSError, ValueError) as error:
        raise SystemExit(f"failed to synchronize streaming STT environment: {error}") from error


if __name__ == "__main__":
    main()
