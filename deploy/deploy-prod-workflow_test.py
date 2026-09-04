from pathlib import Path
import unittest


ROOT = Path(__file__).resolve().parent.parent


class DeployProdWorkflowTest(unittest.TestCase):
    def test_prod_deploy_uses_noninteractive_sudo_for_root_owned_state(self):
        workflow = (ROOT / ".github/workflows/deploy-prod.yml").read_text(
            encoding="utf-8"
        )

        for script in ("sync-vapid-env.py", "sync-stt-env.py", "sync-worker-project-env.py"):
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

    def test_prod_deploy_repairs_migrated_cn_cloud_worker_timeout_route(self):
        workflow = (ROOT / ".github/workflows/deploy-prod.yml").read_text(
            encoding="utf-8"
        )
        self.assertIn("ensure-cloud-worker-nginx.sh", workflow)
        self.assertIn(
            "/etc/nginx/sites-available/catscompany-app-cn:app.catsco.cn",
            workflow,
        )
        self.assertIn(
            "/etc/nginx/sites-available/catscompany-api-cn:api.catsco.cn",
            workflow,
        )


if __name__ == "__main__":
    unittest.main()
