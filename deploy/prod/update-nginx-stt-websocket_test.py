"""Regression tests for the host-Nginx STT WebSocket location renderer."""

from __future__ import annotations

import importlib.util
import sys
import unittest
from pathlib import Path


SCRIPT_DIR = Path(__file__).resolve().parent
RENDERER_PATH = SCRIPT_DIR / "update-nginx-stt-websocket.py"
sys.dont_write_bytecode = True


def load_renderer():
    spec = importlib.util.spec_from_file_location("update_nginx_stt_websocket", RENDERER_PATH)
    assert spec and spec.loader
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


class UpdateNginxSTTWebSocketTest(unittest.TestCase):
    def setUp(self) -> None:
        self.renderer = load_renderer()

    def test_adds_a_dedicated_upgrade_location_before_the_generic_api_proxy(self) -> None:
        source = """server {
    listen 443 ssl http2;
    server_name app.catsco.cc;

    location /api/ {
        proxy_pass http://127.0.0.1:28080;
    }

    location /uploads/ {
        proxy_pass http://127.0.0.1:28080;
    }
}
"""

        rendered = self.renderer.render(source)

        stt_location = """    location /api/stt/realtime {
        access_log off;
        proxy_pass http://127.0.0.1:28080;
        proxy_http_version 1.1;
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection \"upgrade\";
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto https;
        proxy_read_timeout 180s;
        proxy_send_timeout 180s;
        proxy_buffering off;
        proxy_hide_header Cache-Control;
        proxy_cache off;
        proxy_no_cache 1;
        proxy_cache_bypass 1;
        add_header Cache-Control \"no-store\" always;
    }

"""
        self.assertIn(stt_location, rendered)
        self.assertLess(
            rendered.index("location /api/stt/realtime"),
            rendered.index("location /api/ {"),
        )
        self.assertIn("location /uploads/", rendered)

    def test_replaces_an_outdated_stt_location_without_touching_other_locations(self) -> None:
        source = """server {
    listen 443 ssl http2;
    server_name app.catsco.cc;

    location /api/stt/realtime {
        proxy_pass http://127.0.0.1:28080;
    }

    location /api/ {
        proxy_pass http://127.0.0.1:28080;
    }
}
"""

        rendered = self.renderer.render(source)

        self.assertEqual(rendered.count("location /api/stt/realtime"), 1)
        self.assertIn('proxy_set_header Connection "upgrade";', rendered)
        self.assertIn("proxy_buffering off;", rendered)
        self.assertIn("location /api/ {\n        proxy_pass", rendered)


if __name__ == "__main__":
    unittest.main()
