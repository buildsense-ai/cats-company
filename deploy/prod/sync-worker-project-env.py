#!/usr/bin/env python3
"""Synchronize Tianyi worker and image enterprise-project IDs in prod.env."""

from __future__ import annotations

import argparse
import os
import re
import stat
import sys
from pathlib import Path


WORKER_KEY = "CTYUN_WORKER_PROJECT_ID"
IMAGE_KEY = "CTYUN_IMAGE_PROJECT_ID"
MANAGED_KEYS = (WORKER_KEY, IMAGE_KEY)
PROJECT_ID_PATTERN = re.compile(r"^(?:0|[0-9a-fA-F]{32})$")


def normalize_project_id(value: str, name: str) -> str:
    normalized = str(value or "").strip()
    if not PROJECT_ID_PATTERN.fullmatch(normalized):
        raise ValueError(f"{name} must be 0 or a 32-character hexadecimal project ID")
    return normalized


def render(source: str, worker_project_id: str, image_project_id: str) -> str:
    updates = {
        WORKER_KEY: normalize_project_id(worker_project_id, WORKER_KEY),
        IMAGE_KEY: normalize_project_id(image_project_id, IMAGE_KEY),
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


def update_file(env_file: Path, worker_project_id: str, image_project_id: str) -> None:
    source = env_file.read_text(encoding="utf-8", errors="replace")
    rendered = render(source, worker_project_id, image_project_id)
    current_mode = stat.S_IMODE(env_file.stat().st_mode)
    temporary = env_file.with_name(f".{env_file.name}.{os.getpid()}.tmp")
    try:
        with temporary.open("w", encoding="utf-8", newline="\n") as handle:
            handle.write(rendered)
            handle.flush()
            os.fsync(handle.fileno())
        os.chmod(temporary, current_mode)
        os.replace(temporary, env_file)
    finally:
        temporary.unlink(missing_ok=True)


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("env_file", type=Path)
    args = parser.parse_args()
    values = sys.stdin.buffer.read().split(b"\0")
    if len(values) < 2:
        raise SystemExit("expected worker and image project IDs on stdin")
    try:
        update_file(
            args.env_file,
            values[0].decode("utf-8"),
            values[1].decode("utf-8"),
        )
    except ValueError as error:
        raise SystemExit(str(error)) from error


if __name__ == "__main__":
    main()
