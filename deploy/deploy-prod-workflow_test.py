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


if __name__ == "__main__":
    unittest.main()
