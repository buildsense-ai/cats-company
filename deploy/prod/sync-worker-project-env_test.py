#!/usr/bin/env python3

from __future__ import annotations

import importlib.util
import os
import stat
import sys
import tempfile
import unittest
from pathlib import Path


sys.dont_write_bytecode = True
SCRIPT_DIR = Path(__file__).resolve().parent
SYNC_PATH = SCRIPT_DIR / "sync-worker-project-env.py"
spec = importlib.util.spec_from_file_location("sync_worker_project_env", SYNC_PATH)
if spec is None or spec.loader is None:
    raise RuntimeError(f"failed to load {SYNC_PATH}")
sync = importlib.util.module_from_spec(spec)
spec.loader.exec_module(sync)


class SyncWorkerProjectEnvTest(unittest.TestCase):
    def test_replaces_duplicates_and_keeps_projects_independent(self) -> None:
        project = "a" * 32
        rendered = sync.render(
            "KEEP=value\nCTYUN_WORKER_PROJECT_ID=0\nCTYUN_WORKER_PROJECT_ID=old\n",
            project,
            project,
        )
        self.assertIn("KEEP=value\n", rendered)
        self.assertEqual(rendered.count("CTYUN_WORKER_PROJECT_ID="), 1)
        self.assertEqual(rendered.count("CTYUN_IMAGE_PROJECT_ID="), 1)
        self.assertIn(f"CTYUN_WORKER_PROJECT_ID={project}\n", rendered)
        self.assertIn(f"CTYUN_IMAGE_PROJECT_ID={project}\n", rendered)

    def test_rejects_worker_or_bot_ids(self) -> None:
        with self.assertRaisesRegex(ValueError, "32-character hexadecimal"):
            sync.render("", "12211", "0")

    def test_update_is_atomic_and_preserves_mode(self) -> None:
        project = "a" * 32
        with tempfile.TemporaryDirectory() as directory:
            env_file = Path(directory) / "prod.env"
            env_file.write_text("CTYUN_WORKER_PROJECT_ID=0\n", encoding="utf-8")
            os.chmod(env_file, 0o640)
            expected_mode = stat.S_IMODE(env_file.stat().st_mode)
            sync.update_file(env_file, project, project)
            rendered = env_file.read_text(encoding="utf-8")
            self.assertIn(f"CTYUN_IMAGE_PROJECT_ID={project}\n", rendered)
            self.assertEqual(stat.S_IMODE(env_file.stat().st_mode), expected_mode)


if __name__ == "__main__":
    unittest.main()
