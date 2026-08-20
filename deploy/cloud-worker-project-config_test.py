from pathlib import Path
import unittest


ROOT = Path(__file__).resolve().parent.parent


class CloudWorkerProjectConfigTest(unittest.TestCase):
    def test_image_project_is_passed_to_server_in_both_compose_stacks(self):
        for relative in ("deploy/prod/docker-compose.yml", "deploy/test/docker-compose.yml"):
            content = (ROOT / relative).read_text(encoding="utf-8")
            self.assertIn(
                'CTYUN_IMAGE_PROJECT_ID: "${CTYUN_IMAGE_PROJECT_ID:-0}"',
                content,
                relative,
            )

    def test_production_example_declares_separate_image_project(self):
        content = (ROOT / "deploy/prod/env.prod.example").read_text(encoding="utf-8")
        self.assertIn("CTYUN_WORKER_PROJECT_ID=0", content)
        self.assertIn("CTYUN_IMAGE_PROJECT_ID=0", content)


if __name__ == "__main__":
    unittest.main()
