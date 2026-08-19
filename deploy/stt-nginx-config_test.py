#!/usr/bin/env python3
"""Keep the duplicated STT WebSocket proxy routes behaviorally aligned."""

from __future__ import annotations

import re
import shlex
import unittest
from pathlib import Path


ROOT = Path(__file__).resolve().parent.parent
CONFIGS = (
    ROOT / "deploy/nginx/nginx.conf",
    ROOT / "deploy/tencent/nginx/catscompany-api.conf",
    ROOT / "deploy/tencent/nginx/catscompany-app.conf",
)
COMMON_DIRECTIVES = {
    "access_log": "off",
    "proxy_http_version": "1.1",
    "proxy_set_header Upgrade": "$http_upgrade",
    "proxy_set_header Connection": "upgrade",
    "proxy_set_header Host": "$host",
    "proxy_set_header X-Real-IP": "$remote_addr",
    "proxy_set_header X-Forwarded-For": "$proxy_add_x_forwarded_for",
    "proxy_read_timeout": "180s",
    "proxy_send_timeout": "180s",
    "proxy_buffering": "off",
    "proxy_hide_header": "Cache-Control",
    "proxy_cache": "off",
    "proxy_no_cache": "1",
    "proxy_cache_bypass": "1",
    "add_header Cache-Control": "no-store always",
}


def stt_location(config: str) -> str:
    match = re.search(r"location /api/stt/realtime\s*\{", config)
    if match is None:
        raise AssertionError("missing /api/stt/realtime location")
    depth = 1
    cursor = match.end()
    while cursor < len(config) and depth:
        if config[cursor] == "{":
            depth += 1
        elif config[cursor] == "}":
            depth -= 1
        cursor += 1
    if depth:
        raise AssertionError("unterminated /api/stt/realtime location")
    return config[match.end():cursor - 1]


def parsed_directives(location: str) -> dict[str, list[str]]:
    directives: dict[str, list[str]] = {}
    for raw_line in location.splitlines():
        line = raw_line.split("#", 1)[0].strip()
        if not line.endswith(";"):
            continue
        parts = shlex.split(line[:-1])
        if not parts:
            continue
        key_size = 2 if parts[0] in {"proxy_set_header", "add_header"} else 1
        key = " ".join(parts[:key_size])
        value = " ".join(parts[key_size:])
        directives.setdefault(key, []).append(value)
    return directives


class SttNginxConfigTest(unittest.TestCase):
    def test_common_proxy_directives_match_every_deployment(self) -> None:
        for path in CONFIGS:
            location = stt_location(path.read_text(encoding="utf-8"))
            directives = parsed_directives(location)
            for key, expected in COMMON_DIRECTIVES.items():
                self.assertEqual(directives.get(key), [expected], f"{key} differs in {path}")

    def test_conflicting_duplicate_directives_are_rejected(self) -> None:
        directives = parsed_directives('''
            proxy_set_header Connection "upgrade";
            proxy_set_header Connection "close";
        ''')
        self.assertNotEqual(directives["proxy_set_header Connection"], ["upgrade"])


if __name__ == "__main__":
    unittest.main()
