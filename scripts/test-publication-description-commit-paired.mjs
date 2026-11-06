import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { spawnSync } from 'node:child_process';

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const cloud = path.resolve(process.env.SKILLHUB_REPO || path.join(root, '..', 'skillhub'));
const pin = 'b1d9f49c50738da3213c9effc2a15e1439beb8c7';
const revision = spawnSync('git', ['-C', cloud, 'rev-parse', 'HEAD'], { encoding: 'utf8' });
if (revision.status !== 0 || revision.stdout.trim() !== pin) throw Error(`Cloud checkout must equal ${pin}`);
if (!process.env.SKILLHUB_TEST_DATABASE_URL) throw Error('Real Cloud PostgreSQL is mandatory');
for (const file of ['apps/api/publication-description-derivation.js', 'packages/publication-description-derivations/commit-admission.js',
  'packages/publication-description-derivations/release-plan.js', 'packages/publication-description-derivations/release-status.js', 'infra/migrations/026_publication_description_release_active_source.sql',
  'tests/helpers/description-copy.js', 'node_modules/yaml/package.json']) {
  if (!fs.existsSync(path.join(cloud, file))) throw Error(`Missing mandatory Cloud dependency: ${file}`);
}
const result = spawnSync('go', ['test', './server', '-run', '^TestPublicationDescriptionReleaseCommitPairedCloud$', '-count=1', '-json'], {
  cwd: root, shell: false, encoding: 'utf8', timeout: 300000, maxBuffer: 8 * 1024 * 1024,
  env: { ...process.env, SKILLHUB_DESCRIPTION_COMMIT_ROOT: cloud, SKILLHUB_DESCRIPTION_COMMIT_NODE: process.execPath }
});
process.stdout.write(result.stdout || ''); process.stderr.write(result.stderr || '');
if (result.error || result.status !== 0 || result.signal) throw Error(`Cloud commit admission contract failed: ${result.error?.message || result.signal || result.status}`);
const events = result.stdout.split(/\r?\n/).filter(Boolean).map(line => JSON.parse(line));
const suite = 'TestPublicationDescriptionReleaseCommitPairedCloud';
const passed = new Set(events.filter(event => event.Action === 'pass').map(event => event.Test));
if (events.some(event => event.Action === 'skip' || event.Action === 'fail') || !passed.has(suite) ||
  events.filter(event => event.Action === 'run' && event.Test).length !== 1) {
  throw Error('The commit admission paired case must execute without skips');
}
console.log('[description-commit-paired] Real Cloud PostgreSQL permit/admission and CatsCo HTTPS client/coordinator; synthetic CatsCo authority/Runtime, not deployed E2E.');
