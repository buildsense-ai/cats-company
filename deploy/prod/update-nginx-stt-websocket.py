"""Render a host Nginx config with a dedicated streaming-STT WebSocket route."""

from __future__ import annotations

import argparse
import re
from pathlib import Path


STT_LOCATION = "/api/stt/realtime"


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


def leading_indent(text: str, index: int) -> str:
    line_start = text.rfind("\n", 0, index) + 1
    return re.match(r"[ \t]*", text[line_start:]).group(0)


def stt_location_block(indent: str) -> str:
    child_indent = f"{indent}    "
    directives = (
        "access_log off;",
        "proxy_pass http://127.0.0.1:28080;",
        "proxy_http_version 1.1;",
        "proxy_set_header Upgrade $http_upgrade;",
        'proxy_set_header Connection "upgrade";',
        "proxy_set_header Host $host;",
        "proxy_set_header X-Real-IP $remote_addr;",
        "proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;",
        "proxy_set_header X-Forwarded-Proto https;",
        "proxy_read_timeout 180s;",
        "proxy_send_timeout 180s;",
        "proxy_buffering off;",
        "proxy_hide_header Cache-Control;",
        "proxy_cache off;",
        "proxy_no_cache 1;",
        "proxy_cache_bypass 1;",
        'add_header Cache-Control "no-store" always;',
    )
    return "\n".join(
        (f"{indent}location {STT_LOCATION} {{", *(f"{child_indent}{directive}" for directive in directives), f"{indent}}}", ""),
    )


def app_tls_server_span(source: str) -> tuple[int, int]:
    candidates: list[tuple[int, int]] = []
    for start, end in block_spans(source, r"^[ \t]*server[ \t]*\{"):
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
    return candidates[0]


def location_spans(server_block: str, path: str) -> list[tuple[int, int]]:
    escaped_path = re.escape(path)
    return block_spans(
        server_block,
        rf"^[ \t]*location[ \t]+(?:(?:=|\^~)[ \t]+)?{escaped_path}[ \t]*\{{",
    )


def render(source: str) -> str:
    server_start, server_end = app_tls_server_span(source)
    server_block = source[server_start:server_end]
    stt_locations = location_spans(server_block, STT_LOCATION)

    if len(stt_locations) > 1:
        raise ValueError(f"expected at most one {STT_LOCATION} location, found {len(stt_locations)}")

    if stt_locations:
        location_start, location_end = stt_locations[0]
        replacement = stt_location_block(leading_indent(server_block, location_start))
        updated_server = server_block[:location_start] + replacement + server_block[location_end:]
    else:
        api_locations = location_spans(server_block, "/api/")
        if len(api_locations) != 1:
            raise ValueError(f"expected exactly one /api/ location in app.catsco.cc TLS server, found {len(api_locations)}")
        insert_at = api_locations[0][0]
        replacement = stt_location_block(leading_indent(server_block, insert_at))
        updated_server = server_block[:insert_at] + replacement + "\n" + server_block[insert_at:]

    return source[:server_start] + updated_server + source[server_end:]


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--input", required=True, type=Path)
    parser.add_argument("--output", required=True, type=Path)
    args = parser.parse_args()

    source = args.input.read_text(encoding="utf-8")
    args.output.write_text(render(source), encoding="utf-8")


if __name__ == "__main__":
    main()
