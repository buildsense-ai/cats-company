#!/usr/bin/env python3
"""Atomically synchronize Web Push VAPID settings into a deploy env file."""

from __future__ import annotations

import argparse
import os
import sys
import tempfile
from pathlib import Path
from typing import BinaryIO


MANAGED_KEYS = (
    "VAPID_PUBLIC_KEY",
    "VAPID_PRIVATE_KEY",
    "VAPID_SUBJECT",
)


def normalize_value(value: str, name: str) -> str:
    if "\n" in value or "\r" in value or "\0" in value:
        raise ValueError(f"{name} must be a single-line value")
    normalized = value.strip()
    if not normalized:
        raise ValueError(f"{name} must not be empty")
    return normalized


def read_values(stream: BinaryIO) -> tuple[str, str, str]:
    parts = stream.read().split(b"\0")
    if len(parts) != len(MANAGED_KEYS) + 1 or parts[-1] != b"":
        raise ValueError("expected exactly three NUL-delimited VAPID values")

    try:
        decoded = [part.decode("utf-8") for part in parts[:-1]]
    except UnicodeDecodeError as error:
        raise ValueError("VAPID values must be valid UTF-8") from error

    return (
        normalize_value(decoded[0], "VAPID_PUBLIC_KEY"),
        normalize_value(decoded[1], "VAPID_PRIVATE_KEY"),
        normalize_value(decoded[2], "VAPID_SUBJECT"),
    )


def render(source: str, public_key: str, private_key: str, subject: str) -> str:
    updates = {
        "VAPID_PUBLIC_KEY": normalize_value(public_key, "VAPID_PUBLIC_KEY"),
        "VAPID_PRIVATE_KEY": normalize_value(private_key, "VAPID_PRIVATE_KEY"),
        "VAPID_SUBJECT": normalize_value(subject, "VAPID_SUBJECT"),
    }
    lines: list[str] = []
    seen: set[str] = set()
    for raw_line in source.replace("\ufeff", "").replace("\r\n", "\n").splitlines():
        key = ""
        if "=" in raw_line and not raw_line.lstrip().startswith("#"):
            key = raw_line.partition("=")[0]
        if key in updates:
            if key not in seen:
                lines.append(f"{key}={updates[key]}")
                seen.add(key)
            continue
        lines.append(raw_line)

    for key in MANAGED_KEYS:
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
) -> None:
    # Validate all values before touching the destination file.
    public_key = normalize_value(public_key, "VAPID_PUBLIC_KEY")
    private_key = normalize_value(private_key, "VAPID_PRIVATE_KEY")
    subject = normalize_value(subject, "VAPID_SUBJECT")
    source = read_source(env_file)
    rendered = render(source, public_key, private_key, subject)

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
        # checked-in example. It now contains a VAPID private key, so always
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
        public_key, private_key, subject = read_values(sys.stdin.buffer)
        update_file(
            args.env_file,
            public_key,
            private_key,
            subject,
        )
    except (OSError, ValueError) as error:
        raise SystemExit(f"failed to synchronize VAPID environment: {error}") from error


if __name__ == "__main__":
    main()
