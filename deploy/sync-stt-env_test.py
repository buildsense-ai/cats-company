#!/usr/bin/env python3
"""Tests for synchronizing the streaming STT API key."""

from __future__ import annotations

import importlib.util
import io
import os
import stat
import sys
import tempfile
import unittest
from pathlib import Path


sys.dont_write_bytecode = True
SCRIPT_DIR = Path(__file__).resolve().parent
SYNC_PATH = SCRIPT_DIR / "sync-stt-env.py"
WORKFLOWS_DIR = SCRIPT_DIR.parent / ".github" / "workflows"
spec = importlib.util.spec_from_file_location("sync_stt_env", SYNC_PATH)
if spec is None or spec.loader is None:
    raise RuntimeError(f"failed to load {SYNC_PATH}")
sync = importlib.util.module_from_spec(spec)
spec.loader.exec_module(sync)


class SyncSttEnvTest(unittest.TestCase):
    def test_compose_and_examples_expose_every_documented_timeout(self) -> None:
        expected = (
            "CATSCO_STT_TICKET_TTL_SECONDS",
            "CATSCO_STT_IDLE_TIMEOUT_MS",
            "CATSCO_STT_CONNECT_TIMEOUT_MS",
            "CATSCO_STT_FINAL_TIMEOUT_MS",
        )
        for relative_path in (
            "deploy/prod/docker-compose.yml",
            "deploy/test/docker-compose.yml",
            "deploy/prod/env.prod.example",
            "deploy/test/env.test.example",
            "deploy/test/env.test.postgres.example",
        ):
            content = (SCRIPT_DIR.parent / relative_path).read_text(encoding="utf-8")
            for key in expected:
                self.assertIn(key, content, f"{key} missing from {relative_path}")

    def test_deploy_workflows_sync_one_api_key_after_bootstrap(self) -> None:
        for workflow_name, stack_root, environment in (
            ("deploy-prod.yml", "PROD_STACK_ROOT", "prod"),
            ("deploy-test.yml", "TEST_STACK_ROOT", "test"),
        ):
            workflow = (WORKFLOWS_DIR / workflow_name).read_text(encoding="utf-8")
            bootstrap = f'"bash ${{{stack_root}}}/compose/bootstrap-server.sh ${{{stack_root}}}"'
            command = f'"python3 ${{{stack_root}}}/compose/sync-stt-env.py ${{{stack_root}}}/env/{environment}.env"'
            self.assertIn('VOLCENGINE_STT_API_KEY: ${{ secrets.VOLCENGINE_STT_API_KEY }}', workflow)
            self.assertNotIn('secrets.VOLCENGINE_STT_APP_ID', workflow)
            self.assertIn(command, workflow)
            self.assertLess(workflow.index(bootstrap), workflow.index(command))

    def test_reads_exactly_one_nul_delimited_key(self) -> None:
        self.assertEqual(sync.read_value(io.BytesIO(b"api-key\0")), "api-key")
        with self.assertRaisesRegex(ValueError, "exactly one"):
            sync.read_value(io.BytesIO(b"api-key\0extra\0"))

    def test_render_enables_stt_and_removes_legacy_credentials(self) -> None:
        source = (
            "KEEP=value\nCATSCO_STT_ENABLED=0\nVOLCENGINE_STT_API_KEY=old\n"
            "VOLCENGINE_STT_APP_ID=legacy\nVOLCENGINE_STT_ACCESS_TOKEN=legacy\n"
            "VOLCENGINE_STT_CLUSTER=legacy\n"
        )
        rendered = sync.render(source, "new-key")
        self.assertEqual(rendered, "KEEP=value\nCATSCO_STT_ENABLED=1\nVOLCENGINE_STT_API_KEY=new-key\n")

    def test_empty_key_disables_stt_and_removes_stale_values(self) -> None:
        rendered = sync.render("CATSCO_STT_ENABLED=1\nVOLCENGINE_STT_API_KEY=old\n", "")
        self.assertEqual(rendered, "CATSCO_STT_ENABLED=0\n")

    def test_rejects_multiline_and_nul_before_writing(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            env_file = Path(directory) / "prod.env"
            env_file.write_text("KEEP=value\n", encoding="utf-8")
            for value in ("bad\nkey", "bad\0key"):
                with self.assertRaisesRegex(ValueError, "single-line"):
                    sync.update_file(env_file, value)
                self.assertEqual(env_file.read_text(encoding="utf-8"), "KEEP=value\n")

    def test_update_is_atomic_and_hardens_mode(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            env_file = Path(directory) / "test.env"
            env_file.write_text("CATSCO_STT_ENABLED=0\n", encoding="utf-8")
            os.chmod(env_file, 0o644)
            sync.update_file(env_file, "api-key")
            self.assertEqual(stat.S_IMODE(env_file.stat().st_mode), 0o600)
            self.assertIn("VOLCENGINE_STT_API_KEY=api-key\n", env_file.read_text(encoding="utf-8"))
            self.assertFalse(any(path.name.startswith(".") for path in env_file.parent.iterdir()))


if __name__ == "__main__":
    unittest.main()
