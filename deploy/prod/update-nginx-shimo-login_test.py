#!/usr/bin/env python3

from __future__ import annotations

import importlib.util
import unittest
from pathlib import Path


SCRIPT_DIR = Path(__file__).resolve().parent
RENDERER_PATH = SCRIPT_DIR / "update-nginx-shimo-login.py"
APP_CONFIG_PATH = SCRIPT_DIR.parent / "tencent" / "nginx" / "catscompany-app.conf"

spec = importlib.util.spec_from_file_location("update_nginx_shimo_login", RENDERER_PATH)
if spec is None or spec.loader is None:
    raise RuntimeError(f"failed to load {RENDERER_PATH}")
renderer = importlib.util.module_from_spec(spec)
spec.loader.exec_module(renderer)


class UpdateNginxShimoLoginTest(unittest.TestCase):
    def setUp(self) -> None:
        self.source = APP_CONFIG_PATH.read_text(encoding="utf-8")

    def test_repository_config_is_idempotent(self) -> None:
        self.assertEqual(renderer.render(self.source), self.source)

    def test_repairs_missing_routes_without_replacing_other_routes(self) -> None:
        source = self.source.replace("    location /shimo-login/ {", "    location /shimo-login-old/ {", 1)
        source = source.replace("    location /connect/shimo/ {", "    location /connect/shimo-old/ {", 1)
        source = source.replace("    location /v0/channels {", "    location /local-host-only {\n        return 418;\n    }\n\n    location /v0/channels {", 1)
        rendered = renderer.render(source, 26071)
        self.assertIn("location /shimo-login/ {", rendered)
        self.assertIn("proxy_pass http://127.0.0.1:26071;", rendered)
        self.assertIn("location /connect/shimo/ {", rendered)
        self.assertIn("location /local-host-only {\n        return 418;", rendered)
        self.assertIn("location /shimo-login-old/ {", rendered)
        self.assertIn("location /connect/shimo-old/ {", rendered)

    def test_refuses_duplicate_route(self) -> None:
        duplicate = self.source.replace("    location /v0/channels {", "    location /shimo-login/ {\n        proxy_pass http://127.0.0.1:26070;\n    }\n\n    location /v0/channels {", 1)
        with self.assertRaisesRegex(ValueError, "expected at most one /shimo-login/"):
            renderer.render(duplicate)


if __name__ == "__main__":
    unittest.main()
