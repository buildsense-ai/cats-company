import importlib.util
import unittest
from pathlib import Path


SCRIPT = Path(__file__).with_name("update-nginx-mcp-streaming.py")
spec = importlib.util.spec_from_file_location("mcp_streaming_nginx", SCRIPT)
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)


APP = """server {
    listen 80;
    server_name app.catsco.cn;
    return 301 https://app.catsco.cn$request_uri;
}

server {
    listen 443 ssl http2;
    server_name app.catsco.cn;
    location /api/ {
        proxy_pass http://127.0.0.1:28080;
        proxy_set_header Host $host;
    }
}
"""


def location_block(source: str, route: str) -> str:
    start = source.index(f"location ^~ {route}")
    return source[start : module.matching_brace(source, source.index("{", start))]


class McpStreamingNginxRouteTest(unittest.TestCase):
    def test_repo_configs_carry_the_streaming_route(self):
        # Every hop that serves /api/mcp must disable response buffering: with
        # the TLS vhost buffering on, a GET stream never released its headers.
        root = Path(__file__).resolve().parents[2]
        configs = [
            root / "deploy/nginx/nginx.conf",
            root / "deploy/tencent/nginx/catscompany-app.conf",
            root / "deploy/tencent/nginx/catscompany-app-cn.conf",
            root / "deploy/tencent/nginx/catscompany-api.conf",
        ]
        for config in configs:
            source = config.read_text(encoding="utf-8")
            self.assertEqual(source.count("location ^~ /api/mcp"), 1, str(config))
            block = location_block(source, "/api/mcp")
            self.assertIn("proxy_buffering off;", block, str(config))
            self.assertIn("proxy_http_version 1.1;", block, str(config))
            self.assertIn('proxy_set_header Connection "";', block, str(config))
            self.assertIn("proxy_read_timeout 3600s;", block, str(config))

    def test_inserts_route_only_in_tls_vhost(self):
        rendered = module.render(APP, "app.catsco.cn")
        self.assertEqual(rendered.count("location ^~ /api/mcp"), 1)
        self.assertIn("proxy_buffering off;", rendered)
        self.assertIn("proxy_read_timeout 3600s;", rendered)
        self.assertEqual(rendered.count("server_name app.catsco.cn;"), 2)
        # The route must precede the generic /api/ block so the prefix wins.
        self.assertLess(
            rendered.index("location ^~ /api/mcp"),
            rendered.index("location /api/"),
        )

    def test_is_idempotent(self):
        rendered = module.render(APP, "app.catsco.cn")
        self.assertEqual(module.render(rendered, "app.catsco.cn"), rendered)

    def test_repairs_a_buffered_existing_route(self):
        stale = APP.replace(
            "location /api/ {",
            "location ^~ /api/mcp {\n"
            "        proxy_pass http://127.0.0.1:28080;\n"
            "        proxy_read_timeout 60s;\n"
            "    }\n"
            "    location /api/ {",
        )
        rendered = module.render(stale, "app.catsco.cn")
        self.assertEqual(rendered.count("location ^~ /api/mcp"), 1)
        self.assertIn("proxy_buffering off;", rendered)
        self.assertIn("proxy_read_timeout 3600s;", rendered)
        self.assertNotIn("proxy_read_timeout 60s;", rendered)

    def test_rejects_duplicate_routes(self):
        duplicate = APP.replace(
            "location /api/ {",
            "location ^~ /api/mcp {\n"
            "        proxy_pass http://127.0.0.1:28080;\n"
            "    }\n"
            "    location ^~ /api/mcp {\n"
            "        proxy_pass http://127.0.0.1:28080;\n"
            "    }\n"
            "    location /api/ {",
        )
        with self.assertRaises(ValueError):
            module.render(duplicate, "app.catsco.cn")


if __name__ == "__main__":
    unittest.main()
