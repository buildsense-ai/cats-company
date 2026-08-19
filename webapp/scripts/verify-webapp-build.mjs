import { existsSync, readdirSync, readFileSync } from 'node:fs';

const BUILD_ROOT = new URL('../build/', import.meta.url);
const assetsRoot = new URL('./assets/', BUILD_ROOT);
const serviceWorkerPath = new URL('./sw.js', BUILD_ROOT);

function fail(message) {
  throw new Error(`[webapp build] ${message}`);
}

if (!existsSync(serviceWorkerPath)) fail('missing build/sw.js');
if (!existsSync(assetsRoot)) fail('missing build/assets directory');

const serviceWorker = readFileSync(serviceWorkerPath, 'utf8');
const requiredPrecacheEntries = [
  'index.html',
  'assets/index-',
  'assets/workbox-window',
  'offline.html',
];

for (const entry of requiredPrecacheEntries) {
  if (!serviceWorker.includes(`"url":"${entry}`)) {
    fail(`service worker does not precache ${entry}`);
  }
}

for (const forbiddenEntry of ['tinode-web-', 'pdf-']) {
  if (serviceWorker.includes(forbiddenEntry)) {
    fail(`service worker unexpectedly precaches ${forbiddenEntry} chunk`);
  }
}

const assetNames = readdirSync(assetsRoot);
if (!assetNames.some((name) => /^tinode-web-.+\.js$/.test(name))) {
  fail('workspace chunk is missing from the production build');
}
if (!assetNames.some((name) => /^pdf-.+\.js$/.test(name))) {
  fail('PDF chunk is missing from the production build');
}

console.log('Webapp build split and precache checks passed.');
