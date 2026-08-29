from pathlib import Path
import unittest


ROOT = Path(__file__).resolve().parent.parent


class DeployTestWorkflowTest(unittest.TestCase):
    def test_test_deploy_pulls_the_published_web_image(self):
        workflow = (ROOT / ".github/workflows/deploy-test.yml").read_text(
            encoding="utf-8"
        )

        self.assertIn("Build and push web image", workflow)
        self.assertIn("export REMOTE_WEB_IMAGE_MODE=pull", workflow)
        self.assertNotIn("export REMOTE_WEB_IMAGE_MODE=local", workflow)


if __name__ == "__main__":
    unittest.main()
