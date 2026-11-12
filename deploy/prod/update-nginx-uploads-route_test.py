import importlib.util
import unittest
from pathlib import Path


MODULE_PATH = Path(__file__).with_name("update-nginx-uploads-route.py")
SPEC = importlib.util.spec_from_file_location("uploads_route", MODULE_PATH)
assert SPEC is not None and SPEC.loader is not None
MODULE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(MODULE)


class UploadRouteMigrationTests(unittest.TestCase):
    def test_replaces_one_indented_location_and_ignores_braces_in_text(self):
        source = (
            "server {\n"
            "    location /uploads/ {\n"
            "        # } is only a comment\n"
            "        add_header X-Example '{not a block}';\n"
            "        proxy_pass http://100.86.2.1/catscompany/uploads/;\n"
            "    }\n"
            "    location @uploads_fallback { proxy_pass http://127.0.0.1:28080; }\n"
            "    location / { proxy_pass http://127.0.0.1:28080; }\n"
            "}\n"
        )
        result, changed = MODULE.migrate_text(source)
        self.assertTrue(changed)
        self.assertIn("    include /etc/nginx/snippets/uploads-zos.conf;", result)
        self.assertNotIn("location /uploads/", result)
        self.assertIn("location / { proxy_pass", result)
        self.assertNotIn("location @uploads_fallback", result)

    def test_managed_include_is_idempotent(self):
        source = "server {\n    include /etc/nginx/snippets/uploads-zos.conf;\n}\n"
        result, changed = MODULE.migrate_text(source)
        self.assertFalse(changed)
        self.assertEqual(source, result)

    def test_ambiguous_or_missing_routes_are_rejected(self):
        for source in (
            "server { location / { } }\n",
            "server { location /uploads/ { } location /uploads/ { } }\n",
            "server { include /etc/nginx/snippets/uploads-zos.conf; location /uploads/ { } }\n",
        ):
            with self.subTest(source=source), self.assertRaises(ValueError):
                MODULE.migrate_text(source)


if __name__ == "__main__":
    unittest.main()
