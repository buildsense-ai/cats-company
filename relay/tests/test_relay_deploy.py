from __future__ import annotations

import os
import shutil
import subprocess
import tempfile
import unittest
from pathlib import Path


REPO_ROOT = Path(__file__).resolve().parents[2]
DEPLOY_SCRIPT = REPO_ROOT / "deploy" / "relay" / "remote-deploy.sh"
ROLLBACK_SCRIPT = REPO_ROOT / "deploy" / "relay" / "remote-rollback.sh"


@unittest.skipUnless(
    os.name != "nt" and shutil.which("bash"),
    "a native Unix bash is required for relay deployment integration tests",
)
class RelayDeploymentTest(unittest.TestCase):
    PREVIOUS_REVISION = "a" * 40
    NEW_REVISION = "b" * 40
    FAILED_REVISION = "c" * 40

    def setUp(self):
        self.tempdir = tempfile.TemporaryDirectory()
        self.root = Path(self.tempdir.name) / "relay"
        self.bin = Path(self.tempdir.name) / "bin"
        (self.root / "adapter").mkdir(parents=True)
        (self.root / "releases").mkdir(parents=True)
        self.bin.mkdir()
        self.systemctl_log = Path(self.tempdir.name) / "systemctl.log"

        self.write_executable(
            self.bin / "sudo",
            '#!/usr/bin/env bash\necho "$*" >> "$FAKE_SYSTEMCTL_LOG"\nexit 0\n',
        )
        self.write_executable(
            self.bin / "curl",
            '#!/usr/bin/env bash\nif [ "${FAKE_HEALTH_FAIL:-0}" = "1" ]; then exit 1; fi\nexit 0\n',
        )
        self.write_executable(self.bin / "sleep", "#!/usr/bin/env bash\nexit 0\n")

    def tearDown(self):
        self.tempdir.cleanup()

    @staticmethod
    def write_executable(path: Path, content: str):
        path.write_text(content, encoding="utf-8", newline="\n")
        path.chmod(0o755)

    def environment(self, *, fail_health: bool = False) -> dict[str, str]:
        return {
            **os.environ,
            "PATH": f"{self.bin}{os.pathsep}{os.environ.get('PATH', '')}",
            "FAKE_SYSTEMCTL_LOG": str(self.systemctl_log),
            "FAKE_HEALTH_FAIL": "1" if fail_health else "0",
        }

    def prepare_sources(self, revision: str):
        (self.root / "adapter" / "openai_adapter.py").write_text("VERSION = 'old'\n", encoding="utf-8")
        (self.root / "CURRENT_ADAPTER_REVISION").write_text(
            f"{self.PREVIOUS_REVISION}\n", encoding="utf-8"
        )
        (self.root / "releases" / f"openai_adapter-{revision}.py").write_text(
            "VERSION = 'new'\n", encoding="utf-8"
        )

    def test_success_is_atomic_and_rollback_restores_previous_source(self):
        revision = self.NEW_REVISION
        self.prepare_sources(revision)

        subprocess.run(
            ["bash", str(DEPLOY_SCRIPT), str(self.root), revision],
            check=True,
            env=self.environment(),
            capture_output=True,
            text=True,
        )

        target = self.root / "adapter" / "openai_adapter.py"
        self.assertEqual(target.read_text(encoding="utf-8"), "VERSION = 'new'\n")
        self.assertEqual((self.root / "CURRENT_ADAPTER_REVISION").read_text().strip(), revision)
        rollback_source = Path((self.root / "ROLLBACK_ADAPTER_SOURCE").read_text().strip())
        self.assertEqual(rollback_source.read_text(encoding="utf-8"), "VERSION = 'old'\n")
        self.assertEqual(
            (self.root / "ROLLBACK_ADAPTER_REVISION").read_text().strip(),
            self.PREVIOUS_REVISION,
        )

        subprocess.run(
            ["bash", str(ROLLBACK_SCRIPT), str(self.root)],
            check=True,
            env=self.environment(),
            capture_output=True,
            text=True,
        )
        self.assertEqual(target.read_text(encoding="utf-8"), "VERSION = 'old'\n")
        self.assertEqual(
            (self.root / "CURRENT_ADAPTER_REVISION").read_text().strip(),
            self.PREVIOUS_REVISION,
        )

    def test_failed_health_check_restores_previous_source(self):
        revision = self.FAILED_REVISION
        self.prepare_sources(revision)

        result = subprocess.run(
            ["bash", str(DEPLOY_SCRIPT), str(self.root), revision],
            check=False,
            env=self.environment(fail_health=True),
            capture_output=True,
            text=True,
        )

        self.assertNotEqual(result.returncode, 0)
        target = self.root / "adapter" / "openai_adapter.py"
        self.assertEqual(target.read_text(encoding="utf-8"), "VERSION = 'old'\n")
        self.assertEqual(
            (self.root / "CURRENT_ADAPTER_REVISION").read_text().strip(),
            self.PREVIOUS_REVISION,
        )

    def test_invalid_revision_is_rejected_before_install(self):
        revision = "not-a-git-sha"
        self.prepare_sources(revision)

        result = subprocess.run(
            ["bash", str(DEPLOY_SCRIPT), str(self.root), revision],
            check=False,
            env=self.environment(),
            capture_output=True,
            text=True,
        )

        self.assertNotEqual(result.returncode, 0)
        self.assertIn("40-character lowercase Git SHA", result.stderr)
        self.assertEqual(
            (self.root / "adapter" / "openai_adapter.py").read_text(encoding="utf-8"),
            "VERSION = 'old'\n",
        )
        self.assertFalse(self.systemctl_log.exists())


if __name__ == "__main__":
    unittest.main()
