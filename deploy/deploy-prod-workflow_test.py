from pathlib import Path
import unittest


ROOT = Path(__file__).resolve().parent.parent


class DeployProdWorkflowTest(unittest.TestCase):
    def test_prod_deploy_uses_noninteractive_sudo_for_root_owned_state(self):
        workflow = (ROOT / ".github/workflows/deploy-prod.yml").read_text(
            encoding="utf-8"
        )

        for script in ("sync-vapid-env.py", "sync-stt-env.py", "sync-worker-project-env.py", "sync-shimo-env.py", "sync-shimo-worker-env.py"):
            self.assertIn(
                f'"sudo -n python3 ${{PROD_STACK_ROOT}}/compose/{script}',
                workflow,
            )

        self.assertIn(
            'sudo -n -E "$root/compose/remote-build-source.sh" "$root" "$revision" "$owner"',
            workflow,
        )
        self.assertIn(
            'sudo -n -E "$root/compose/remote-deploy.sh" "$root" "$revision"',
            workflow,
        )
        self.assertIn(
            'sudo -n -E bash ${PROD_STACK_ROOT}/compose/remote-rollback.sh',
            workflow,
        )
        self.assertIn(
            'sudo -n bash ${PROD_STACK_ROOT}/compose/remote-status.sh',
            workflow,
        )
        self.assertIn("SHIMO_WORKER_ENABLED", workflow)
        self.assertIn("Shimo Worker enablement requires actor, Worker, and session secrets", workflow)

    def test_prod_deploy_installs_and_starts_worker_release_prune_timer(self):
        workflow = (ROOT / ".github/workflows/deploy-prod.yml").read_text(
            encoding="utf-8"
        )
        self.assertIn(
            'deploy/prod/systemd/catsco-worker-release-prune.service', workflow
        )
        self.assertIn(
            'deploy/prod/systemd/catsco-worker-release-prune.timer', workflow
        )
        self.assertIn(
            'systemctl enable --now catsco-worker-release-prune.timer', workflow
        )

    def test_prod_deploy_repairs_cloud_worker_timeout_route_on_both_domains(self):
        workflow = (ROOT / ".github/workflows/deploy-prod.yml").read_text(
            encoding="utf-8"
        )
        self.assertIn("ensure-cloud-worker-nginx.sh", workflow)
        self.assertIn("ensure-shimo-login-nginx.sh", workflow)
        self.assertIn("update-nginx-shimo-login.py", workflow)
        self.assertIn(
            'sudo -n "$root/compose/ensure-shimo-login-nginx.sh" /etc/nginx/sites-available/catscompany-app "$root/env/prod.env"',
            workflow,
        )
        for route in (
            "/etc/nginx/sites-available/catscompany-app:app.catsco.cc",
            "/etc/nginx/sites-available/catscompany-api:api.catsco.cc",
            "/etc/nginx/sites-available/catscompany-app-cn:app.catsco.cn",
            "/etc/nginx/sites-available/catscompany-api-cn:api.catsco.cn",
        ):
            self.assertIn(route, workflow)

    def test_prod_deploy_refreshes_web_after_api_recreation(self):
        script = (ROOT / "deploy/prod/remote-deploy.sh").read_text(encoding="utf-8")
        self.assertIn(
            'compose -f "$compose_file" --env-file "$env_file" up -d --force-recreate --no-deps web',
            script,
        )
        self.assertLess(script.index('wait_for_health "api" "$health_api"'), script.index("--force-recreate --no-deps web"))


if __name__ == "__main__":
    unittest.main()
