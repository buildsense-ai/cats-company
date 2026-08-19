#!/usr/bin/env python3
"""Atomically synchronize Web Push VAPID and relay settings into a deploy env file."""

from __future__ import annotations

import argparse
import os
import sys
import tempfile
from pathlib import Path
from typing import BinaryIO


VAPID_KEYS = (
    "VAPID_PUBLIC_KEY",
    "VAPID_PRIVATE_KEY",
    "VAPID_SUBJECT",
)
RELAY_KEYS = (
    "CATSCO_PUSH_RELAY_URL",
    "CATSCO_PUSH_RELAY_TOKEN",
)
MANAGED_KEYS = VAPID_KEYS + RELAY_KEYS


def normalize_values(
    public_key: str,
    private_key: str,
    subject: str,
    relay_url: str,
    relay_token: str,
) -> tuple[str, str, str, str, str] | None:
    raw_values = (public_key, private_key, subject, relay_url, relay_token)
    for value, name in zip(raw_values, MANAGED_KEYS, strict=True):
        if "\n" in value or "\r" in value or "\0" in value:
            raise ValueError(f"{name} must be a single-line value")

    normalized = tuple(value.strip() for value in raw_values)
    vapid_values = normalized[: len(VAPID_KEYS)]
    relay_values = normalized[len(VAPID_KEYS) :]
    if any(vapid_values) and not all(vapid_values):
        raise ValueError("VAPID values must be configured together or all left empty")
    if any(relay_values) and not all(relay_values):
        raise ValueError("push relay URL and token must be configured together or all left empty")
    if not any(normalized):
        return None
    return normalized


def read_values(stream: BinaryIO) -> tuple[str, str, str, str, str] | None:
    parts = stream.read().split(b"\0")
    if len(parts) != len(MANAGED_KEYS) + 1 or parts[-1] != b"":
        raise ValueError("expected exactly five NUL-delimited Web Push values")

    try:
        decoded = [part.decode("utf-8") for part in parts[:-1]]
    except UnicodeDecodeError as error:
        raise ValueError("Web Push values must be valid UTF-8") from error

    return normalize_values(*decoded)


def render(
    source: str,
    public_key: str,
    private_key: str,
    subject: str,
    relay_url: str,
    relay_token: str,
) -> str:
    values = normalize_values(public_key, private_key, subject, relay_url, relay_token)
    updates = (
        {key: value for key, value in zip(MANAGED_KEYS, values, strict=True) if value}
        if values
        else {}
    )
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

    if updates:
        for key in updates:
            if key not in seen:
                lines.append(f"{key}={updates[key]}")
    return "\n".join(lines) + "\n"


def read_source(env_file: Path) -> str:
    if env_file.is_file():
        return env_file.read_text(encoding="utf-8", errors="replace")
    raise FileNotFoundError(f"missing env file: {env_file}")


def update_file(
    env_file: Path,
    public_key: str,
    private_key: str,
    subject: str,
    relay_url: str,
    relay_token: str,
) -> None:
    # Validate all values before touching the destination file.
    values = normalize_values(public_key, private_key, subject, relay_url, relay_token)
    source = read_source(env_file)
    rendered = render(source, *(values or ("", "", "", "", "")))

    env_file.parent.mkdir(parents=True, exist_ok=True)
    temporary: Path | None = None
    try:
        with tempfile.NamedTemporaryFile(
            mode="w",
            encoding="utf-8",
            newline="\n",
            dir=env_file.parent,
            prefix=f".{env_file.name}.",
            suffix=".tmp",
            delete=False,
        ) as handle:
            temporary = Path(handle.name)
            handle.write(rendered)
            handle.flush()
            os.fsync(handle.fileno())
        # A legacy env file may be mode 0644 because it was copied from a
        # checked-in example. It can contain VAPID and relay secrets, so always
        # replace it with an owner-only file instead of preserving that mode.
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
        values = read_values(sys.stdin.buffer)
        update_file(
            args.env_file,
            *(values or ("", "", "", "", "")),
        )
    except (OSError, ValueError) as error:
        raise SystemExit(f"failed to synchronize Web Push environment: {error}") from error


if __name__ == "__main__":
    main()
