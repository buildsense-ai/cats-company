#!/usr/bin/env python3
"""Render the host app vhost with the Shimo login and grant routes.

The application vhost is maintained on the host and may contain local routes
that are not part of the repository.  This renderer changes only the two
Shimo entry points, preserving every other directive in the file.
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


def app_tls_server_span(source: str) -> tuple[int, int]:
    candidates: list[tuple[int, int]] = []
    for start, end in block_spans(source, r"^[ \t]*server[ \t]*\{"):
        block = source[start:end]
        has_name = re.search(
            r"^[ \t]*server_name[ \t]+[^;]*\bapp\.catsco\.cc\b[^;]*;",
            block,
            flags=re.MULTILINE,
        )
        has_tls = re.search(
            r"^[ \t]*listen[ \t]+(?:\[::\]:)?443(?:\s|;)",
            block,
            flags=re.MULTILINE,
        )
        if has_name and has_tls:
            candidates.append((start, end))
    if len(candidates) != 1:
        raise ValueError(
            f"expected exactly one TLS server for app.catsco.cc, found {len(candidates)}"
        )
    return candidates[0]


def location_spans(server: str, path: str) -> list[tuple[int, int]]:
    return block_spans(
        server,
        rf"^[ \t]*location[ \t]+(?:(?:=|\^~)[ \t]+)?{re.escape(path)}[ \t]*\{{",
    )


def proxy_pass(server: str) -> str:
    generic = location_spans(server, "/")
    if len(generic) != 1:
        raise ValueError(
            f"expected exactly one generic / location in app.catsco.cc TLS server, found {len(generic)}"
        )
    block = server[generic[0][0]:generic[0][1]]
    match = re.search(r"^[ \t]*proxy_pass[ \t]+([^;]+);", block, re.MULTILINE)
    if not match:
        raise ValueError("generic / location has no proxy_pass")
    return match.group(1).strip()


def route_block(indent: str, path: str, upstream: str) -> str:
    child = f"{indent}    "
    directives = [
        f"proxy_pass {upstream};",
        "proxy_http_version 1.1;",
        "proxy_set_header Host $host;",
        "proxy_set_header X-Real-IP $remote_addr;",
        "proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;",
        "proxy_set_header X-Forwarded-Proto https;",
        "proxy_read_timeout 120s;",
        "proxy_send_timeout 120s;",
        "proxy_hide_header Cache-Control;",
        "proxy_cache off;",
        "proxy_no_cache 1;",
        "proxy_cache_bypass 1;",
        'add_header Cache-Control "no-store" always;',
    ]
    return "\n".join(
        (f"{indent}location {path} {{", *(f"{child}{item}" for item in directives), f"{indent}}}")
    )


def render(source: str, worker_port: int = 26070) -> str:
    if worker_port < 1 or worker_port > 65535:
        raise ValueError("worker port must be between 1 and 65535")

    server_start, server_end = app_tls_server_span(source)
    server = source[server_start:server_end]
    app_upstream = proxy_pass(server)
    routes = {
        "/shimo-login/": f"http://127.0.0.1:{worker_port}",
        "/connect/shimo/": app_upstream,
    }

    updated = server
    # Replace existing routes from the end so offsets remain valid.  A route
    # that is already canonical still renders byte-identically.
    replacements: list[tuple[int, int, str]] = []
    for path, upstream in routes.items():
        matches = location_spans(server, path)
        if len(matches) > 1:
            raise ValueError(f"expected at most one {path} location, found {len(matches)}")
        if matches:
            start, end = matches[0]
            line_end = server.find("\n", start)
            if line_end < 0:
                line_end = len(server)
            indent = re.match(r"[ \t]*", server[start:line_end]).group(0)
            replacements.append((start, end, route_block(indent, path, upstream)))

    if replacements:
        for start, end, replacement in sorted(replacements, reverse=True):
            updated = updated[:start] + replacement + updated[end:]

    # Insert any missing route immediately before the generic / location.
    missing = [(path, upstream) for path, upstream in routes.items() if not location_spans(server, path)]
    if missing:
        generic = location_spans(updated, "/")
        if len(generic) != 1:
            raise ValueError("generic / location changed unexpectedly while rendering Shimo routes")
        insert_at = generic[0][0]
        line_end = updated.find("\n", insert_at)
        if line_end < 0:
            line_end = len(updated)
        indent = re.match(r"[ \t]*", updated[insert_at:line_end]).group(0)
        text = "".join(route_block(indent, path, upstream) + "\n\n" for path, upstream in missing)
        updated = updated[:insert_at] + text + updated[insert_at:]

    return source[:server_start] + updated + source[server_end:]


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--input", required=True, type=Path)
    parser.add_argument("--output", required=True, type=Path)
    parser.add_argument("--worker-port", required=True, type=int)
    args = parser.parse_args()
    args.output.write_text(
        render(args.input.read_text(encoding="utf-8"), args.worker_port),
        encoding="utf-8",
    )


if __name__ == "__main__":
    main()
