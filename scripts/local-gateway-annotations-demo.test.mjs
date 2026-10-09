import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import http from 'node:http';
import { mkdtemp, readFile, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { once } from 'node:events';
import test from 'node:test';

async function listen(server) {
  server.listen(0, '127.0.0.1');
  await once(server, 'listening');
  return server.address().port;
}

async function availablePort() {
  const reservation = http.createServer();
  const port = await listen(reservation);
  await new Promise(resolve => reservation.close(resolve));
  return port;
}

test('demo forwards annotated JSON once and serves the documented parent origin', { timeout: 15000 }, async () => {
  const requests = [];
  const upstream = http.createServer(async (req, res) => {
    let body = '';
    for await (const chunk of req) body += chunk;
    requests.push({ path: req.url, body });
    res.writeHead(200, { 'Content-Type': 'application/json' });
    res.end(JSON.stringify({ metadata: JSON.parse(body).metadata }));
  });
  const upstreamPort = await listen(upstream);
  const platformPort = await availablePort();
  const gatewayPort = await availablePort();
  const directory = await mkdtemp(join(tmpdir(), 'catsco-annotation-demo-'));
  const logPath = join(directory, 'sends.jsonl');
  const child = spawn(process.execPath, [new URL('./local-gateway-annotations-demo.mjs', import.meta.url).pathname], {
    env: {
      ...process.env,
      GATEWAY_ANNOTATIONS_PLATFORM_PORT: String(platformPort),
      GATEWAY_ANNOTATIONS_GATEWAY_PORT: String(gatewayPort),
      MOCK_CATS_PORT_UPSTREAM: String(upstreamPort),
      GATEWAY_ANNOTATIONS_LOG_PATH: logPath,
      GATEWAY_DEMO_PUBLIC_BASE: `http://127.0.0.1:${gatewayPort}`,
      GATEWAY_DEMO_HOST_ORIGIN: '',
    },
    stdio: ['ignore', 'pipe', 'pipe'],
  });
  let output = '';
  let errors = '';
  child.stderr.on('data', chunk => { errors += chunk; });
  try {
    await new Promise((resolve, reject) => {
      const timer = setTimeout(() => reject(new Error(`demo startup timeout: ${errors}`)), 5000);
      const handleData = chunk => {
        output += chunk;
        if (output.includes('fixture app:')) {
          clearTimeout(timer);
          child.stdout.off('data', handleData);
          resolve();
        }
      };
      child.stdout.on('data', handleData);
      child.once('error', error => { clearTimeout(timer); reject(error); });
      child.once('exit', code => { clearTimeout(timer); reject(new Error(`demo exited ${code}: ${errors}`)); });
    });
    const payload = {
      topic_id: 'p2p_7_201',
      content: 'Please update this target',
      metadata: { gateway_annotations: { app_id: 'saturday-demo', annotations: [{ body: 'Update title' }] } },
    };
    const serialized = JSON.stringify(payload);
    const response = await fetch(`http://127.0.0.1:${platformPort}/api/messages/send`, {
      method: 'POST', headers: { 'Content-Type': 'application/json' }, body: serialized,
      signal: AbortSignal.timeout(5000),
    });
    assert.equal(response.status, 200);
    assert.deepEqual((await response.json()).metadata, payload.metadata);
    assert.deepEqual(requests, [{ path: '/api/messages/send', body: serialized }]);
    const logs = (await readFile(logPath, 'utf8')).trim().split('\n').map(line => JSON.parse(line));
    assert.equal(logs.length, 1);
    assert.deepEqual(logs[0].gateway_annotations, payload.metadata.gateway_annotations);
    const fixture = await (await fetch(`http://127.0.0.1:${gatewayPort}/saturday-demo/`)).text();
    assert.ok(fixture.includes('data-catsco-parent-origins="[&quot;http://localhost:5173&quot;]"'));
    assert.ok(fixture.includes('src="/_catsco/runtime/annotations-v1.js"'));
    assert.ok(!fixture.includes('CatsCoAnnotations.create'));
    assert.ok(!fixture.includes('__CATSCO_HOST_ORIGIN__'));
    const base = `http://127.0.0.1:${platformPort}`;
    const headers = { 'Content-Type': 'application/json', Authorization: 'Bearer demo-a' };
    const launch = async topic => (await (await fetch(`${base}/api/artifacts/launch`, {
      method: 'POST', headers, body: JSON.stringify({ app: 'saturday-demo', topic_id: topic }),
    })).json()).open_binding;
    const a = await launch('topic-A'); const b = await launch('topic-B');
    assert.notEqual(a.open_ref, b.open_ref);
    const submit = async (binding, authorization = headers.Authorization) => fetch(`${base}/api/artifacts/annotations`, {
      method: 'POST', headers: { ...headers, Authorization: authorization },
      body: JSON.stringify({ open_ref: binding.open_ref, client_msg_id: 'demo-one', content: 'Review',
        gateway_annotations: { ...payload.metadata.gateway_annotations, agent_uid: 201 } }),
    });
    assert.equal((await submit(a)).status, 200);
    assert.equal(JSON.parse(requests.at(-1).body).topic_id, 'topic-A');
    assert.equal((await submit(b)).status, 200);
    assert.equal(JSON.parse(requests.at(-1).body).topic_id, 'topic-B');
    const count = requests.length;
    assert.equal((await submit(a, 'Bearer wrong')).status, 403);
    assert.equal(requests.length, count);
    assert.equal((await fetch(`${base}/api/artifacts/open-bindings/${a.open_ref}`, { method: 'DELETE', headers })).status, 204);
    assert.equal((await submit(a)).status, 403);
    assert.equal(requests.length, count);
  } finally {
    child.kill('SIGTERM');
    if (child.exitCode === null) await once(child, 'exit');
    await new Promise(resolve => upstream.close(resolve));
    await rm(directory, { recursive: true, force: true });
  }
});
