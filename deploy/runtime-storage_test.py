import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent


class RuntimeStorageTest(unittest.TestCase):
    def test_persistent_redis_is_opt_in_and_not_public(self):
        text = (ROOT / 'deploy/prod/docker-compose.yml').read_text(encoding='utf-8')
        block = text.split('\n  runtime-redis:\n', 1)[1].split('\nnetworks:', 1)[0]
        for expected in ('profiles: ["runtime-redis"]', '"--appendonly", "yes"',
                         '"--appendfsync", "always"', '"noeviction"',
                         'PROD_RUNTIME_REDIS_ROOT', ':/data', 'redis-cli', 'prodnet'):
            self.assertIn(expected, block)
        self.assertNotIn('ports:', block)
        self.assertIn('OC_RUNTIME_REQUIRE_DURABLE', text)


if __name__ == '__main__':
    unittest.main()
