#!/usr/bin/env python3
"""Render a host Nginx config with a bounded /v1/ timeout update."""

from __future__ import annotations

import argparse
import re
from pathlib import Path


def matching_brace(text: str, open_index: int) -> int:
    depth = 0
    for index in range(open_index, len(text)):
        char = text[index]
        if char == "{":
            depth += 1
        elif char == "}":
            depth -= 1
            if depth == 0:
                return index + 1
    raise ValueError("unbalanced Nginx block")


def block_spans(text: str, pattern: str) -> list[tuple[int, int]]:
    spans: list[tuple[int, int]] = []
    for match in re.finditer(pattern, text, flags=re.MULTILINE):
        open_index = text.find("{", match.start(), match.end())
        spans.append((match.start(), matching_brace(text, open_index)))
    return spans


def replace_directive(block: str, directive: str, timeout_seconds: int) -> str:
    pattern = re.compile(
        rf"^([ \t]*{re.escape(directive)}[ \t]+)([^;\s]+)([ \t]*;)",
        flags=re.MULTILINE,
    )
    matches = list(pattern.finditer(block))
    if len(matches) != 1:
        raise ValueError(f"expected exactly one {directive} in app.catsco.cc /v1/, found {len(matches)}")
    return pattern.sub(rf"\g<1>{timeout_seconds}s\g<3>", block, count=1)


def render(source: str, timeout_seconds: int) -> str:
    server_spans = block_spans(source, r"^[ \t]*server[ \t]*\{")
    candidates: list[tuple[int, int]] = []
    for start, end in server_spans:
        block = source[start:end]
        has_name = re.search(
            r"^[ \t]*server_name[ \t]+[^;]*\bapp\.catsco\.cc\b[^;]*;",
            block,
            flags=re.MULTILINE,
        )
        has_tls = re.search(r"^[ \t]*listen[ \t]+(?:\[::\]:)?443\b", block, flags=re.MULTILINE)
        if has_name and has_tls:
            candidates.append((start, end))
    if len(candidates) != 1:
        raise ValueError(f"expected exactly one TLS server for app.catsco.cc, found {len(candidates)}")

    server_start, server_end = candidates[0]
    server_block = source[server_start:server_end]
    locations = block_spans(
        server_block,
        r"^[ \t]*location[ \t]+(?:\^~[ \t]+)?/v1/[ \t]*\{",
    )
    if len(locations) != 1:
        raise ValueError(f"expected exactly one /v1/ location in app.catsco.cc TLS server, found {len(locations)}")

    location_start, location_end = locations[0]
    location_block = server_block[location_start:location_end]
    location_block = replace_directive(location_block, "proxy_read_timeout", timeout_seconds)
    location_block = replace_directive(location_block, "proxy_send_timeout", timeout_seconds)
    updated_server = server_block[:location_start] + location_block + server_block[location_end:]
    return source[:server_start] + updated_server + source[server_end:]


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--input", required=True, type=Path)
    parser.add_argument("--output", required=True, type=Path)
    parser.add_argument("--timeout", required=True, type=int)
    args = parser.parse_args()
    if args.timeout < 1:
        raise SystemExit("--timeout must be a positive number of seconds")

    source = args.input.read_text(encoding="utf-8")
    args.output.write_text(render(source, args.timeout), encoding="utf-8")


if __name__ == "__main__":
    main()
