#!/usr/bin/env python3
"""Tests for synchronizing Shimo deployment settings."""

from __future__ import annotations

import importlib.util
import io
import stat
import sys
import tempfile
import unittest
from pathlib import Path


sys.dont_write_bytecode = True
SCRIPT_DIR = Path(__file__).resolve().parent
WORKFLOWS_DIR = SCRIPT_DIR.parent / ".github" / "workflows"
spec = importlib.util.spec_from_file_location("sync_shimo_env", SCRIPT_DIR / "sync-shimo-env.py")
if spec is None or spec.loader is None:
    raise RuntimeError("failed to load sync-shimo-env.py")
sync = importlib.util.module_from_spec(spec)
spec.loader.exec_module(sync)


class SyncShimoEnvTest(unittest.TestCase):
    def test_deploy_workflows_upload_and_sync_shimo_settings(self) -> None:
        for workflow_name, stack_root, environment in (
            ("deploy-prod.yml", "PROD_STACK_ROOT", "prod"),
            ("deploy-test.yml", "TEST_STACK_ROOT", "test"),
        ):
            with self.subTest(workflow=workflow_name):
                workflow = (WORKFLOWS_DIR / workflow_name).read_text(encoding="utf-8")
                self.assertIn("cp deploy/sync-shimo-env.py", workflow)
                self.assertIn("CATSCO_SHIMO_ACTOR_SECRET", workflow)
                sync_command = (
                    f"sync-shimo-env.py ${{{stack_root}}}/env/{environment}.env"
                )
                self.assertIn(sync_command, workflow)

    def test_reads_and_validates_values(self) -> None:
        secret = "s" * 32
        self.assertEqual(
            sync.read_values(io.BytesIO(f"{secret}\0arrowhaken/shimo-reader\0".encode())),
            (secret, "arrowhaken/shimo-reader"),
        )
        with self.assertRaisesRegex(ValueError, "at least 32"):
            sync.read_values(io.BytesIO(b"short\0arrowhaken/shimo-reader\0"))
        with self.assertRaisesRegex(ValueError, "owner/name"):
            sync.read_values(io.BytesIO(b"\0bad-id\0"))

    def test_empty_secret_preserves_existing_secret_but_updates_skill_id(self) -> None:
        source = (
            "KEEP=value\n"
            "CATSCO_SHIMO_ACTOR_SECRET=existing-secret\n"
            "CATSCO_SHIMO_SKILL_ID=catsco/shimo-reader\n"
        )
        rendered = sync.render(source, "", "arrowhaken/shimo-reader")
        self.assertIn("CATSCO_SHIMO_ACTOR_SECRET=existing-secret\n", rendered)
        self.assertIn("CATSCO_SHIMO_SKILL_ID=arrowhaken/shimo-reader\n", rendered)
        self.assertNotIn("catsco/shimo-reader", rendered)

    def test_update_is_owner_only_and_replaces_duplicates(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            env_file = Path(directory) / "prod.env"
            env_file.write_text(
                "CATSCO_SHIMO_SKILL_ID=catsco/shimo-reader\n"
                "CATSCO_SHIMO_SKILL_ID=duplicate\n",
                encoding="utf-8",
            )
            sync.update_file(env_file, "x" * 32, "arrowhaken/shimo-reader")
            self.assertEqual(env_file.read_text(encoding="utf-8").count("CATSCO_SHIMO_SKILL_ID="), 1)
            if sys.platform != "win32":
                self.assertEqual(stat.S_IMODE(env_file.stat().st_mode), 0o600)


if __name__ == "__main__":
    unittest.main()
