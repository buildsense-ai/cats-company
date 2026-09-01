from pathlib import Path
import unittest


ROOT = Path(__file__).resolve().parent.parent


class DeployTestWorkflowTest(unittest.TestCase):
    def test_test_deploy_builds_images_on_the_remote_host(self):
        workflow = (ROOT / ".github/workflows/deploy-test.yml").read_text(
            encoding="utf-8"
        )

        self.assertIn("export REMOTE_WEB_IMAGE_MODE=local", workflow)
        self.assertIn("export REMOTE_WEBSITE_IMAGE_MODE=local", workflow)
        self.assertIn("rsync --archive --compress --checksum --delete", workflow)
        self.assertIn("prepare '${DEPLOY_CACHE_ROOT}'", workflow)
        self.assertIn("pack '${DEPLOY_CACHE_ROOT}'", workflow)
        self.assertNotIn("Build and push web image", workflow)
        self.assertNotIn("Build and push public website image", workflow)


if __name__ == "__main__":
    unittest.main()
