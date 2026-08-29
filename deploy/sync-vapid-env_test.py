#!/usr/bin/env python3
"""Tests for synchronizing Web Push VAPID and relay variables into deploy env files."""

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
SYNC_PATH = SCRIPT_DIR / "sync-vapid-env.py"
WORKFLOWS_DIR = SCRIPT_DIR.parent / ".github" / "workflows"
spec = importlib.util.spec_from_file_location("sync_vapid_env", SYNC_PATH)
if spec is None or spec.loader is None:
    raise RuntimeError(f"failed to load {SYNC_PATH}")
sync = importlib.util.module_from_spec(spec)
spec.loader.exec_module(sync)


def payload(
    public_key: str,
    private_key: str,
    subject: str,
    relay_url: str = "",
    relay_token: str = "",
) -> bytes:
    return b"\0".join(
        value.encode("utf-8")
        for value in (public_key, private_key, subject, relay_url, relay_token)
    ) + b"\0"


class SyncVapidEnvTest(unittest.TestCase):
    def test_deploy_workflows_bootstrap_env_before_syncing_web_push_secrets(self) -> None:
        for workflow_name, stack_root, environment in (
            ("deploy-prod.yml", "PROD_STACK_ROOT", "prod"),
            ("deploy-test.yml", "TEST_STACK_ROOT", "test"),
        ):
            with self.subTest(workflow=workflow_name):
                workflow = (WORKFLOWS_DIR / workflow_name).read_text(encoding="utf-8")
                bootstrap_candidates = (
                    f'"bash ${{{stack_root}}}/compose/bootstrap-server.sh ${{{stack_root}}}"',
                    f'"sudo -n bash ${{{stack_root}}}/compose/bootstrap-server.sh ${{{stack_root}}}"',
                )
                sync_candidates = (
                    f'"python3 ${{{stack_root}}}/compose/sync-vapid-env.py ${{{stack_root}}}/env/{environment}.env"',
                    f'"sudo -n python3 ${{{stack_root}}}/compose/sync-vapid-env.py ${{{stack_root}}}/env/{environment}.env"',
                )
                bootstrap = next(
                    (candidate for candidate in bootstrap_candidates if candidate in workflow),
                    None,
                )
                sync = next(
                    (candidate for candidate in sync_candidates if candidate in workflow),
                    None,
                )

                self.assertIsNotNone(bootstrap)
                self.assertIsNotNone(sync)
                self.assertLess(workflow.index(bootstrap), workflow.index(sync))
                self.assertIn("CATSCO_PUSH_RELAY_URL", workflow)
                self.assertIn("CATSCO_PUSH_RELAY_TOKEN", workflow)

    def test_reads_exactly_five_nul_delimited_values(self) -> None:
        self.assertEqual(
            sync.read_values(
                io.BytesIO(
                    payload(
                        "public",
                        "private",
                        "mailto:ops@catsco.cc",
                        "https://relay.example/v1/push/relay",
                        "relay-token",
                    )
                )
            ),
            (
                "public",
                "private",
                "mailto:ops@catsco.cc",
                "https://relay.example/v1/push/relay",
                "relay-token",
            ),
        )

        with self.assertRaisesRegex(ValueError, "exactly five"):
            sync.read_values(io.BytesIO(b"public\0private\0"))

    def test_render_replaces_duplicates_and_preserves_other_lines(self) -> None:
        source = (
            "KEEP=value\n"
            "VAPID_PUBLIC_KEY=old\n"
            "VAPID_PUBLIC_KEY=duplicate\n"
            "# VAPID_PRIVATE_KEY=comment\n"
            "VAPID_PRIVATE_KEY=old\n"
            "CATSCO_PUSH_RELAY_TOKEN=old\n"
            "CATSCO_PUSH_RELAY_TOKEN=duplicate\n"
        )

        rendered = sync.render(
            source,
            "public",
            "private",
            "mailto:ops@catsco.cc",
            "https://relay.example/v1/push/relay",
            "relay-token",
        )

        self.assertIn("KEEP=value\n", rendered)
        self.assertIn("# VAPID_PRIVATE_KEY=comment\n", rendered)
        assignments = [line for line in rendered.splitlines() if not line.startswith("#")]
        self.assertEqual(sum(line.startswith("VAPID_PUBLIC_KEY=") for line in assignments), 1)
        self.assertEqual(sum(line.startswith("VAPID_PRIVATE_KEY=") for line in assignments), 1)
        self.assertEqual(sum(line.startswith("VAPID_SUBJECT=") for line in assignments), 1)
        self.assertEqual(sum(line.startswith("CATSCO_PUSH_RELAY_URL=") for line in assignments), 1)
        self.assertEqual(sum(line.startswith("CATSCO_PUSH_RELAY_TOKEN=") for line in assignments), 1)
        self.assertIn("VAPID_PUBLIC_KEY=public\n", rendered)
        self.assertIn("VAPID_PRIVATE_KEY=private\n", rendered)
        self.assertIn("VAPID_SUBJECT=mailto:ops@catsco.cc\n", rendered)
        self.assertIn("CATSCO_PUSH_RELAY_URL=https://relay.example/v1/push/relay\n", rendered)
        self.assertIn("CATSCO_PUSH_RELAY_TOKEN=relay-token\n", rendered)

    def test_all_empty_values_disable_push_and_remove_existing_values(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            env_file = Path(directory) / "prod.env"
            env_file.write_text(
                "KEEP=value\n"
                "VAPID_PUBLIC_KEY=old-public\n"
                "VAPID_PRIVATE_KEY=old-private\n"
                "VAPID_SUBJECT=mailto:old@example.com\n"
                "CATSCO_PUSH_RELAY_URL=https://old.example/v1/push/relay\n"
                "CATSCO_PUSH_RELAY_TOKEN=old-token\n",
                encoding="utf-8",
            )

            sync.update_file(env_file, "", "", "", "", "")

            rendered = env_file.read_text(encoding="utf-8")
            self.assertEqual(rendered, "KEEP=value\n")

    def test_relay_only_configuration_removes_stale_vapid_values(self) -> None:
        rendered = sync.render(
            "VAPID_PUBLIC_KEY=old\nVAPID_PRIVATE_KEY=old\nVAPID_SUBJECT=mailto:old@example.com\n",
            "",
            "",
            "",
            "https://relay.example/v1/push/relay",
            "relay-token",
        )
        self.assertNotIn("VAPID_PUBLIC_KEY=", rendered)
        self.assertNotIn("VAPID_PRIVATE_KEY=", rendered)
        self.assertNotIn("VAPID_SUBJECT=", rendered)
        self.assertIn("CATSCO_PUSH_RELAY_URL=https://relay.example/v1/push/relay\n", rendered)
        self.assertIn("CATSCO_PUSH_RELAY_TOKEN=relay-token\n", rendered)

    def test_vapid_only_configuration_removes_stale_relay_values(self) -> None:
        rendered = sync.render(
            "CATSCO_PUSH_RELAY_URL=https://old.example/v1/push/relay\nCATSCO_PUSH_RELAY_TOKEN=old\n",
            "public",
            "private",
            "mailto:ops@catsco.cc",
            "",
            "",
        )
        self.assertNotIn("CATSCO_PUSH_RELAY_URL=", rendered)
        self.assertNotIn("CATSCO_PUSH_RELAY_TOKEN=", rendered)
        self.assertIn("VAPID_PUBLIC_KEY=public\n", rendered)
        self.assertIn("VAPID_PRIVATE_KEY=private\n", rendered)
        self.assertIn("VAPID_SUBJECT=mailto:ops@catsco.cc\n", rendered)

    def test_rejects_partial_or_multiline_values_before_writing(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            env_file = Path(directory) / "prod.env"
            original = "KEEP=value\n"
            env_file.write_text(original, encoding="utf-8")

            with self.assertRaisesRegex(ValueError, "configured together"):
                sync.update_file(env_file, "", "private", "mailto:ops@catsco.cc", "", "")
            self.assertEqual(env_file.read_text(encoding="utf-8"), original)

            with self.assertRaisesRegex(ValueError, "single-line"):
                sync.update_file(env_file, "public\nvalue", "private", "mailto:ops@catsco.cc", "", "")
            self.assertEqual(env_file.read_text(encoding="utf-8"), original)

            with self.assertRaisesRegex(ValueError, "configured together"):
                sync.update_file(env_file, "public", "private", "mailto:ops@catsco.cc", "https://relay.example", "")
            self.assertEqual(env_file.read_text(encoding="utf-8"), original)

    def test_missing_file_is_not_created(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            env_file = root / "env" / "prod.env"

            with self.assertRaisesRegex(FileNotFoundError, "missing env file"):
                sync.update_file(env_file, "public", "private", "mailto:ops@catsco.cc", "", "")
            self.assertFalse(env_file.exists())
            self.assertFalse(env_file.parent.exists())

    def test_existing_file_modes_are_hardened_and_update_is_atomic(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            for legacy_mode in (0o640, 0o644):
                with self.subTest(legacy_mode=oct(legacy_mode)):
                    env_file = root / f"test-{legacy_mode:o}.env"
                    env_file.write_text("VAPID_PUBLIC_KEY=old\n", encoding="utf-8")
                    os.chmod(env_file, legacy_mode)

                    sync.update_file(
                        env_file,
                        "public",
                        "private",
                        "mailto:ops@catsco.cc",
                        "https://relay.example/v1/push/relay",
                        "relay-token",
                    )

                    self.assertEqual(stat.S_IMODE(env_file.stat().st_mode), 0o600)
            self.assertFalse(any(path.name.startswith(".") for path in root.iterdir()))


if __name__ == "__main__":
    unittest.main()
