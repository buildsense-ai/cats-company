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
        self.assertIn("sync-shimo-env.py", workflow)
        self.assertIn("sync-shimo-worker-env.py", workflow)
        self.assertIn("SHIMO_WORKER_ENABLED", workflow)
        self.assertIn("Shimo Worker enablement requires actor, Worker, and session secrets", workflow)

    def test_test_deploy_refreshes_web_after_api_recreation(self):
        script = (ROOT / "deploy/test/remote-deploy.sh").read_text(encoding="utf-8")
        self.assertIn(
            'compose -f "$compose_file" --env-file "$env_file" up -d --force-recreate --no-deps web',
            script,
        )
        self.assertLess(script.index('wait_for_health "api" "$health_api"'), script.index("--force-recreate --no-deps web"))


if __name__ == "__main__":
    unittest.main()
