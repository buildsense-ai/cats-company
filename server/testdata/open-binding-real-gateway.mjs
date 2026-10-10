// Test-only loopback fixture. Imports the actual gateway control plane rather
// than reimplementing /_gateway/apps or /_gateway/codes. No source probes,
// deployment, production routes or app credentials are modified.
import { pathToFileURL } from 'node:url';
import path from 'node:path';
import fs from 'node:fs';
import os from 'node:os';

const root = process.env.CATSCO_GATEWAY_TEST_ROOT;
if (!root) throw new Error('CATSCO_GATEWAY_TEST_ROOT is required');
const { createControlPlane } = await import(pathToFileURL(path.join(root, 'src/control-plane.mjs')));
const { ViewerStore } = await import(pathToFileURL(path.join(root, 'src/viewer-store.mjs')));
const directory = fs.mkdtempSync(path.join(os.tmpdir(), 'catsco-open-binding-real-gateway-'));
const config = {
  sshPort: 22443, user: 'cag_ingress', publicHosts: ['artifact.example.test'],
  hostKey: '/test/key', authorizedKeys: '/test/keys', controlPort: 22445,
  apps: [
    { id: 'board', agent: '9', remotePort: 28191, publicKey: 'ssh-ed25519 AAAATEST board', title: 'Board' },
    { id: 'other', agent: '11', remotePort: 28192, publicKey: 'ssh-ed25519 AAAATEST other', title: 'Other' },
  ],
};
const server = createControlPlane({
  config, store: new ViewerStore({ file: path.join(directory, 'viewer-state.json') }),
  controlToken: 'control-test-0123456789abcdef-0123456789', cookieSecure: false,
  fetchImpl: () => { throw new Error('unexpected platform identity call'); },
  statusProbe: { probeAll: async apps => new Map(apps.map(app => [app.id, 'offline'])) },
  logger: { error() {} },
});
await new Promise(resolve => server.listen(0, '127.0.0.1', resolve));
console.log(JSON.stringify({ control_url: `http://127.0.0.1:${server.address().port}`, public_origin: 'https://artifact.example.test' }));
process.stdin.resume();
process.stdin.on('end', () => server.close(() => {
  fs.rmSync(directory, { recursive: true, force: true });
}));
process.on('SIGTERM', () => server.close(() => {
  fs.rmSync(directory, { recursive: true, force: true });
  process.exit(0);
}));
