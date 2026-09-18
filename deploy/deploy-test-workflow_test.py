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

    def test_test_deploy_does_not_recreate_web_after_api_recreation(self):
        # The web nginx config resolves the API container per request through
        # the Docker DNS resolver, so a second web recreate would only add
        # another multi-second 502 window to every deploy.
        script = (ROOT / "deploy/test/remote-deploy.sh").read_text(encoding="utf-8")
        self.assertNotIn("--force-recreate", script)
        self.assertIn('wait_for_health "api" "$health_api"', script)
        self.assertIn('wait_for_health "web" "$health_web"', script)

    def test_deploy_hands_the_shimo_session_mount_to_the_worker_user(self):
        # The Shimo worker image runs as uid/gid 1000.  When the deploy only
        # ran `mkdir`, the container could read the session mount but never
        # write it, so every finished Shimo login failed to persist with a
        # silently swallowed EACCES.
        script = (ROOT / "deploy/test/remote-deploy.sh").read_text(encoding="utf-8")
        self.assertIn('shimo_state_dir="$root/data/shimo-sessions"', script)
        self.assertIn("chown -R \"$shimo_state_uid:$shimo_state_gid\" \"$shimo_state_dir\"", script)
        self.assertIn("chmod 700 \"$shimo_state_dir\"", script)
        self.assertIn("shimo_state_uid=1000", script)


if __name__ == "__main__":
    unittest.main()
