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
});
