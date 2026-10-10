#!/usr/bin/env node
// The public SDK is the only editable runtime source. Export exact bytes;
// gateway deployments can verify this deterministic manifest before serving.
import { createHash } from 'node:crypto';
import { mkdir, readFile, writeFile } from 'node:fs/promises';
import { dirname, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const root = resolve(dirname(fileURLToPath(import.meta.url)), '..');
const sourcePath = resolve(root, 'webapp/public/catsco-annotations.js');
const usage = 'Usage: node scripts/export-gateway-annotation-runtime.mjs --out-dir <directory> [--check] [--expected-sha256 <hex>]';

export async function exportRuntime({ outDir, check = false, expectedSha256 } = {}) {
  if (!outDir) throw new Error(usage);
  if (expectedSha256 !== undefined && !/^[a-f0-9]{64}$/.test(expectedSha256)) {
    throw new Error('--expected-sha256 must be a lowercase SHA-256 hex digest');
  }
  const bytes = await readFile(sourcePath);
  const sha256 = createHash('sha256').update(bytes).digest('hex');
  if (expectedSha256 && sha256 !== expectedSha256) throw new Error(`SDK source changed: expected ${expectedSha256}, got ${sha256}`);
  const outputPath = resolve(outDir, 'annotations-v1.js');
  if (outputPath === sourcePath) throw new Error('Refusing to overwrite the canonical SDK');
  const specs = [
    // The SDK bytes are reused from the single read above so the top-level
    // sha256 and the manifest resource entry can never disagree.
    { filename: 'annotations-v1.js', source: 'webapp/public/catsco-annotations.js', mime_type: 'application/javascript', data: bytes },
    { filename: 'html2canvas-pro-1.6.7.min.js', source: 'webapp/public/catsco-runtime/html2canvas-pro-1.6.7.min.js', mime_type: 'application/javascript', pin: 'bacbbb275f41a08e6eb4db0c5b44d9477546186d3078757577f4205147d1814f' },
    { filename: 'html2canvas-pro-1.6.7.LICENSE', source: 'webapp/public/catsco-runtime/html2canvas-pro-1.6.7.LICENSE', mime_type: 'text/plain', pin: '04092b9193d5ef3c611d82509387a6341447dccbe2330716884383c4dc9c568a' },
    // Existing documents have already frozen the old SDK/renderer URL. Keep
    // those exact assets available during rollout; reload picks the new SDK.
    { filename: 'html2canvas-1.4.1.min.js', source: 'webapp/public/catsco-runtime/html2canvas-1.4.1.min.js', mime_type: 'application/javascript', pin: 'e87e550794322e574a1fda0c1549a3c70dae5a93d9113417a429016838eab8cb' },
    { filename: 'html2canvas-1.4.1.LICENSE', source: 'webapp/public/catsco-runtime/html2canvas-1.4.1.LICENSE', mime_type: 'text/plain', pin: '86200ce4e92d9a22c41c8647a55f7a5fddff304ff89b4d36ecc699ed8c123d2c' },
  ];
  const assets = await Promise.all(specs.map(async (spec) => {
    const data = spec.data ?? await readFile(resolve(root, spec.source));
    const digest = createHash('sha256').update(data).digest('hex');
    if (spec.pin && digest !== spec.pin) throw new Error(`Pinned renderer/license source drift: ${spec.filename}`);
    return { data, resource: { filename: spec.filename, runtime_path: `/_catsco/runtime/${spec.filename}`, source: spec.source, sha256: digest, bytes: data.length, mime_type: spec.mime_type } };
  }));
  const manifest = {
    contract_version: 'catsco.gateway-annotation-runtime-export.v1',
    runtime_path: '/_catsco/runtime/annotations-v1.js',
    config_attribute: 'data-catsco-parent-origins',
    source: 'webapp/public/catsco-annotations.js',
    sha256,
    bytes: bytes.length,
    resources: assets.map((asset) => asset.resource),
  };
  const manifestBytes = Buffer.from(`${JSON.stringify(manifest, null, 2)}\n`);
  const manifestPath = resolve(outDir, 'annotations-v1.manifest.json');
  if (check) {
    const exported = await Promise.all(assets.map((asset) => readFile(resolve(outDir, asset.resource.filename))));
    const exportedManifest = await readFile(manifestPath);
    if (assets.some((asset, index) => !asset.data.equals(exported[index])) || !manifestBytes.equals(exportedManifest)) {
      throw new Error('Gateway runtime/manifest drift: re-export from the canonical public SDK');
    }
  } else {
    await mkdir(resolve(outDir), { recursive: true });
    await Promise.all(assets.map((asset) => writeFile(resolve(outDir, asset.resource.filename), asset.data)));
    await writeFile(manifestPath, manifestBytes);
  }
  return { ...manifest, output: outputPath, checked: check };
}

async function main(args) {
  const options = {};
  for (let i = 0; i < args.length; i++) {
    if (args[i] === '--check') options.check = true;
    else if (args[i] === '--out-dir' || args[i] === '--expected-sha256') {
      const flag = args[i];
      if (!args[i + 1] || args[i + 1].startsWith('--')) throw new Error(usage);
      options[flag === '--out-dir' ? 'outDir' : 'expectedSha256'] = args[++i];
    } else if (args[i] === '--help') {
      console.log(usage);
      return;
    } else throw new Error(usage);
  }
  console.log(JSON.stringify(await exportRuntime(options), null, 2));
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  main(process.argv.slice(2)).catch((error) => {
    console.error(error.message);
    process.exitCode = 1;
  });
}
