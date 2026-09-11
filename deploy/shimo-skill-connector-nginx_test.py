#!/usr/bin/env python3
"""Keep the Shimo connector routes from shadowing the hardened /v1/ proxy.

A longer `/v1/shimo/` prefix would win over `/v1/` and silently drop that
route's longer read/send timeouts and its client-forwarding-chain reset, so
these routes are asserted to stay on the existing prefix instead.
"""

from __future__ import annotations

import re
import shlex
import unittest
from pathlib import Path


ROOT = Path(__file__).resolve().parent.parent
CONTAINER_NGINX = ROOT / "deploy/nginx/nginx.conf"
API_NGINX = ROOT / "deploy/tencent/nginx/catscompany-api.conf"
APP_NGINX = ROOT / "deploy/tencent/nginx/catscompany-app.conf"

CONNECTOR_PREFIX = "/connect/shimo/"
MIN_LOGIN_TIMEOUT_SECONDS = 120


def location_block(config: str, prefix: str) -> str:
    pattern = re.compile(
        r"location[ \t]+(?:\^~[ \t]+)?" + re.escape(prefix) + r"[ \t]*\{"
    )
    match = pattern.search(config)
    if match is None:
        raise AssertionError(f"missing location {prefix}")
    depth = 1
    cursor = match.end()
    while cursor < len(config) and depth:
        if config[cursor] == "{":
            depth += 1
        elif config[cursor] == "}":
            depth -= 1
        cursor += 1
    if depth:
        raise AssertionError(f"unterminated location {prefix}")
    return config[match.end():cursor - 1]


def directives(location: str) -> dict[str, list[str]]:
    parsed: dict[str, list[str]] = {}
    for raw_line in location.splitlines():
        line = raw_line.split("#", 1)[0].strip()
        if not line.endswith(";"):
            continue
        parts = shlex.split(line[:-1])
        if len(parts) < 2:
            continue
        parsed.setdefault(parts[0], []).append(" ".join(parts[1:]))
    return parsed


def timeout_seconds(value: str) -> int:
    match = re.fullmatch(r"(\d+)(s|ms)?", value)
    if match is None:
        raise AssertionError(f"unsupported timeout value {value!r}")
    seconds = int(match.group(1))
    if match.group(2) == "ms":
        raise AssertionError(f"timeout {value!r} is finer than one second")
    return seconds


class ShimoConnectorNginxTest(unittest.TestCase):
    def test_no_config_shadows_the_v1_prefix_for_shimo(self) -> None:
        for path in (CONTAINER_NGINX, API_NGINX, APP_NGINX):
            config = path.read_text(encoding="utf-8")
            self.assertNotIn("location /v1/shimo/", config, str(path))

    def test_container_nginx_keeps_v1_timeouts_and_bounds_the_login_entry(self) -> None:
        config = CONTAINER_NGINX.read_text(encoding="utf-8")
        v1 = directives(location_block(config, "/v1/"))
        self.assertEqual(v1["proxy_read_timeout"], ["580s"])
        self.assertEqual(v1["proxy_send_timeout"], ["580s"])

        login = directives(location_block(config, CONNECTOR_PREFIX))
        self.assertGreaterEqual(
            timeout_seconds(login["proxy_read_timeout"][0]),
            MIN_LOGIN_TIMEOUT_SECONDS,
        )

    def test_api_nginx_keeps_the_hardened_v1_forwarding_boundary(self) -> None:
        config = API_NGINX.read_text(encoding="utf-8")
        v1 = directives(location_block(config, "/v1/"))
        self.assertEqual(v1["proxy_set_header"], [
            "Host $host",
            "X-Real-IP $remote_addr",
            "X-Forwarded-For $remote_addr",
            "X-Forwarded-Proto https",
        ])
        self.assertEqual(v1["proxy_read_timeout"], ["580s"])
        self.assertEqual(v1["proxy_send_timeout"], ["580s"])

    def test_app_nginx_keeps_the_hardened_v1_boundary_and_bounds_the_login_entry(self) -> None:
        config = APP_NGINX.read_text(encoding="utf-8")
        v1 = directives(location_block(config, "/v1/"))
        self.assertEqual(v1["proxy_set_header"], [
            "Host $host",
            "X-Real-IP $remote_addr",
            "X-Forwarded-For $remote_addr",
            "X-Forwarded-Proto https",
        ])
        self.assertEqual(v1["proxy_read_timeout"], ["580s"])

        login = directives(location_block(config, CONNECTOR_PREFIX))
        self.assertGreaterEqual(
            timeout_seconds(login["proxy_read_timeout"][0]),
            MIN_LOGIN_TIMEOUT_SECONDS,
        )


if __name__ == "__main__":
    unittest.main()
