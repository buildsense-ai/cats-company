import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import net from 'node:net';
import test from 'node:test';
import { fileURLToPath } from 'node:url';

const showcaseScript = fileURLToPath(new URL('./local-chat-showcase.mjs', import.meta.url));

async function availablePort() {
  const server = net.createServer();
  await new Promise((resolve, reject) => {
    server.once('error', reject);
    server.listen(0, '127.0.0.1', resolve);
  });
  const { port } = server.address();
  await new Promise((resolve, reject) => server.close((error) => error ? reject(error) : resolve()));
  return port;
}

async function waitForServer(baseURL) {
  for (let attempt = 0; attempt < 60; attempt += 1) {
    try {
      const response = await fetch(`${baseURL}/__mock/state`);
      if (response.ok) return;
    } catch {
      // The child process may still be binding its port.
    }
    await new Promise((resolve) => setTimeout(resolve, 50));
  }
  throw new Error('showcase mock server did not become ready');
}

test('showcase mock serves the Agent quota endpoint without console-noisy 404s', async (t) => {
  const port = await availablePort();
  const baseURL = `http://127.0.0.1:${port}`;
  const server = spawn(process.execPath, [showcaseScript], {
    env: {
      ...process.env,
      MOCK_CATS_PORT: String(port),
      MOCK_CATS_SCENARIO: 'showcase',
      MOCK_CATS_SHOWCASE_USERNAME: 'quota-reviewer',
      MOCK_CATS_SHOWCASE_PASSWORD: 'demo123456',
    },
    stdio: 'ignore',
  });

  t.after(() => {
    if (!server.killed) server.kill();
  });

  await waitForServer(baseURL);

  const loginResponse = await fetch(`${baseURL}/api/auth/login`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ account: 'quota-reviewer', password: 'demo123456' }),
  });
  assert.equal(loginResponse.status, 200);
  const login = await loginResponse.json();
  const headers = { Authorization: `Bearer ${login.token}` };

  const agentsResponse = await fetch(`${baseURL}/api/agents`, { headers });
  assert.equal(agentsResponse.status, 200);
  const { agents } = await agentsResponse.json();
  assert.ok(agents.length > 0);

  const quotaResponse = await fetch(`${baseURL}/api/agents/quota?uid=${agents[0].uid}`, { headers });
  assert.equal(quotaResponse.status, 200);
  assert.deepEqual(await quotaResponse.json(), {
    configured: false,
    shared: true,
  });

  const missingAgentResponse = await fetch(`${baseURL}/api/agents/quota?uid=999999`, { headers });
  assert.equal(missingAgentResponse.status, 404);
});
