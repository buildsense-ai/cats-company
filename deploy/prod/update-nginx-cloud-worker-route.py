#!/usr/bin/env python3
"""Add the long-running cloud-worker API route to a host Nginx vhost.

The migrated ``app.catsco.cn`` vhost initially only had the generic ``/api/``
route. Nginx's default upstream timeout is 60 seconds, which made a valid
worker update look like a failed operation. Keep the special route scoped to
the cloud-worker endpoints so ordinary API timeout policy is unchanged.
"""

from __future__ import annotations

import argparse
import re
from pathlib import Path


def matching_brace(text: str, open_index: int) -> int:
    depth = 0
    for index in range(open_index, len(text)):
        if text[index] == "{":
            depth += 1
        elif text[index] == "}":
            depth -= 1
            if depth == 0:
                return index + 1
    raise ValueError("unbalanced Nginx block")


def block_spans(text: str, pattern: str) -> list[tuple[int, int]]:
    spans: list[tuple[int, int]] = []
    for match in re.finditer(pattern, text, flags=re.MULTILINE):
        open_index = text.find("{", match.start(), match.end())
        if open_index < 0:
            raise ValueError("Nginx block has no opening brace")
        spans.append((match.start(), matching_brace(text, open_index)))
    return spans


def render(source: str, server_name: str) -> str:
    servers = block_spans(source, r"^[ \t]*server[ \t]*\{")
    candidates: list[tuple[int, int]] = []
    for start, end in servers:
        block = source[start:end]
        if not re.search(rf"^[ \t]*server_name[ \t]+[^;]*\b{re.escape(server_name)}\b[^;]*;", block, re.MULTILINE):
            continue
        if not re.search(r"^[ \t]*listen[ \t]+(?:\[::\]:)?443(?:\s|;)", block, re.MULTILINE):
            continue
        candidates.append((start, end))
    if len(candidates) != 1:
        raise ValueError(f"expected exactly one TLS server for {server_name}, found {len(candidates)}")

    server_start, server_end = candidates[0]
    server = source[server_start:server_end]
    # A single file can contain multiple vhosts (for example the app and API
    # CN entries). Only the selected TLS server matters; a route in a sibling
    # vhost must not make this target look configured.
    worker_locations = block_spans(
        server,
        r"^[ \t]*location[ \t]+\^~[ \t]+/api/cloud-workers[ \t]*\{",
    )
    if len(worker_locations) > 1:
        raise ValueError(f"expected at most one cloud-worker location for {server_name}, found {len(worker_locations)}")
    api_locations = block_spans(server, r"^[ \t]*location[ \t]+/api/[ \t]*\{")
    if len(api_locations) != 1:
        raise ValueError(f"expected exactly one generic /api/ location for {server_name}, found {len(api_locations)}")
    api_start, api_end = api_locations[0]
    api_block = server[api_start:api_end]
    proxy = re.search(r"^[ \t]*proxy_pass[ \t]+([^;]+);", api_block, re.MULTILINE)
    if not proxy:
        raise ValueError(f"generic /api/ location for {server_name} has no proxy_pass")

    indent = re.match(r"^[ \t]*", api_block).group(0)
    route = (
        f"{indent}location ^~ /api/cloud-workers {{\n"
        f"{indent}    proxy_pass {proxy.group(1)};\n"
        f"{indent}    proxy_http_version 1.1;\n"
        f"{indent}    proxy_set_header Host $host;\n"
        f"{indent}    proxy_set_header X-Real-IP $remote_addr;\n"
        f"{indent}    proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;\n"
        f"{indent}    proxy_set_header X-Forwarded-Proto $scheme;\n"
        f"{indent}    proxy_read_timeout 660s;\n"
        f"{indent}    proxy_send_timeout 660s;\n"
        f"{indent}    proxy_hide_header Cache-Control;\n"
        f"{indent}    proxy_cache off;\n"
        f"{indent}    proxy_no_cache 1;\n"
        f"{indent}    proxy_cache_bypass 1;\n"
        f'{indent}    add_header Cache-Control "no-store" always;\n'
        f"{indent}}}\n\n"
    )

    def has_directive(block: str, directive: str) -> bool:
        return re.search(rf"^[ \t]*{directive};", block, re.MULTILINE) is not None

    if worker_locations:
        worker_start, worker_end = worker_locations[0]
        worker_block = server[worker_start:worker_end]
        expected_proxy = proxy.group(1).strip()
        valid = (
            re.search(rf"^[ \t]*proxy_pass[ \t]+{re.escape(expected_proxy)};", worker_block, re.MULTILINE)
            and has_directive(worker_block, r"proxy_http_version[ \t]+1\.1")
            and has_directive(worker_block, r"proxy_read_timeout[ \t]+660s")
            and has_directive(worker_block, r"proxy_send_timeout[ \t]+660s")
            and has_directive(worker_block, r"proxy_hide_header[ \t]+Cache-Control")
            and has_directive(worker_block, r"proxy_cache[ \t]+off")
            and has_directive(worker_block, r"proxy_no_cache[ \t]+1")
            and has_directive(worker_block, r"proxy_cache_bypass[ \t]+1")
            and re.search(r'^[ \t]*add_header[ \t]+Cache-Control[ \t]+"no-store"[ \t]+always;', worker_block, re.MULTILINE)
        )
        if valid:
            return source
        # Replace a stale/incomplete route in place instead of silently
        # accepting a 60-second timeout or an upstream copied from another
        # vhost.
        updated_server = server[:worker_start] + route + server[worker_end:]
    else:
        updated_server = server[:api_start] + route + server[api_start:]
    return source[:server_start] + updated_server + source[server_end:]


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--input", required=True, type=Path)
    parser.add_argument("--output", required=True, type=Path)
    parser.add_argument("--server-name", required=True)
    args = parser.parse_args()
    args.output.write_text(
        render(args.input.read_text(encoding="utf-8"), args.server_name),
        encoding="utf-8",
    )


if __name__ == "__main__":
    main()
