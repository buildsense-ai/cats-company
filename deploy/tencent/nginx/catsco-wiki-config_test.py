import pathlib
import unittest

ROOT = pathlib.Path(__file__).parent

class WikiNginxConfigTest(unittest.TestCase):
    def test_wiki_proxy_preserves_websocket_upgrade(self):
        text = (ROOT / "catsco-wiki.conf").read_text(encoding="utf-8")
        self.assertIn("server_name wiki.catsco.cn wiki.catsco.cc;", text)
        self.assertIn("proxy_pass http://127.0.0.1:28080;", text)
        self.assertIn("proxy_set_header Upgrade $http_upgrade;", text)
        self.assertIn('proxy_set_header Connection "upgrade";', text)
        self.assertIn("proxy_buffering off;", text)

if __name__ == "__main__":
    unittest.main()
