"""Static rollback contracts; runtime Nginx checks remain a separate deployment gate."""
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]


class AttachmentRollbackTest(unittest.TestCase):
    def test_both_public_vhosts_bypass_zos_for_existing_authenticated_clients(self):
        for name in ('catscompany-app.conf', 'catscompany-app-cn.conf'):
            with self.subTest(name=name):
                text = (ROOT / 'deploy/tencent/nginx' / name).read_text(encoding='utf-8')
                uploads = text.split('location /uploads/ {', 1)[1].split('location @uploads_fallback', 1)[0]
                upstream = uploads.index('proxy_pass http://100.86.2.1/catscompany/uploads/;')
                for guard in ('if ($http_authorization != "") { return 418; }',
                              'if ($arg_message_id != "") { return 418; }',
                              'if ($arg_preview) { return 418; }',
                              'if ($arg_download) { return 418; }'):
                    self.assertLess(uploads.index(guard), upstream)
                self.assertIn('error_page 403 404 418 500 502 503 504 = @uploads_fallback;', uploads)
                fallback = text.split('location @uploads_fallback {', 1)[1].split('}', 1)[0]
                self.assertIn('proxy_pass http://127.0.0.1:28080$request_uri;', fallback)
                self.assertNotIn('error_page', fallback)
                self.assertIn('access_log /var/log/nginx/access.log catsco_safe;', text)

    def test_migration_history_and_idempotent_schema_remain(self):
        migration = (ROOT / 'server/db/migrations/postgres/000067_file_access.up.sql').read_text(encoding='utf-8')
        self.assertTrue((ROOT / 'server/db/migrations/postgres/000067_file_access.down.sql').is_file())
        for backend in ('postgres', 'mysql'):
            schema = (ROOT / f'server/db/{backend}/schema.go').read_text(encoding='utf-8')
            definitions = (ROOT / f'server/db/{backend}/file_access_schema.go').read_text(encoding='utf-8')
            for symbol in ('createFileUploadsTable', 'createFileMessageBindingsTable', 'createFileMediaSessionsTable'):
                self.assertIn(symbol, schema)
                self.assertIn(symbol, definitions)
            for table in ('file_uploads', 'file_message_bindings', 'file_media_sessions'):
                self.assertRegex(definitions, rf'CREATE TABLE IF NOT EXISTS {table}\b')
                self.assertIn(table, migration)

    def test_no_global_media_bootstrap_remains(self):
        for name in ('auth-session.js', 'api.js'):
            source = (ROOT / 'webapp/src' / name).read_text(encoding='utf-8')
            self.assertNotIn('ensureFileMediaSession', source)
            self.assertNotIn('file-media-session', source)
        self.assertFalse((ROOT / 'webapp/src/file-media-session.js').exists())


if __name__ == '__main__':
    unittest.main()
