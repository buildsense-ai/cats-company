import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { spawnSync } from 'node:child_process';

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const xiaoba = path.resolve(process.env.XIAOBA_REPO || path.join(root, '..', 'XiaoBa-CLI'));
const pin = '062c7cf1b11fa49fdc44466c1cbfe80b041d11c4';
const revision = spawnSync('git', ['-C', xiaoba, 'rev-parse', 'HEAD'], { encoding: 'utf8' });
if (revision.status !== 0 || revision.stdout.trim() !== pin) throw new Error(`XiaoBa checkout must equal ${pin}`);
for (const file of ['src/catscompany/skillhub-rpc.ts', 'src/skillhub/publication-capture.ts', 'node_modules/tsx/package.json']) {
  if (!fs.existsSync(path.join(xiaoba, file))) throw new Error(`Missing mandatory Runtime dependency: ${file}`);
}
const result = spawnSync('go', ['test', './server', '-run', '^TestSkillHubPublicationRepairSourcePaired', '-count=1', '-json'], {
  cwd: root, shell: false, encoding: 'utf8', timeout: 240_000, maxBuffer: 16 * 1024 * 1024,
  env: { ...process.env, XIAOBA_REPAIR_PAIRED_ROOT: xiaoba, XIAOBA_REPAIR_PAIRED_NODE: process.execPath },
});
process.stdout.write(result.stdout || ''); process.stderr.write(result.stderr || '');
if (result.error || result.status !== 0 || result.signal) throw new Error(`Paired source contract failed: ${result.error?.message || result.signal || result.status}`);
const events = result.stdout.split(/\r?\n/).filter(Boolean).map(line => JSON.parse(line));
const suites = ['TestSkillHubPublicationRepairSourcePairedReplay', 'TestSkillHubPublicationRepairSourcePairedFailures', 'TestSkillHubPublicationRepairSourcePairedLaterRounds'];
const expected = [...suites, ...['normal', 'malformed'].map(n => `${suites[0]}/${n}`),
  ...['change', 'remove', 'disable', 'owner', 'switching', 'missing-capture', 'mismatch'].map(n => `${suites[1]}/${n}`),
  ...['round-2', 'round-2-source-changed', 'round-3', 'round-3-source-changed'].map(n => `${suites[2]}/${n}`)];
const passed = new Set(events.filter(e => e.Action === 'pass').map(e => e.Test));
const ran = events.filter(e => e.Action === 'run' && e.Test);
if (events.some(e => e.Action === 'skip' || e.Action === 'fail') || expected.some(n => !passed.has(n)) || ran.length !== expected.length || ran.some(e => !passed.has(e.Test))) {
  throw new Error('All three suites and thirteen subcases must pass without skips');
}
console.log('[repair-source-paired] Real CatsCo transport and pinned XiaoBa handler/store; synthetic socket/authority, not deployed E2E.');
