#!/usr/bin/env python3
"""Add a streaming MCP route to a host Nginx vhost.

MCP's streamable HTTP transport keeps a GET on ``/api/mcp`` open as a
server-sent event stream. With Nginx's default ``proxy_buffering on`` behind
TLS the response is never released: measured on ``app.catsco.cc`` a raw
HTTP/1.1 request got no response headers at all within ten seconds, while the
same request through a plain-HTTP hop (and a throwaway TLS vhost with
``proxy_buffering off``) answered in 0.00s. The consequence was that
``notifications/tools/list_changed`` never reached clients, and every stream
was cut off by the default 60s ``proxy_read_timeout``.

Keep the route scoped to ``/api/mcp`` so ordinary ``/api/`` buffering and
timeout policy is unchanged.
"""

from __future__ import annotations

import argparse
import re
from pathlib import Path


ROUTE_PATH = "/api/mcp"
STREAM_TIMEOUT = "3600s"


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


def has_directive(block: str, directive: str) -> bool:
    return re.search(rf"^[ \t]*{directive};", block, re.MULTILINE) is not None


def render(source: str, server_name: str) -> str:
    servers = block_spans(source, r"^[ \t]*server[ \t]*\{")
    candidates: list[tuple[int, int]] = []
    for start, end in servers:
        block = source[start:end]
        if not re.search(
            rf"^[ \t]*server_name[ \t]+[^;]*\b{re.escape(server_name)}\b[^;]*;",
            block,
            re.MULTILINE,
        ):
            continue
        if not re.search(r"^[ \t]*listen[ \t]+(?:\[::\]:)?443(?:\s|;)", block, re.MULTILINE):
            continue
        candidates.append((start, end))
    if len(candidates) != 1:
        raise ValueError(f"expected exactly one TLS server for {server_name}, found {len(candidates)}")

    server_start, server_end = candidates[0]
    server = source[server_start:server_end]
    # A single file can contain multiple vhosts; only the selected TLS server
    # matters, so a route in a sibling vhost must not look configured.
    mcp_locations = block_spans(server, rf"^[ \t]*location[ \t]+\^~[ \t]+{re.escape(ROUTE_PATH)}[ \t]*\{{")
    if len(mcp_locations) > 1:
        raise ValueError(f"expected at most one {ROUTE_PATH} location for {server_name}, found {len(mcp_locations)}")
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
        f"{indent}# MCP streamable HTTP keeps a GET open as an SSE stream; the\n"
        f"{indent}# TLS hop must not buffer it and must outlive the default 60s.\n"
        f"{indent}location ^~ {ROUTE_PATH} {{\n"
        f"{indent}    proxy_pass {proxy.group(1)};\n"
        f"{indent}    proxy_http_version 1.1;\n"
        f'{indent}    proxy_set_header Connection "";\n'
        f"{indent}    proxy_set_header Host $host;\n"
        f"{indent}    proxy_set_header X-Real-IP $remote_addr;\n"
        f"{indent}    proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;\n"
        f"{indent}    proxy_set_header X-Forwarded-Proto $scheme;\n"
        f"{indent}    proxy_buffering off;\n"
        f"{indent}    proxy_cache off;\n"
        f"{indent}    proxy_no_cache 1;\n"
        f"{indent}    proxy_cache_bypass 1;\n"
        f"{indent}    proxy_read_timeout {STREAM_TIMEOUT};\n"
        f"{indent}    proxy_send_timeout {STREAM_TIMEOUT};\n"
        f"{indent}    proxy_hide_header Cache-Control;\n"
        f'{indent}    add_header Cache-Control "no-store" always;\n'
        f"{indent}}}\n\n"
    )

    if mcp_locations:
        mcp_start, mcp_end = mcp_locations[0]
        mcp_block = server[mcp_start:mcp_end]
        expected_proxy = proxy.group(1).strip()
        valid = (
            re.search(rf"^[ \t]*proxy_pass[ \t]+{re.escape(expected_proxy)};", mcp_block, re.MULTILINE)
            and has_directive(mcp_block, r"proxy_buffering[ \t]+off")
            and has_directive(mcp_block, r"proxy_http_version[ \t]+1\.1")
            and has_directive(mcp_block, rf"proxy_read_timeout[ \t]+{STREAM_TIMEOUT}")
            and has_directive(mcp_block, rf"proxy_send_timeout[ \t]+{STREAM_TIMEOUT}")
            and re.search(r'^[ \t]*proxy_set_header[ \t]+Connection[ \t]+"";', mcp_block, re.MULTILINE)
        )
        if valid:
            return source
        # Replace a stale/incomplete route in place instead of silently keeping
        # a buffered 60-second stream or an upstream copied from another vhost.
        updated_server = server[:mcp_start] + route + server[mcp_end:]
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
