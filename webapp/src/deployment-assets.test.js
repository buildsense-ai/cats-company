import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { describe, expect, it } from 'vitest';
import viteConfig from '../vite.config.js';

function nginxLocationBlock(config, path) {
  const escapedPath = path.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
  const match = config.match(new RegExp(`location\\s+${escapedPath}\\s*\\{([^}]*)\\}`));
  expect(match, `missing Nginx location for ${path}`).not.toBeNull();
  return match[1];
}

function expectImmutableAssetLocation(config, path) {
  const block = nginxLocationBlock(config, path);
  expect(block).toContain('try_files $uri =404;');
  expect(block).toContain('expires 1y;');
  expect(block).toContain(
    'add_header Cache-Control "public, max-age=31536000, immutable" always;',
  );
}

function expectPrivateProxyLocation(config, path) {
  const block = nginxLocationBlock(config, path);
  expect(block).toContain('proxy_hide_header Cache-Control;');
  expect(block).toContain('add_header Cache-Control "no-store" always;');
  expect(block).toContain('proxy_cache off;');
  expect(block).toContain('proxy_no_cache 1;');
  expect(block).toContain('proxy_cache_bypass 1;');
}

function expectSTTWebSocketTimeout(config) {
  const block = nginxLocationBlock(config, '/api/stt/realtime');
  expect(block).toContain('proxy_read_timeout 180s;');
  expect(block).toContain('proxy_send_timeout 180s;');
}

describe('production asset caching', () => {
  it('immutably caches the Vite asset directory and the legacy static directory', () => {
    const nginxConfig = readFileSync(
      resolve(process.cwd(), '../deploy/nginx/nginx.conf'),
      'utf8',
    );
    const assetsDir = viteConfig.build?.assetsDir ?? 'assets';

    expectImmutableAssetLocation(nginxConfig, `/${assetsDir.replace(/^\/|\/$/g, '')}/`);
    expectImmutableAssetLocation(nginxConfig, '/static/');
  });

  it('does not permit HTTP caching for API responses or uploads', () => {
    const nginxConfig = readFileSync(
      resolve(process.cwd(), '../deploy/nginx/nginx.conf'),
      'utf8',
    );

    expectPrivateProxyLocation(nginxConfig, '/api/');
    expectPrivateProxyLocation(nginxConfig, '/api/stt/realtime');
    expectPrivateProxyLocation(nginxConfig, '/v1/');
    expectPrivateProxyLocation(nginxConfig, '/uploads/');
    expectPrivateProxyLocation(nginxConfig, '/v0/channels');
  });

  it('does not expose capability tokens through the SPA referrer', () => {
    const nginxConfig = readFileSync(
      resolve(process.cwd(), '../deploy/nginx/nginx.conf'),
      'utf8',
    );
    expect(nginxConfig).toContain('location ^~ /share/ {');
    const shareBlock = nginxConfig.match(/location \^~ \/share\/ \{([^}]*)\}/)?.[1] || '';
    expect(shareBlock).toContain('add_header Referrer-Policy "no-referrer" always;');
  });

  it('does not permit HTTP caching at the Internet-facing TLS proxies', () => {
    const privateRoutesByConfig = new Map([
      [
        '../deploy/tencent/nginx/catscompany-app.conf',
        ['/api/', '/api/stt/realtime', '/v1/', '/uploads/', '/v0/channels'],
      ],
      [
        '../deploy/tencent/nginx/catscompany-api.conf',
        ['/api/', '/api/stt/realtime', '/v1/', '/v0/channels', '/advanced-reader/'],
      ],
    ]);

    for (const [configPath, privateRoutes] of privateRoutesByConfig) {
      const nginxConfig = readFileSync(resolve(process.cwd(), configPath), 'utf8');

      for (const route of privateRoutes) {
        expectPrivateProxyLocation(nginxConfig, route);
      }
    }
  });

  it('keeps STT websocket proxy timeouts above the 150 second session limit', () => {
    for (const configPath of [
      '../deploy/nginx/nginx.conf',
      '../deploy/tencent/nginx/catscompany-app.conf',
      '../deploy/tencent/nginx/catscompany-api.conf',
    ]) {
      expectSTTWebSocketTimeout(readFileSync(resolve(process.cwd(), configPath), 'utf8'));
    }
  });
});
