import { createServer as createHTTPServer } from 'node:http';
import { createServer as createViteServer } from 'vite';
import { artifactGatewayCatalogueProxy, artifactGatewayCatalogueRoute } from './artifact-gateway-proxy.mjs';

const listen = (server) => new Promise((resolve) => server.listen(0, '127.0.0.1', resolve));
const close = (server) => new Promise((resolve, reject) => server.close((error) => error ? reject(error) : resolve()));

test('dev gateway catalogue proxy preserves the agent filter and strips credentials in both directions', async () => {
  const received = [];
  const upstream = createHTTPServer((request, response) => {
    received.push({ url: request.url, cookie: request.headers.cookie, authorization: request.headers.authorization });
    response.setHeader('content-type', 'application/json');
    response.setHeader('set-cookie', 'upstream-session=must-not-reach-localhost; Path=/');
    response.end(JSON.stringify({ apps: [{ id: 'agent-574-app' }] }));
  });
  await listen(upstream);
  let vite;
  try {
    vite = await createViteServer({
      configFile: false,
      appType: 'custom',
      server: { host: '127.0.0.1', port: 0, proxy: {
        [artifactGatewayCatalogueRoute]: artifactGatewayCatalogueProxy(`http://127.0.0.1:${upstream.address().port}`),
      } },
    });
    await vite.listen();
    const base = `http://127.0.0.1:${vite.httpServer.address().port}`;
    const response = await fetch(`${base}/artifact-gateway/api/apps?agent=574`, {
      headers: { Cookie: 'platform-session=private', Authorization: 'Bearer platform-token' },
    });
    expect(response.status).toBe(200);
    expect(await response.json()).toEqual({ apps: [{ id: 'agent-574-app' }] });
    expect(response.headers.get('set-cookie')).toBeNull();
    expect(received).toEqual([{ url: '/api/apps?agent=574', cookie: undefined, authorization: undefined }]);
    expect((await fetch(`${base}/artifact-gateway/api/apps/other`)).status).toBe(404);
    expect((await fetch(`${base}/artifact-gateway/_gateway/me`)).status).toBe(404);
    expect(received).toHaveLength(1);
  } finally {
    if (vite) await vite.close();
    await close(upstream);
  }
});
