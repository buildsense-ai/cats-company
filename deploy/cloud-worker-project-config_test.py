from pathlib import Path
import unittest


ROOT = Path(__file__).resolve().parent.parent


class CloudWorkerProjectConfigTest(unittest.TestCase):
    def test_image_project_is_passed_to_server_in_both_compose_stacks(self):
        prod = (ROOT / "deploy/prod/docker-compose.yml").read_text(encoding="utf-8")
        self.assertIn(
            'CTYUN_IMAGE_PROJECT_ID: "${CTYUN_IMAGE_PROJECT_ID:?CTYUN_IMAGE_PROJECT_ID is required}"',
            prod,
        )
        test = (ROOT / "deploy/test/docker-compose.yml").read_text(encoding="utf-8")
        self.assertIn('CTYUN_IMAGE_PROJECT_ID: "${CTYUN_IMAGE_PROJECT_ID:-0}"', test)

    def test_production_example_declares_separate_image_project(self):
        content = (ROOT / "deploy/prod/env.prod.example").read_text(encoding="utf-8")
        self.assertIn("CTYUN_WORKER_PROJECT_ID=0", content)
        self.assertIn("CTYUN_IMAGE_PROJECT_ID=637cfbd046df4050a3544f687eb9fb55", content)


if __name__ == "__main__":
    unittest.main()
