#!/usr/bin/env python3
"""Synchronize the non-public Shimo grant settings into a deploy env file.

The actor secret is supplied over the deployment runner's standard input and
is never placed in the repository or a remote command line.  An empty secret
is intentionally a no-op so deployments can keep Shimo disabled until the
environment secret is provisioned.  The skill id is always written because
it is public configuration and must match the installed SkillHub package.
"""

from __future__ import annotations

import argparse
import os
import re
import sys
import tempfile
from pathlib import Path
from typing import BinaryIO


ACTOR_SECRET = "CATSCO_SHIMO_ACTOR_SECRET"
SKILL_ID = "CATSCO_SHIMO_SKILL_ID"
SKILL_ID_PATTERN = re.compile(r"^[A-Za-z0-9._-]+/[A-Za-z0-9._-]+$")


def normalize_values(actor_secret: str, skill_id: str) -> tuple[str, str]:
    for value, name in ((actor_secret, ACTOR_SECRET), (skill_id, SKILL_ID)):
        if "\n" in value or "\r" in value or "\0" in value:
            raise ValueError(f"{name} must be a single-line value")

    secret = actor_secret.strip()
    package_id = skill_id.strip()
    if secret and len(secret.encode("utf-8")) < 32:
        raise ValueError(f"{ACTOR_SECRET} must contain at least 32 bytes")
    if not SKILL_ID_PATTERN.fullmatch(package_id):
        raise ValueError(f"{SKILL_ID} must use the owner/name format")
    return secret, package_id


def read_values(stream: BinaryIO) -> tuple[str, str]:
    parts = stream.read().split(b"\0")
    if len(parts) != 3 or parts[-1] != b"":
        raise ValueError("expected exactly two NUL-delimited Shimo values")
    try:
        decoded = [part.decode("utf-8") for part in parts[:-1]]
    except UnicodeDecodeError as error:
        raise ValueError("Shimo values must be valid UTF-8") from error
    return normalize_values(*decoded)


def render(source: str, actor_secret: str, skill_id: str) -> str:
    secret, package_id = normalize_values(actor_secret, skill_id)
    updates = {SKILL_ID: package_id}
    if secret:
        updates[ACTOR_SECRET] = secret

    lines: list[str] = []
    seen: set[str] = set()
    managed = {ACTOR_SECRET, SKILL_ID}
    for raw_line in source.replace("\ufeff", "").replace("\r\n", "\n").splitlines():
        key = ""
        if "=" in raw_line and not raw_line.lstrip().startswith("#"):
            key = raw_line.partition("=")[0]
        if key in managed:
            if key in updates and key not in seen:
                lines.append(f"{key}={updates[key]}")
                seen.add(key)
            elif key == ACTOR_SECRET and not secret:
                # An unprovisioned workflow secret must not erase a secret
                # already present on the deployment host.
                lines.append(raw_line)
                seen.add(key)
            continue
        lines.append(raw_line)

    for key, value in updates.items():
        if key not in seen:
            lines.append(f"{key}={value}")
    return "\n".join(lines) + "\n"


def update_file(env_file: Path, actor_secret: str, skill_id: str) -> None:
    source = env_file.read_text(encoding="utf-8", errors="replace")
    rendered = render(source, actor_secret, skill_id)
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
        raise SystemExit(f"failed to synchronize Shimo environment: {error}") from error


if __name__ == "__main__":
    main()
