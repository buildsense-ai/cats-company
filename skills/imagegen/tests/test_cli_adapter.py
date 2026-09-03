from __future__ import annotations

import base64
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import threading
import unittest


SKILL_DIR = Path(__file__).resolve().parents[1]
ADAPTER = SKILL_DIR / "scripts" / "invoke_imagegen.py"
FIXTURE = SKILL_DIR / "tests" / "fixture.png"


class _ImageAPIHandler(BaseHTTPRequestHandler):
    calls: list[dict[str, object]] = []
    response_image = base64.b64encode(FIXTURE.read_bytes()).decode("ascii")

    def do_POST(self) -> None:  # noqa: N802
        length = int(self.headers.get("Content-Length", "0"))
        body = self.rfile.read(length)
        self.__class__.calls.append(
            {
                "path": self.path,
                "authorization": self.headers.get("Authorization"),
                "private_provider_header": self.headers.get(
                    "X-CatsCo-Image-Provider"
                ),
                "content_type": self.headers.get("Content-Type"),
                "body": body,
            }
        )
        payload = json.dumps(
            {"created": 1, "data": [{"b64_json": self.response_image}]}
        ).encode("utf-8")
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(payload)))
        self.end_headers()
        self.wfile.write(payload)

    def log_message(self, _format: str, *args: object) -> None:
        return


class CLIAdapterIntegrationTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls) -> None:
        _ImageAPIHandler.calls = []
        cls.server = ThreadingHTTPServer(("127.0.0.1", 0), _ImageAPIHandler)
        cls.thread = threading.Thread(target=cls.server.serve_forever, daemon=True)
        cls.thread.start()

    @classmethod
    def tearDownClass(cls) -> None:
        cls.server.shutdown()
        cls.thread.join(timeout=5)

    def invoke(self, request: dict[str, object]) -> tuple[dict[str, object], Path]:
        run_dir = Path(tempfile.mkdtemp(prefix="catsco-imagegen-test-"))
        request_path = run_dir / "request.json"
        request_path.write_text(json.dumps(request), encoding="utf-8")
        env = os.environ.copy()
        env.update(
            {
                "CATSCO_IMAGE_API_BASE": f"http://127.0.0.1:{self.server.server_port}/v1",
                "CATSCO_API_KEY": "bot-test-key",
            }
        )
        completed = subprocess.run(
            [
                sys.executable,
                str(ADAPTER),
                "--request",
                str(request_path),
                "--out-dir",
                str(run_dir),
            ],
            env=env,
            check=True,
            capture_output=True,
            text=True,
        )
        result = json.loads(completed.stdout.strip().splitlines()[-1])
        return result, run_dir

    def test_generate_and_edit_use_the_tool_contract(self) -> None:
        generated, generated_dir = self.invoke({"prompt": "a clean studio mug"})
        self.assertEqual(generated["mode"], "generate")
        self.assertTrue((generated_dir / "image.png").is_file())

        edited, edited_dir = self.invoke(
            {
                "prompt": "replace only the background",
                "referenced_image_paths": [str(FIXTURE)],
            }
        )
        self.assertEqual(edited["mode"], "edit")
        self.assertEqual(edited["reference_count"], 1)
        self.assertTrue((edited_dir / "image.png").is_file())

        self.assertEqual(len(_ImageAPIHandler.calls), 2)
        generation, edit = _ImageAPIHandler.calls
        self.assertEqual(generation["path"], "/v1/images/generations")
        self.assertEqual(edit["path"], "/v1/images/edits")
        self.assertEqual(generation["authorization"], "Bearer bot-test-key")
        self.assertEqual(edit["authorization"], "Bearer bot-test-key")
        self.assertIsNone(generation["private_provider_header"])
        self.assertIsNone(edit["private_provider_header"])
        self.assertTrue(str(generation["content_type"]).startswith("application/json"))
        self.assertTrue(str(edit["content_type"]).startswith("multipart/form-data"))
        generation_json = json.loads(generation["body"])
        self.assertEqual(generation_json["model"], "gpt-image-2")
        self.assertEqual(generation_json["size"], "auto")
        self.assertEqual(generation_json["quality"], "medium")
        self.assertIn(b"replace only the background", edit["body"])


if __name__ == "__main__":
    unittest.main()
