#!/usr/bin/env python3
"""Synchronize the mutually exclusive Artifact node registry env keys."""

from __future__ import annotations

import argparse
import os
import stat
from collections.abc import Mapping
from pathlib import Path


INLINE_KEY = "CATSCO_ARTIFACT_NODES_JSON"
FILE_KEY = "CATSCO_ARTIFACT_NODES_FILE"
MANAGED_KEYS = (INLINE_KEY, FILE_KEY)


def normalize_value(value: str, name: str) -> str:
    normalized = str(value or "").strip()
    if "\n" in normalized or "\r" in normalized or "\0" in normalized:
        raise ValueError(f"{name} must be a single-line value")
    return normalized


def render(source: str, inline_json: str, file_path: str) -> str:
    inline_json = normalize_value(inline_json, INLINE_KEY)
    file_path = normalize_value(file_path, FILE_KEY)
    if inline_json and file_path:
        raise ValueError(f"configure only one of {INLINE_KEY} or {FILE_KEY}")

    updates = {
        INLINE_KEY: inline_json,
        FILE_KEY: file_path,
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


def update_file(env_file: Path, inline_json: str, file_path: str) -> None:
    source = env_file.read_text(encoding="utf-8", errors="replace")
    rendered = render(source, inline_json, file_path)
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


def update_from_environment(env_file: Path, env: Mapping[str, str]) -> bool:
    if INLINE_KEY not in env and FILE_KEY not in env:
        return False
    update_file(env_file, env.get(INLINE_KEY, ""), env.get(FILE_KEY, ""))
    return True


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("env_file", type=Path)
    args = parser.parse_args()
    try:
        update_from_environment(args.env_file, os.environ)
    except ValueError as error:
        raise SystemExit(str(error)) from error


if __name__ == "__main__":
    main()
