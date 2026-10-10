import { test } from 'node:test';
import assert from 'node:assert/strict';
import { mkdtemp, readFile, rm, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { spawnSync } from 'node:child_process';
import { exportRuntime } from './export-gateway-annotation-runtime.mjs';

async function fixture(run) {
  const outDir = await mkdtemp(join(tmpdir(), 'catsco-sdk-export-'));
  try { await run(outDir); } finally { await rm(outDir, { recursive: true, force: true }); }
}

test('export exact canonical bytes and deterministic manifest; check and pinned source hash work', async () => {
  await fixture(async (outDir) => {
    const result = await exportRuntime({ outDir });
    const source = await readFile(new URL('../webapp/public/catsco-annotations.js', import.meta.url));
    assert.deepEqual(await readFile(join(outDir, 'annotations-v1.js')), source);
    const firstManifest = await readFile(join(outDir, 'annotations-v1.manifest.json'));
    assert.equal(result.bytes, source.length);
    assert.match(result.sha256, /^[a-f0-9]{64}$/);
    assert.equal(result.config_attribute, 'data-catsco-parent-origins');
    // One snapshot: the top-level digest and the SDK resource entry agree.
    assert.equal(result.resources[0].sha256, result.sha256);
    assert.equal(result.resources[0].bytes, result.bytes);
    assert.deepEqual(result.resources.map((resource) => resource.filename), ['annotations-v1.js', 'html2canvas-pro-1.6.7.min.js', 'html2canvas-pro-1.6.7.LICENSE', 'html2canvas-1.4.1.min.js', 'html2canvas-1.4.1.LICENSE']);
    for (const resource of result.resources) {
      const canonical = await readFile(new URL(`../${resource.source}`, import.meta.url));
      assert.deepEqual(await readFile(join(outDir, resource.filename)), canonical);
      assert.equal(resource.bytes, canonical.length);
    }
    assert.equal((await exportRuntime({ outDir, check: true, expectedSha256: result.sha256 })).checked, true);
    await exportRuntime({ outDir });
    assert.deepEqual(await readFile(join(outDir, 'annotations-v1.manifest.json')), firstManifest);
  });
});

test('detect vendored bytes/manifest drift; wrong source pin cannot overwrite output', async () => {
  await fixture(async (outDir) => {
    await exportRuntime({ outDir });
    await writeFile(join(outDir, 'annotations-v1.js'), 'drift');
    await assert.rejects(exportRuntime({ outDir, check: true }), /drift/);
    await assert.rejects(exportRuntime({ outDir, expectedSha256: '0'.repeat(64) }), /source changed/);
    assert.equal(await readFile(join(outDir, 'annotations-v1.js'), 'utf8'), 'drift');
    await exportRuntime({ outDir });
    await writeFile(join(outDir, 'annotations-v1.manifest.json'), '{}');
    await assert.rejects(exportRuntime({ outDir, check: true }), /drift/);
  });
});

test('check detects renderer and license drift without silently replacing vendor', async () => {
  await fixture(async (outDir) => {
    for (const filename of ['html2canvas-pro-1.6.7.min.js', 'html2canvas-pro-1.6.7.LICENSE', 'html2canvas-1.4.1.min.js', 'html2canvas-1.4.1.LICENSE']) {
      await exportRuntime({ outDir });
      await writeFile(join(outDir, filename), 'changed');
      await assert.rejects(exportRuntime({ outDir, check: true }), /drift/);
      assert.equal(await readFile(join(outDir, filename), 'utf8'), 'changed');
    }
  });
});

test('real CLI accepts export/check and rejects missing/unknown flags', async () => {
  await fixture(async (outDir) => {
    const script = new URL('./export-gateway-annotation-runtime.mjs', import.meta.url);
    const run = (...args) => spawnSync(process.execPath, [script.pathname, ...args], { encoding: 'utf8' });
    const result = run('--out-dir', outDir);
    assert.equal(result.status, 0, result.stderr);
    const { sha256 } = JSON.parse(result.stdout);
    assert.equal(run('--out-dir', outDir, '--check', '--expected-sha256', sha256).status, 0);
    for (const args of [[], ['--out-dir'], ['--bogus'], ['--expected-sha256', 'bad', '--out-dir', outDir]]) {
      assert.equal(run(...args).status, 1);
    }
  });
});
