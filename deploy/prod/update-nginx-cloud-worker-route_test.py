import importlib.util
import unittest
from pathlib import Path


SCRIPT = Path(__file__).with_name("update-nginx-cloud-worker-route.py")
spec = importlib.util.spec_from_file_location("cloud_worker_nginx", SCRIPT)
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


class CloudWorkerNginxRouteTest(unittest.TestCase):
    def test_inserts_route_only_in_tls_vhost(self):
        rendered = module.render(APP, "app.catsco.cn")
        self.assertEqual(rendered.count("location ^~ /api/cloud-workers"), 1)
        self.assertIn("proxy_read_timeout 660s;", rendered)
        self.assertIn("proxy_send_timeout 660s;", rendered)
        self.assertEqual(rendered.count("server_name app.catsco.cn;"), 2)

    def test_is_idempotent(self):
        rendered = module.render(APP, "app.catsco.cn")
        self.assertEqual(module.render(rendered, "app.catsco.cn"), rendered)

    def test_repairs_an_incomplete_existing_route(self):
        stale = APP.replace(
            "location /api/ {",
            "location ^~ /api/cloud-workers {\n"
            "        proxy_pass http://127.0.0.1:29999;\n"
            "        proxy_read_timeout 60s;\n"
            "    }\n"
            "    location /api/ {",
        )
        rendered = module.render(stale, "app.catsco.cn")
        self.assertEqual(rendered.count("location ^~ /api/cloud-workers"), 1)
        self.assertIn("proxy_pass http://127.0.0.1:28080;", rendered)
        self.assertIn("proxy_read_timeout 660s;", rendered)
        self.assertIn("proxy_send_timeout 660s;", rendered)

    def test_rejects_duplicate_cloud_worker_routes(self):
        duplicate = APP.replace(
            "location /api/ {",
            "location ^~ /api/cloud-workers {\n"
            "        proxy_pass http://127.0.0.1:28080;\n"
            "    }\n"
            "    location ^~ /api/cloud-workers {\n"
            "        proxy_pass http://127.0.0.1:28080;\n"
            "    }\n"
            "    location /api/ {",
        )
        with self.assertRaises(ValueError):
            module.render(duplicate, "app.catsco.cn")

    def test_sibling_vhost_route_does_not_skip_target(self):
        combined = APP + APP.replace("app.catsco.cn", "api.catsco.cn").replace(
            "location /api/ {", "location ^~ /api/cloud-workers {\n        proxy_pass http://127.0.0.1:28080;\n    }\n    location /api/ {",
        )
        rendered = module.render(combined, "app.catsco.cn")
        self.assertEqual(rendered.count("location ^~ /api/cloud-workers"), 2)

    def test_does_not_accept_missing_or_ambiguous_tls_vhost(self):
        with self.assertRaises(ValueError):
            module.render(APP.replace("server_name app.catsco.cn;", "server_name other.example;"), "app.catsco.cn")


if __name__ == "__main__":
    unittest.main()
