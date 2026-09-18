import re
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

    def service_block(self, stack: str, name: str) -> str:
        compose = self.compose_text(stack)
        match = re.search(rf"\n  {name}:\n(.*?)(?=\n  \S)", compose, re.DOTALL)
        self.assertIsNotNone(match, f"service {name} missing in deploy/{stack}/docker-compose.yml")
        return match.group(1)

    def test_server_health_check_flips_fast_after_a_deploy(self):
        # Scope to the server block: the same strings exist on other services,
        # so a whole-file assertion would not notice the server losing them.
        for stack in ("prod", "test"):
            block = self.service_block(stack, "server")
            self.assertIn("start_interval: 2s", block)
            self.assertIn("start_period: 30s", block)

    def test_web_does_not_wait_for_api_health(self):
        for stack in ("prod", "test"):
            # Scope the assertion to the web service block: another service
            # may legitimately gate on health without stretching the rollout.
            self.assertNotIn(
                "condition: service_healthy",
                self.service_block(stack, "web"),
                f"web must not gate on API health in deploy/{stack}/docker-compose.yml",
            )

    def test_slow_stopping_services_bound_their_grace_period(self):
        # Containers only start after every recreate finished creating, so one
        # slow stop holds back the whole `up -d` phase and stretches the 502
        # window. nginx exits in well under a second and the Dreamina worker
        # never exits on SIGTERM, so both get a short bounded grace.
        for stack in ("prod", "test"):
            self.assertIn(
                "stop_grace_period: 2s",
                self.service_block(stack, "web"),
                f"web must bound its stop grace in deploy/{stack}/docker-compose.yml",
            )
        self.assertIn(
            "stop_grace_period: 2s",
            self.service_block("prod", "dreamina-worker"),
        )

    def test_dreamina_health_check_flips_fast_after_a_deploy(self):
        block = self.service_block("prod", "dreamina-worker")
        self.assertIn("start_interval: 2s", block)
        self.assertIn("start_period: 30s", block)


if __name__ == "__main__":
    unittest.main()
