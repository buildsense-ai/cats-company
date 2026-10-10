// Local demo for gateway app annotations (design doc: docs/gateway-artifact-annotations.md).
//
// Two cross-origin origins, one platform mirror:
//   6062  onboarding mock          (MOCK_CATS_SCENARIO=showcase, run separately)
//   6063  platform mirror          (this file: forwards to the mock, records annotation sends)
//   6066  local artifact gateway   (this file: app registry + one-time launch codes + fixture app)
//   5173  webapp (explicit localhost origin and port, matching the SDK parentOrigin)
//
//   MOCK_CATS_SCENARIO=showcase MOCK_CATS_PORT=6062 node scripts/local-onboarding-mock-server.mjs
//   node scripts/local-gateway-annotations-demo.mjs
//   cd webapp && VITE_BACKEND_TARGET=http://127.0.0.1:6063 VITE_ARTIFACT_GATEWAY_BASE=http://127.0.0.1:6066 pnpm start --host localhost --port 5173 --strictPort
//
// The webapp then loads the showcase account (ui-reviewer / demo123456). Open a
// bot conversation, open the 云文件 sidebar → 应用 tab → Saturday 演示应用,
// enable a annotation mode, capture a target in the cross-origin iframe, write
// the comment in the composer, and send. POST /api/artifacts/annotations is recorded in
// demo-annotations.log.jsonl next to this script.
import http from 'node:http';
import { readFileSync, existsSync, mkdirSync, appendFileSync } from 'node:fs';
import { randomUUID } from 'node:crypto';

const PLATFORM_PORT = Number(process.env.GATEWAY_ANNOTATIONS_PLATFORM_PORT || 6063);
const GATEWAY_PORT = Number(process.env.GATEWAY_ANNOTATIONS_GATEWAY_PORT || 6066);
const MOCK_PORT = Number(process.env.MOCK_CATS_PORT_UPSTREAM || 6062);
const MOCK_ORIGIN = `http://127.0.0.1:${MOCK_PORT}`;

const AGENT_UID = Number(process.env.GATEWAY_DEMO_AGENT_UID || 201);
const APP_ID = process.env.GATEWAY_DEMO_APP_ID || 'saturday-demo';
const PUBLIC_BASE = process.env.GATEWAY_DEMO_PUBLIC_BASE || `http://127.0.0.1:${GATEWAY_PORT}`;
// The fixture's SDK validates the host page's exact origin in the connect
// handshake; this is the webapp dev origin, not the gateway origin.
const HOST_ORIGIN = process.env.GATEWAY_DEMO_HOST_ORIGIN || 'http://localhost:5173';
const LOG_PATH = process.env.GATEWAY_ANNOTATIONS_LOG_PATH || new URL('../demo-annotations.log.jsonl', import.meta.url);

const LAUNCH_CODES = new Map(); // code -> { agent, topic }
const OPEN_BINDINGS = new Map(); // DEMO ONLY, auth header + canonical topic
const REAL_BACKEND = String(process.env.GATEWAY_ANNOTATIONS_REAL_BACKEND || '').replace(/\/+$/, '');
// With REAL_BACKEND all platform routes pass through to A's actual server.
// Without it this is only a UI fixture, never backend security/E2E evidence.

if (!existsSync(LOG_PATH)) mkdirSync(new URL('.', import.meta.url), { recursive: true });

function appURL() { return `${PUBLIC_BASE}/${APP_ID}/`; }

function readBody(req) {
  return new Promise((resolve) => {
    let body = '';
    req.on('data', (chunk) => { body += chunk; });
    req.on('end', () => resolve(body));
  });
}

function sendJSON(res, status, payload, extraHeaders = {}) {
  const body = JSON.stringify(payload);
  res.writeHead(status, {
    'Content-Type': 'application/json; charset=utf-8',
    'Cache-Control': 'no-store',
    ...extraHeaders,
  });
  res.end(body);
}

function corsHeaders(req) {
  const origin = req.headers.origin || '';
  return {
    'Access-Control-Allow-Origin': origin,
    'Access-Control-Allow-Methods': 'GET, POST, DELETE, OPTIONS',
    'Access-Control-Allow-Headers': 'Content-Type, Authorization',
    'Access-Control-Allow-Credentials': 'true',
    Vary: 'Origin',
  };
}

function recordLog(entry) {
  try {
    appendFileSync(LOG_PATH, `${JSON.stringify({ ts: new Date().toISOString(), ...entry })}\n`);
  } catch { /* demo only */ }
}

// ---------------------------------------------------------------------
// Fixture gateway-only app. Cross-origin from the webapp; the annotation
// SDK script is served verbatim from webapp/public/catsco-annotations.js.
const FIXTURE_HTML_PATH = new URL('./local-gateway-annotations-fixture.html', import.meta.url);

function fixtureHTML() {
  const origins = JSON.stringify([HOST_ORIGIN]).replace(/&/g, '&amp;').replace(/"/g, '&quot;').replace(/</g, '&lt;');
  const runtime = `<script src="/_catsco/runtime/annotations-v1.js" data-catsco-parent-origins="${origins}"></script>`;
  if (existsSync(FIXTURE_HTML_PATH)) {
    // The fixture's SDK is pointed at the exact platform origin; the shared
    // SDK refuses any handshake from another parent origin.
    return readFileSync(FIXTURE_HTML_PATH, 'utf8')
      .replace('</head>', `${runtime}</head>`);
  }
  return `<!doctype html>
<html lang="zh-CN"><head><meta charset="utf-8"><title>${APP_ID}</title>
<style>body{font:14px system-ui;padding:24px}button,input{display:block;margin:8px 0}</style></head>
<body>
<div id="app">
  <h1>GKB 物料看板</h1>
  <input id="token-input" type="password" placeholder="访问令牌（敏感输入框，SDK 不可标注）" />
  <button id="submit-btn">发布物料</button>
  <section id="inventory">当前库位：A-12-3</section>
</div>
${runtime}
</body></html>`;
}

function handleGateway(req, res, url) {
  if (req.method === 'OPTIONS') {
    res.writeHead(204, corsHeaders(req));
    res.end();
    return;
  }
  if (url.pathname === '/api/apps' && req.method === 'GET') {
    sendJSON(res, 200, {
      apps: [{
        id: APP_ID,
        title: 'GKB 物料看板',
        agent: String(AGENT_UID),
        remote_port: 41001,
        url: appURL(),
        status: 'ready',
        updated_at: '2026-10-06T09:00:00.000Z',
      }],
    }, corsHeaders(req));
    return;
  }
  if (url.pathname === '/_gateway/apps' && req.method === 'GET') {
    // The shared-token control surface the platform's publish/owner checks use.
    if (req.headers.authorization !== 'Bearer demo-gateway-token') {
      sendJSON(res, 401, { error: 'unauthorized' });
      return;
    }
    sendJSON(res, 200, {
      apps: [{ id: APP_ID, title: 'GKB 物料看板', agent: String(AGENT_UID), url: appURL() }],
    });
    return;
  }
  const launchMatch = url.pathname.match(/^\/_launch\/([A-Za-z0-9]+)(\/?.*)$/);
  if (launchMatch && req.method === 'GET') {
    const entry = LAUNCH_CODES.get(launchMatch[1]);
    if (!entry) { sendJSON(res, 404, { error: 'launch code expired' }); return; }
    // Cookie set on the gateway origin via the redirect path.
    res.writeHead(302, {
      Location: `/${APP_ID}/${launchMatch[2] || ''}`,
      'Set-Cookie': `catsco_demo_viewer=guest-${Date.now()}; Path=/; SameSite=Lax`,
    });
    res.end();
    return;
  }
  if (url.pathname === `/${APP_ID}/` || url.pathname === `/${APP_ID}` || url.pathname.startsWith(`/${APP_ID}/`)) {
    res.writeHead(200, { 'Content-Type': 'text/html; charset=utf-8', 'Cache-Control': 'no-store' });
    res.end(fixtureHTML());
    return;
  }
  if (url.pathname === '/catsco-annotations.js' || url.pathname === '/_catsco/runtime/annotations-v1.js') {
    const webappPath = new URL('../webapp/public/catsco-annotations.js', import.meta.url);
    if (!existsSync(webappPath)) { sendJSON(res, 500, { error: 'SDK missing' }); return; }
    res.writeHead(200, { 'Content-Type': 'text/javascript; charset=utf-8', 'Cache-Control': 'no-store' });
    res.end(readFileSync(webappPath));
    return;
  }
  sendJSON(res, 404, { error: 'not_found' });
}

// ---------------------------------------------------------------------
// Platform mirror: same-origin /api traffic from the webapp, forwarded to the
// onboarding mock except for the two artifact-app routes this demo owns.
async function handlePlatform(req, res, url) {
  // One-time launch codes come from the platform (same shape as the real one).
  if (!REAL_BACKEND && url.pathname === '/api/artifacts/launch' && req.method === 'POST') {
    const payload = JSON.parse((await readBody(req)) || '{}');
    if (!req.headers.authorization || payload.app !== APP_ID) {
      sendJSON(res, 403, { error: 'demo launch denied' }); return;
    }
    const code = randomUUID().replace(/-/g, '').slice(0, 16);
    const binding = payload.topic_id ? {
      contract_version: 'catsco.artifact-open-binding.v1', open_ref: `aob_${randomUUID().replace(/-/g, '')}`,
      topic_id: payload.topic_id, agent_uid: AGENT_UID, app_id: APP_ID,
      app_origin: new URL(PUBLIC_BASE).origin, expires_at: new Date(Date.now() + 3600_000).toISOString(),
    } : null;
    if (binding) OPEN_BINDINGS.set(binding.open_ref, { binding, auth: req.headers.authorization });
    LAUNCH_CODES.set(code, { agent: payload.app, topic: payload.topic_id || '' });
    setTimeout(() => LAUNCH_CODES.delete(code), 60_000);
    sendJSON(res, 200, {
      app_id: payload.app,
      launch_url: `${PUBLIC_BASE}/_launch/${code}`,
      viewer_role: 'member',
      ...(binding ? { open_binding: binding } : {}),
    });
    recordLog({ kind: 'launch', app: payload.app, topic: payload.topic_id || '' });
    return;
  }

  // Consume the request stream once: the same bytes are used for logging and
  // forwarding. Reading IncomingMessage again after its end event would hang.
  let body = ['GET', 'HEAD'].includes(req.method) ? undefined : await readBody(req);
  const revoke = url.pathname.match(/^\/api\/artifacts\/open-bindings\/(aob_[A-Za-z0-9_-]+)$/);
  if (!REAL_BACKEND && revoke && req.method === 'DELETE') {
    const owned = OPEN_BINDINGS.get(revoke[1]);
    if (owned && owned.auth !== req.headers.authorization) { sendJSON(res, 403, { error: 'wrong session' }); return; }
    OPEN_BINDINGS.delete(revoke[1]); res.writeHead(204); res.end(); return;
  }
  let forwardPath = url.pathname;
  if (url.pathname === '/api/artifacts/annotations' && req.method === 'POST') {
    const parsed = JSON.parse(body || '{}');
    if (!REAL_BACKEND) {
      const owned = OPEN_BINDINGS.get(parsed.open_ref);
      if (!owned || owned.auth !== req.headers.authorization || Date.parse(owned.binding.expires_at) <= Date.now()
        || parsed.gateway_annotations?.agent_uid !== owned.binding.agent_uid
        || parsed.gateway_annotations?.app_id !== owned.binding.app_id) {
        sendJSON(res, 403, { error: 'open binding invalid; reopen' }); return;
      }
      forwardPath = '/api/messages/send';
      body = JSON.stringify({ ...parsed, topic_id: owned.binding.topic_id, type: 'text',
        metadata: { gateway_annotations: parsed.gateway_annotations } });
      recordLog({ kind: 'bound-message', topic: owned.binding.topic_id,
        content: parsed.content, gateway_annotations: parsed.gateway_annotations });
    } else recordLog({ kind: 'bound-request-forwarded', gateway_annotations: parsed.gateway_annotations });
  }
  if (url.pathname === '/api/messages/send' && req.method === 'POST') {
    try {
      const parsed = JSON.parse(body || '{}');
      recordLog({
        kind: 'message',
        topic: parsed.topic_id || '',
        content: parsed.content || '',
        gateway_annotations: parsed.metadata?.gateway_annotations || null,
      });
    } catch { /* demo only */ }
  }

  // Everything else proxies to the onboarding mock using the captured body.
  const forwardHeaders = { ...req.headers };
  delete forwardHeaders.host;
  delete forwardHeaders['content-length'];
  const upstream = await fetch((REAL_BACKEND || MOCK_ORIGIN) + forwardPath + url.search, {
    method: req.method,
    headers: forwardHeaders,
    body,
  }).catch((error) => ({ status: 502, ok: false, headers: new Map(), text: () => Promise.resolve(String(error)) }));
  const headers = {};
  if (typeof upstream.headers?.forEach === 'function') {
    upstream.headers.forEach((value, key) => { headers[key] = value; });
  }
  res.writeHead(upstream.status || 502, headers);
  res.end(await (typeof upstream.text === 'function' ? upstream.text() : Promise.resolve('')));
}

const platformServer = http.createServer((req, res) => {
  const url = new URL(req.url, `http://127.0.0.1:${PLATFORM_PORT}`);
  handlePlatform(req, res, url).catch((error) => {
    sendJSON(res, 500, { error: String(error) });
  });
});

const gatewayServer = http.createServer((req, res) => {
  const url = new URL(req.url, `http://127.0.0.1:${GATEWAY_PORT}`);
  handleGateway(req, res, url);
});

platformServer.listen(PLATFORM_PORT, '127.0.0.1', () => {
  console.log(`[gateway-annotations-demo] platform mirror on http://127.0.0.1:${PLATFORM_PORT} -> ${MOCK_ORIGIN}`);
});
gatewayServer.listen(GATEWAY_PORT, '127.0.0.1', () => {
  console.log(`[gateway-annotations-demo] local artifact gateway on ${PUBLIC_BASE}`);
  console.log(`[gateway-annotations-demo] fixture app: ${appURL()} (agent ${AGENT_UID})`);
  console.log(`[gateway-annotations-demo] annotation sends recorded in ${LOG_PATH}`);
});
