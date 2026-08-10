#!/usr/bin/env python3
"""Keep the duplicated STT WebSocket proxy routes behaviorally aligned."""

from __future__ import annotations

import re
import unittest
from pathlib import Path


ROOT = Path(__file__).resolve().parent.parent
CONFIGS = (
    ROOT / "deploy/nginx/nginx.conf",
    ROOT / "deploy/tencent/nginx/catscompany-api.conf",
    ROOT / "deploy/tencent/nginx/catscompany-app.conf",
)
COMMON_DIRECTIVES = (
    "access_log off;",
    "proxy_http_version 1.1;",
    "proxy_set_header Upgrade $http_upgrade;",
    'proxy_set_header Connection "upgrade";',
    "proxy_set_header Host $host;",
    "proxy_set_header X-Real-IP $remote_addr;",
    "proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;",
    "proxy_read_timeout 120s;",
    "proxy_send_timeout 120s;",
    "proxy_buffering off;",
    "proxy_hide_header Cache-Control;",
    "proxy_cache off;",
    "proxy_no_cache 1;",
    "proxy_cache_bypass 1;",
    'add_header Cache-Control "no-store" always;',
)


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


class SttNginxConfigTest(unittest.TestCase):
    def test_common_proxy_directives_match_every_deployment(self) -> None:
        for path in CONFIGS:
            location = stt_location(path.read_text(encoding="utf-8"))
            for directive in COMMON_DIRECTIVES:
                self.assertIn(directive, location, f"{directive} missing from {path}")


if __name__ == "__main__":
    unittest.main()
