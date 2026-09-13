#!/usr/bin/env python3
"""Tests for synchronizing Shimo browser Worker settings."""

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
spec = importlib.util.spec_from_file_location("sync_shimo_worker_env", SCRIPT_DIR / "sync-shimo-worker-env.py")
if spec is None or spec.loader is None:
    raise RuntimeError("failed to load sync-shimo-worker-env.py")
sync = importlib.util.module_from_spec(spec)
spec.loader.exec_module(sync)


class SyncShimoWorkerEnvTest(unittest.TestCase):
    def test_empty_payload_is_a_noop(self) -> None:
        self.assertEqual(sync.read_values(io.BytesIO(b"\0\0\0")), ("", "", ""))
        source = "KEEP=value\nCOMPOSE_PROFILES=mysql\n"
        self.assertEqual(sync.render(source, "", "", ""), source)

    def test_valid_payload_enables_shimo_profile_and_preserves_existing_profiles(self) -> None:
        source = "KEEP=value\nCOMPOSE_PROFILES=mysql\n"
        rendered = sync.render(source, "t" * 32, "a" * 64, "http://shimo-worker:7070")
        self.assertIn("COMPOSE_PROFILES=mysql,shimo\n", rendered)
        self.assertIn("CATSCO_SHIMO_WORKER_TOKEN=" + "t" * 32, rendered)
        self.assertIn("SHIMO_WORKER_SESSION_KEY=" + "a" * 64, rendered)
        self.assertEqual(rendered.count("COMPOSE_PROFILES="), 1)

    def test_rejects_partial_or_invalid_payload(self) -> None:
        with self.assertRaisesRegex(ValueError, "at least 32"):
            sync.normalize_values("short", "a" * 64, "http://shimo-worker:7070")
        with self.assertRaisesRegex(ValueError, "64 hex characters"):
            sync.normalize_values("t" * 32, "invalid", "http://shimo-worker:7070")
        with self.assertRaisesRegex(ValueError, "http\(s\)"):
            sync.normalize_values("t" * 32, "a" * 64, "shimo-worker:7070")

    def test_update_is_owner_only(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            env_file = Path(directory) / "test.env"
            env_file.write_text("COMPOSE_PROFILES=\n", encoding="utf-8")
            sync.update_file(env_file, "t" * 32, "a" * 64, "http://shimo-worker:7070")
            if sys.platform != "win32":
                self.assertEqual(stat.S_IMODE(env_file.stat().st_mode), 0o600)


if __name__ == "__main__":
    unittest.main()
