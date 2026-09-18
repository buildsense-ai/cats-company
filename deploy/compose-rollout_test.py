import unittest
from pathlib import Path


ROOT = Path(__file__).resolve().parent.parent


class ComposeRolloutTest(unittest.TestCase):
    """Guards the rolling-restart contract that keeps deploys to a few seconds.

    The web container serves every app.catsco.cc request, so anything that
    delays its restart (or makes it wait for the API) shows up as a site-wide
    502 outage.
    """

    def compose_text(self, stack: str) -> str:
        return (ROOT / f"deploy/{stack}/docker-compose.yml").read_text(encoding="utf-8")

    def test_server_health_check_flips_fast_after_a_deploy(self):
        for stack in ("prod", "test"):
            compose = self.compose_text(stack)
            self.assertIn("start_interval: 2s", compose)
            self.assertIn("start_period: 30s", compose)

    def test_web_does_not_wait_for_api_health(self):
        for stack in ("prod", "test"):
            compose = self.compose_text(stack)
            self.assertNotIn("condition: service_healthy", compose)


if __name__ == "__main__":
    unittest.main()
