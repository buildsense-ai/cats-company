import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import test from 'node:test';

const script = fs.readFileSync(
  path.join(path.dirname(fileURLToPath(import.meta.url)), 'prune-worker-releases.sh'),
  'utf8',
);

test('worker release retention defaults to a bounded rollback window', () => {
  assert.match(script, /KEEP_COUNT="\$\{CATSCO_WORKER_RELEASE_KEEP_COUNT:-3\}"/);
  assert.match(script, /protect(?:ed|s).*current|protected.*current/i);
  assert.match(script, /STATUS_SCRIPT/);
  assert.match(script, /NF>=4 && \$4!="" \{print \$4\}/);
  assert.doesNotMatch(script, /NF>=5 && \$5!="" \{print \$5\}/);
});

test('worker release deletion remains fail-closed and explicit', () => {
  assert.match(script, /--apply/);
  assert.match(script, /I_UNDERSTAND_DELETE_WORKER_RELEASES/);
  assert.match(script, /CATSCO_WORKER_RELEASE_PRUNE_ENABLED/);
  assert.match(script, /status probe failed; refusing deletion/);
  assert.match(script, /CATSCO_WORKER_STATUS_SCRIPT is required for --apply/);
});
