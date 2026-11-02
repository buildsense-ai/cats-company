import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { spawnSync } from 'node:child_process';

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const xiaobaRoot = process.env.XIAOBA_REPAIR_PAIRED_ROOT;
const cloudRoot = process.env.SKILLHUB_REPAIR_MATERIAL_ROOT;
if (!xiaobaRoot || !cloudRoot) throw new Error('Pinned XiaoBa and Cloud checkouts are mandatory');
const xiaoba = path.resolve(xiaobaRoot);
const cloud = path.resolve(cloudRoot);
const pin = '062c7cf1b11fa49fdc44466c1cbfe80b041d11c4';
if (!fs.existsSync(path.join(xiaoba, 'package-lock.json')) || !fs.existsSync(path.join(cloud, 'package-lock.json'))) {
  throw new Error('Pinned XiaoBa and Cloud checkouts are mandatory');
}
const revision = spawnSync('git', ['-C', xiaoba, 'rev-parse', 'HEAD'], { encoding: 'utf8' });
if (revision.status !== 0 || revision.stdout.trim() !== pin) throw new Error(`XiaoBa checkout must equal ${pin}`);
for (const file of ['src/catscompany/skillhub-rpc.ts', 'src/skillhub/publication-capture.ts', 'node_modules/tsx/package.json']) {
  if (!fs.existsSync(path.join(xiaoba, file))) throw new Error(`Missing mandatory Runtime dependency: ${file}`);
}
const result = spawnSync('go', ['test', '-race', './server', '-run', '^TestPublicationRepairMaterialCloudPostgres$', '-count=1', '-json'], {
  cwd: root, shell: false, encoding: 'utf8', timeout: 300_000, maxBuffer: 16 * 1024 * 1024,
  env: { ...process.env, CATSCO_REQUIRE_REPAIR_MATERIAL_CONTRACT: '1', CATSCO_REQUIRE_REPAIR_RUNTIME_CLOUD_CONTRACT: '1' },
});
process.stdout.write(result.stdout || '');
process.stderr.write(result.stderr || '');
if (result.error || result.status !== 0 || result.signal) throw new Error(`Runtime/Cloud paired contract failed: ${result.error?.message || result.signal || result.status}`);
const events = result.stdout.split(/\r?\n/).filter(Boolean).map(line => JSON.parse(line));
const ran = events.filter(e => e.Action === 'run' && e.Test === 'TestPublicationRepairMaterialCloudPostgres');
const passed = events.filter(e => e.Action === 'pass' && e.Test === 'TestPublicationRepairMaterialCloudPostgres');
if (ran.length !== 1 || passed.length !== 1 || events.some(e => e.Action === 'skip' || e.Action === 'fail')) {
  throw new Error('Combined Runtime + Cloud/PostgreSQL contract must pass without skips');
}
console.log('[repair-runtime-cloud-paired] Real pinned Runtime capture/source checks + real Cloud HTTPS/PostgreSQL report/material; synthetic model provider and dispatch journal only.');
