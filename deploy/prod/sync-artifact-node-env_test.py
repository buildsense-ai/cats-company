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
SYNC_PATH = SCRIPT_DIR / "sync-artifact-node-env.py"
spec = importlib.util.spec_from_file_location("sync_artifact_node_env", SYNC_PATH)
if spec is None or spec.loader is None:
    raise RuntimeError(f"failed to load {SYNC_PATH}")
sync = importlib.util.module_from_spec(spec)
spec.loader.exec_module(sync)


class SyncArtifactNodeEnvTest(unittest.TestCase):
    def test_switching_from_inline_to_file_clears_inline(self) -> None:
        source = (
            "KEEP=value\n"
            'CATSCO_ARTIFACT_NODES_JSON={"nodes":{"old":{}}}\n'
            "CATSCO_ARTIFACT_NODES_FILE=\n"
        )
        rendered = sync.render(source, "", "/run/catsco-secrets/artifact-nodes.json")
        self.assertIn("KEEP=value\n", rendered)
        self.assertIn("CATSCO_ARTIFACT_NODES_JSON=\n", rendered)
        self.assertIn(
            "CATSCO_ARTIFACT_NODES_FILE=/run/catsco-secrets/artifact-nodes.json\n",
            rendered,
        )

    def test_switching_from_file_to_inline_clears_file(self) -> None:
        source = (
            "CATSCO_ARTIFACT_NODES_JSON=\n"
            "CATSCO_ARTIFACT_NODES_FILE=/run/catsco-secrets/old.json\n"
        )
        rendered = sync.render(source, '{"nodes":{"new":{}}}', "")
        self.assertIn('CATSCO_ARTIFACT_NODES_JSON={"nodes":{"new":{}}}\n', rendered)
        self.assertIn("CATSCO_ARTIFACT_NODES_FILE=\n", rendered)

    def test_empty_selection_disables_previous_file_mode(self) -> None:
        source = "CATSCO_ARTIFACT_NODES_FILE=/run/catsco-secrets/artifact-nodes.json\n"
        rendered = sync.render(source, "", "")
        self.assertIn("CATSCO_ARTIFACT_NODES_JSON=\n", rendered)
        self.assertIn("CATSCO_ARTIFACT_NODES_FILE=\n", rendered)

    def test_absent_environment_selection_preserves_manual_configuration(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            env_file = Path(directory) / "prod.env"
            original = "CATSCO_ARTIFACT_NODES_FILE=/run/catsco-secrets/manual.json\n"
            env_file.write_text(original, encoding="utf-8")
            self.assertFalse(sync.update_from_environment(env_file, {}))
            self.assertEqual(env_file.read_text(encoding="utf-8"), original)

    def test_conflicting_values_are_rejected_before_write(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            env_file = Path(directory) / "prod.env"
            original = "KEEP=value\n"
            env_file.write_text(original, encoding="utf-8")
            with self.assertRaisesRegex(ValueError, "configure only one"):
                sync.update_file(env_file, '{"nodes":{}}', "/run/catsco-secrets/nodes.json")
            self.assertEqual(env_file.read_text(encoding="utf-8"), original)

    def test_update_is_atomic_and_preserves_mode(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            env_file = Path(directory) / "prod.env"
            env_file.write_text(
                "CATSCO_ARTIFACT_NODES_JSON=old\n"
                "CATSCO_ARTIFACT_NODES_JSON=duplicate\n",
                encoding="utf-8",
            )
            os.chmod(env_file, 0o640)
            expected_mode = stat.S_IMODE(env_file.stat().st_mode)
            sync.update_file(env_file, "", "/run/catsco-secrets/nodes.json")
            rendered = env_file.read_text(encoding="utf-8")
            self.assertEqual(rendered.count("CATSCO_ARTIFACT_NODES_JSON="), 1)
            self.assertEqual(rendered.count("CATSCO_ARTIFACT_NODES_FILE="), 1)
            self.assertEqual(stat.S_IMODE(env_file.stat().st_mode), expected_mode)


if __name__ == "__main__":
    unittest.main()
