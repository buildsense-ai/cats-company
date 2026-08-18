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
    def test_share_fallback_and_final_shell_are_not_cacheable_or_referrable(self) -> None:
        config = CONFIG_PATH.read_text(encoding="utf-8")
        share_location = location_body(config, "location ^~ /share/")
        index_location = location_body(config, "location = /index.html")

        self.assertIn("try_files $uri $uri/ /index.html;", share_location)
        for location in (share_location, index_location):
            self.assertIn('add_header Cache-Control "no-store" always;', location)
            self.assertIn('add_header Referrer-Policy "no-referrer" always;', location)


if __name__ == "__main__":
    unittest.main()
