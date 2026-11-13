"""Exercise the checked-in uploads locations with real Nginx and synthetic origins."""
import http.client
import os
from pathlib import Path
import shutil
import socket
import subprocess
import tempfile
import threading
import time
import unittest
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

ROOT = Path(__file__).resolve().parents[1]
NGINX = os.environ.get('NGINX_BIN') or shutil.which('nginx')


def location(text, marker):
    start = text.index(marker)
    brace = text.index('{', start)
    depth = 1
    end = brace + 1
    while depth:
        depth += (text[end] == '{') - (text[end] == '}')
        end += 1
    return text[start:end]


def free_port():
    with socket.socket() as sock:
        sock.bind(('127.0.0.1', 0))
        return sock.getsockname()[1]


class Origin(BaseHTTPRequestHandler):
    def log_message(self, *_):
        pass

    def do_HEAD(self):
        self.do_GET()

    def do_GET(self):
        self.server.seen.append((self.command, self.path, dict(self.headers)))
        status = 200
        if '/missing' in self.path:
            status = 404
        elif '/broken' in self.path:
            status = 503
        body = self.server.label.encode()
        self.send_response(status)
        self.send_header('Content-Length', str(len(body)))
        self.end_headers()
        if self.command != 'HEAD':
            self.wfile.write(body)


class RollbackNginxTest(unittest.TestCase):
    def test_both_vhosts_route_old_and_630_requests(self):
        self.assertTrue(NGINX, 'nginx is required; set NGINX_BIN to its executable')
        origins = []
        for label in ('app', 'zos'):
            server = ThreadingHTTPServer(('127.0.0.1', 0), Origin)
            server.label, server.seen = label, []
            threading.Thread(target=server.serve_forever, daemon=True).start()
            origins.append(server)
        app, zos = origins
        try:
            for name in ('catscompany-app.conf', 'catscompany-app-cn.conf'):
                with self.subTest(vhost=name), tempfile.TemporaryDirectory(
                    prefix='rollback-nginx-', dir=os.environ.get('PI_SCRATCH_DIR')
                ) as temp:
                    text = (ROOT / 'deploy/tencent/nginx' / name).read_text(encoding='utf-8')
                    routes = '\n'.join(location(text, marker) for marker in
                                       ('location /uploads/', 'location @uploads_fallback'))
                    (Path(temp) / 'logs').mkdir()
                    (Path(temp) / 'temp').mkdir()
                    routes = routes.replace('http://100.86.2.1/', f'http://127.0.0.1:{zos.server_port}/')
                    routes = routes.replace('http://127.0.0.1:28080', f'http://127.0.0.1:{app.server_port}')
                    port = free_port()
                    config = Path(temp) / 'nginx.conf'
                    config.write_text(
                        'worker_processes 1;\npid nginx.pid;\nerror_log stderr crit;\n'
                        'events { worker_connections 64; }\nhttp { access_log off;\n'
                        'map $uri $uploads_disp { default "attachment"; }\n'
                        'map $uri $uploads_csp { default "default-src \'none\'"; }\n'
                        f'server {{ listen 127.0.0.1:{port};\n{routes}\n}}\n}}\n', encoding='utf-8')
                    args = [str(NGINX), '-p', Path(temp).as_posix() + '/', '-c', 'nginx.conf']
                    checked = subprocess.run(args + ['-t'], cwd=temp, capture_output=True, text=True)
                    self.assertEqual(checked.returncode, 0, checked.stderr)
                    process = subprocess.Popen(args + ['-g', 'daemon off;'], cwd=temp,
                                               stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
                    try:
                        deadline = time.monotonic() + 10
                        while True:
                            try:
                                with socket.create_connection(('127.0.0.1', port), timeout=.2):
                                    break
                            except OSError:
                                if time.monotonic() > deadline or process.poll() is not None:
                                    self.fail('local nginx did not start')
                                time.sleep(.05)
                        cases = [
                            ('GET', '/uploads/files/fixture.txt', {}, 'zos', 200),
                            ('GET', '/uploads/files/fixture.txt?message_id=42', {}, 'app', 200),
                            ('GET', '/uploads/files/fixture.txt', {'Authorization': 'ApiKey synthetic'}, 'app', 200),
                            ('GET', '/uploads/files/fixture.txt?message_id=42', {'Authorization': 'DeviceConnector synthetic'}, 'app', 200),
                            ('HEAD', '/uploads/files/fixture.txt?message_id=42', {}, 'app', 200),
                            ('GET', '/uploads/files/fixture.txt?download=1', {}, 'app', 200),
                            ('GET', '/uploads/files/fixture.txt?preview=1', {}, 'app', 200),
                            ('GET', '/uploads/files/missing.txt', {}, 'app', 404),
                            ('GET', '/uploads/files/broken.txt', {}, 'app', 503),
                            ('GET', '/uploads/files/missing.txt?message_id=42', {'Authorization': 'ApiKey synthetic'}, 'app', 404),
                            ('GET', '/uploads/files/broken.txt?message_id=42', {'Authorization': 'ApiKey synthetic'}, 'app', 503),
                        ]
                        for method, url, headers, expected, status in cases:
                            with self.subTest(method=method, path=url, origin=expected):
                                app.seen.clear()
                                zos.seen.clear()
                                conn = http.client.HTTPConnection('127.0.0.1', port, timeout=5)
                                try:
                                    conn.request(method, url, headers=headers)
                                    response = conn.getresponse()
                                    body = response.read()
                                    self.assertEqual(response.status, status)
                                    self.assertEqual(body, b'' if method == 'HEAD' else expected.encode())
                                finally:
                                    conn.close()
                                if headers or 'message_id=' in url or 'preview=' in url or 'download=' in url:
                                    self.assertEqual(zos.seen, [], 'application requests must never reach ZOS')
                                if expected == 'app':
                                    self.assertEqual(app.seen[0][1], url)
                                    if headers:
                                        self.assertEqual(app.seen[0][2].get('Authorization'), headers['Authorization'])
                                else:
                                    self.assertEqual(app.seen, [])
                    finally:
                        subprocess.run(args + ['-s', 'quit'], cwd=temp, capture_output=True, timeout=10)
                        try:
                            process.wait(timeout=10)
                        except subprocess.TimeoutExpired:
                            process.kill()
                            process.wait(timeout=5)
        finally:
            for server in origins:
                server.shutdown()
                server.server_close()


if __name__ == '__main__':
    unittest.main()
