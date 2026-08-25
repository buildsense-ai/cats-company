import { existsSync, readdirSync, readFileSync } from 'node:fs';

const BUILD_ROOT = new URL('../build/', import.meta.url);
const assetsRoot = new URL('./assets/', BUILD_ROOT);
const serviceWorkerPath = new URL('./sw.js', BUILD_ROOT);
const manifestPath = new URL('./manifest.webmanifest', BUILD_ROOT);

function fail(message) {
  throw new Error(`[webapp build] ${message}`);
}

for (const artifact of ['index.html', 'manifest.webmanifest', 'offline.html', 'sw.js']) {
  if (!existsSync(new URL(`./${artifact}`, BUILD_ROOT))) fail(`missing build/${artifact}`);
}
if (!existsSync(assetsRoot)) fail('missing build/assets directory');

const serviceWorker = readFileSync(serviceWorkerPath, 'utf8');
const manifest = JSON.parse(readFileSync(manifestPath, 'utf8'));
if (manifest.id !== '/' || manifest.scope !== '/' || manifest.display !== 'standalone') {
  fail('manifest does not declare the required standalone app identity');
}
const requiredPrecacheEntries = [
  'manifest.webmanifest',
  'assets/index-',
  'assets/workbox-window',
  'offline.html',
];

for (const entry of requiredPrecacheEntries) {
  if (!serviceWorker.includes(`"url":"${entry}`)) {
    fail(`service worker does not precache ${entry}`);
  }
}

if (serviceWorker.includes('"url":"index.html')) {
  fail('service worker unexpectedly precaches navigation HTML');
}

if (serviceWorker.includes('"url":"assets/tinode-web-')) {
  fail('service worker unexpectedly precaches the workspace chunk');
}

for (const forbiddenEntry of ['assets/pdf-', 'assets/pdf.worker.']) {
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
