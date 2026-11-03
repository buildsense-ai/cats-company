import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent


class RuntimeStorageTest(unittest.TestCase):
    def test_authorization_only_wiring_is_independent_and_opt_in(self):
        names = ('OC_DEVICE_AUTH_STORE', 'OC_DEVICE_AUTH_REDIS_URL',
                 'OC_DEVICE_AUTH_REDIS_KEY_PREFIX', 'OC_DEVICE_AUTH_REQUIRE_DURABLE')
        for compose in ('deploy/prod/docker-compose.yml', 'deploy/test/docker-compose.yml'):
            text = (ROOT / compose).read_text(encoding='utf-8')
            for name in names:
                self.assertIn(name + ':', text)
            self.assertIn('OC_DEVICE_AUTH_STORE:-}', text)
            self.assertIn('OC_DEVICE_AUTH_REQUIRE_DURABLE:-false}', text)
        for example in ('.env.example', 'deploy/prod/env.prod.example',
                        'deploy/test/env.test.example', 'deploy/test/env.test.postgres.example'):
            text = (ROOT / example).read_text(encoding='utf-8')
            for name in names:
                self.assertIn(name + '=', text)
            self.assertIn('OC_DEVICE_AUTH_REQUIRE_DURABLE=false', text)

    def test_persistent_redis_is_opt_in_and_not_public(self):
        text = (ROOT / 'deploy/prod/docker-compose.yml').read_text(encoding='utf-8')
        block = text.split('\n  runtime-redis:\n', 1)[1].split('\nnetworks:', 1)[0]
        for expected in ('profiles: ["runtime-redis"]', '"--appendonly", "yes"',
                         '"--appendfsync", "always"', '"noeviction"',
                         'PROD_RUNTIME_REDIS_ROOT', ':/data', 'redis-cli', 'prodnet'):
            self.assertIn(expected, block)
        self.assertNotIn('ports:', block)
        self.assertIn('OC_RUNTIME_REQUIRE_DURABLE', text)

    def test_storage_guard_is_executed_by_ci(self):
        workflow = (ROOT / '.github/workflows/ci.yml').read_text(encoding='utf-8')
        self.assertIn('python3 deploy/runtime-storage_test.py', workflow)

    def test_runtime_gate_reaches_test_containers(self):
        text = (ROOT / 'deploy/test/docker-compose.yml').read_text(encoding='utf-8')
        self.assertIn('OC_RUNTIME_REQUIRE_DURABLE:', text)
        for name in ('.env.example', 'deploy/test/env.test.example',
                     'deploy/test/env.test.postgres.example'):
            self.assertIn('OC_RUNTIME_REQUIRE_DURABLE=false',
                          (ROOT / name).read_text(encoding='utf-8'))


if __name__ == '__main__':
    unittest.main()
