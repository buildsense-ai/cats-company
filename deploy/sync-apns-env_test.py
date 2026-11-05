import base64
import importlib.util
from pathlib import Path
import tempfile
import unittest

spec = importlib.util.spec_from_file_location("sync_apns", Path(__file__).with_name("sync-apns-env.py"))
sync_apns = importlib.util.module_from_spec(spec)
spec.loader.exec_module(sync_apns)


class APNsDeployTests(unittest.TestCase):
    def test_key_is_installed_privately_and_never_added_to_env(self):
        with tempfile.TemporaryDirectory() as root:
            env = Path(root) / "prod.env"
            secret = Path(root) / "secrets"
            env.write_text("OTHER_SETTING=preserved\nAPNS_KEY_ID=old\nAPNS_KEY_ID=duplicate\n")
            private = b"-----BEGIN " + b"PRIVATE KEY-----\nfixture\n-----END " + b"PRIVATE KEY-----\n"
            values = ["6Y2YB464VU", "CZ4CA4QXYU", "cc.catsco.ios", base64.b64encode(private).decode()]
            sync_apns.synchronize(env, secret, values)
            result = env.read_text()
            self.assertIn("OTHER_SETTING=preserved", result)
            self.assertEqual(result.count("APNS_KEY_ID="), 1)
            self.assertNotIn("fixture", result)
            self.assertNotIn(values[-1], result)
            self.assertEqual((secret / "apns.p8").read_bytes(), private)
            self.assertEqual((secret / "apns.p8").stat().st_mode & 0o777, 0o600)
            self.assertEqual(env.stat().st_mode & 0o777, 0o600)
            sync_apns.synchronize(env, secret, ["", "", "", ""])
            self.assertIn("APNS_KEY_FILE=\n", env.read_text())
            self.assertTrue((secret / "apns.p8").exists())

    def test_invalid_secrets_do_not_mutate_existing_configuration(self):
        for invalid in (["TEAM", "", "", ""], ["6Y2YB464VU", "CZ4CA4QXYU", "bad\nbundle", "AAAA"], ["6Y2YB464VU", "CZ4CA4QXYU", "cc.catsco.ios", "not-base64"]):
            with self.subTest(invalid=invalid), tempfile.TemporaryDirectory() as root:
                env = Path(root) / "prod.env"
                env.write_text("EXISTING=kept\n")
                with self.assertRaises(ValueError):
                    sync_apns.synchronize(env, Path(root) / "secrets", invalid)
                self.assertEqual(env.read_text(), "EXISTING=kept\n")
                self.assertFalse((Path(root) / "secrets").exists())


if __name__ == "__main__":
    unittest.main()
