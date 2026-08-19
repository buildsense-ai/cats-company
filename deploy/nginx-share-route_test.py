#!/usr/bin/env python3
"""Ensure capability-share SPA fallbacks retain their privacy headers."""

from __future__ import annotations

import re
import unittest
from pathlib import Path


CONFIG_PATH = Path(__file__).resolve().parent / "nginx" / "nginx.conf"


def location_body(config: str, declaration: str) -> str:
    match = re.search(re.escape(declaration) + r"\s*\{", config)
    if match is None:
        raise AssertionError(f"missing {declaration}")
    depth = 1
    cursor = match.end()
    while cursor < len(config) and depth:
        if config[cursor] == "{":
            depth += 1
        elif config[cursor] == "}":
            depth -= 1
        cursor += 1
    if depth:
        raise AssertionError(f"unterminated {declaration}")
    return config[match.end():cursor - 1]


class ShareRouteNginxConfigTest(unittest.TestCase):
    def test_share_fallback_keeps_capability_shell_private_without_disabling_app_shell_cache(self) -> None:
        config = CONFIG_PATH.read_text(encoding="utf-8")
        share_root = location_body(config, "location = /share")
        share_location = location_body(config, "location ^~ /share/")
        share_shell = location_body(config, "location @conversation_share_shell")
        index_location = location_body(config, "location = /index.html")

        self.assertIn("try_files $uri $uri/ @conversation_share_shell;", share_location)
        for location in (share_root, share_location, share_shell):
            self.assertIn('add_header Cache-Control "no-store" always;', location)
            self.assertIn('add_header Referrer-Policy "no-referrer" always;', location)
        self.assertIn('add_header Cache-Control "no-cache" always;', index_location)
        self.assertNotIn('add_header Cache-Control "no-store" always;', index_location)


if __name__ == "__main__":
    unittest.main()
