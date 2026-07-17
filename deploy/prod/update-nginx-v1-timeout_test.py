#!/usr/bin/env python3

from __future__ import annotations

import importlib.util
import sys
import unittest
from pathlib import Path


sys.dont_write_bytecode = True


SCRIPT_DIR = Path(__file__).resolve().parent
RENDERER_PATH = SCRIPT_DIR / "update-nginx-v1-timeout.py"
APP_CONFIG_PATH = SCRIPT_DIR.parent / "tencent" / "nginx" / "catscompany-app.conf"

spec = importlib.util.spec_from_file_location("update_nginx_v1_timeout", RENDERER_PATH)
if spec is None or spec.loader is None:
    raise RuntimeError(f"failed to load {RENDERER_PATH}")
renderer = importlib.util.module_from_spec(spec)
spec.loader.exec_module(renderer)


class UpdateNginxV1TimeoutTest(unittest.TestCase):
    def setUp(self) -> None:
        self.source = APP_CONFIG_PATH.read_text(encoding="utf-8")

    def test_changes_only_the_two_v1_timeout_directives(self) -> None:
        rendered = renderer.render(self.source, 581)
        expected = self.source.replace("proxy_read_timeout 580s;", "proxy_read_timeout 581s;", 1)
        expected = expected.replace("proxy_send_timeout 580s;", "proxy_send_timeout 581s;", 1)
        self.assertEqual(rendered, expected)

    def test_preserves_unrelated_host_only_location(self) -> None:
        marker = "    location /v1/ {"
        host_only = "    location /artifacts/ {\n        alias /srv/catsco-artifacts/;\n    }\n\n"
        source = self.source.replace(marker, host_only + marker, 1)
        rendered = renderer.render(source, 581)
        self.assertIn(host_only, rendered)

    def test_refuses_an_ambiguous_v1_location(self) -> None:
        location = "    location /v1/ {\n        proxy_read_timeout 580s;\n        proxy_send_timeout 580s;\n    }\n\n"
        source = self.source.replace("    location /api/ {", location + "    location /api/ {", 1)
        with self.assertRaisesRegex(ValueError, "exactly one /v1/ location"):
            renderer.render(source, 581)


if __name__ == "__main__":
    unittest.main()
