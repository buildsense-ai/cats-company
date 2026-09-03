// Local demo server for the agent cloud-artifacts panel (PR #376/#388/#389 UX).
//
// Run the onboarding mock on 6062, this composite on 6061, then vite:
//   MOCK_CATS_SCENARIO=showcase MOCK_CATS_PORT=6062 node scripts/local-onboarding-mock-server.mjs
//   node scripts/local-artifact-tags-demo.mjs
//   cd webapp && pnpm start
//
// Everything the app needs is forwarded to the onboarding mock, except the
// artifact management + tag endpoints, which are served from memory with the
// same lifecycle semantics as the real server (purge on delete, 404 unknown
// IDs, owner view).
import http from 'node:http';
import { URL } from 'node:url';

const port = Number(process.env.ARTIFACT_DEMO_PORT || 6061);
const upstreamPort = Number(process.env.MOCK_CATS_PORT_UPSTREAM || 6062);
const upstreamOrigin = `http://127.0.0.1:${upstreamPort}`;

const MANAGEMENT_CONTRACT = 'cloud-artifacts.management-list.v1';
const MAX_TAGS_PER_ARTIFACT = 12;
const MAX_TAG_RUNES = 32;

// uid -> Map<artifactId, artifact>; tags: uid -> Map<artifactId, string[]>
const artifactsByAgent = new Map();
const tagsByAgent = new Map();

function nowISO(offsetMinutes = 0) {
  return new Date(Date.now() - offsetMinutes * 60_000).toISOString();
}

function seedArtifacts(uid) {
  if (artifactsByAgent.has(uid)) return;
  const artifacts = new Map();
  const seed = [
    ['classroom-game', '课堂小游戏', 'html', 8, ['游戏', '演示']],
    ['lesson-poster', '课堂海报', 'image', 60 * 26, ['素材']],
    ['reading-notes', '读书笔记', 'html', 60 * 50, []],
  ];
  for (const [id, title, kind, ageMinutes, tags] of seed) {
    artifacts.set(id, {
      id,
      title,
      kind,
      url: `https://app.catsco.cc/by-agent/${uid}/${id}/latest/`,
      status: 'active',
      created_at: nowISO(ageMinutes + 5),
      updated_at: nowISO(ageMinutes),
      uploader_name: 'UI Reviewer',
      uploader_uid: String(uid + 9000),
      uploaded_by_me: true,
      can_delete: true,
      can_restore: false,
    });
    tagsByAgent.set(uid, (tagsByAgent.get(uid) ?? new Map()).set(id, [...tags]));
  }
  artifactsByAgent.set(uid, artifacts);
}

function tagsFor(uid) {
  seedArtifacts(uid);
  if (!tagsByAgent.has(uid)) tagsByAgent.set(uid, new Map());
  return tagsByAgent.get(uid);
}

function artifactsFor(uid) {
  seedArtifacts(uid);
  return artifactsByAgent.get(uid);
}

function normalizeTags(values) {
  const out = [];
  const seen = new Set();
  for (const value of Array.isArray(values) ? values : []) {
    if (typeof value !== 'string') continue;
    const tag = value.split(/\s+/).filter(Boolean).join(' ');
    if (!tag) continue;
    if ([...tag].length > MAX_TAG_RUNES) return { error: 'artifact_tag_invalid' };
    if (seen.has(tag)) continue;
    seen.add(tag);
    out.push(tag);
  }
  if (out.length > MAX_TAGS_PER_ARTIFACT) return { error: 'artifact_tag_limit_exceeded' };
  return { tags: out };
}

function sendJSON(res, status, payload) {
  const body = JSON.stringify(payload);
  res.writeHead(status, {
    'Content-Type': 'application/json; charset=utf-8',
    'Cache-Control': 'no-store',
  });
  res.end(body);
  return true;
}

function handleArtifactAPI(req, res, url) {
  const match = url.pathname.match(/^\/api\/agents\/(\d+)\/artifacts$/);
  if (match && req.method === 'GET') {
    const uid = Number(match[1]);
    const status = url.searchParams.get('status') || 'active';
    const artifacts = [...artifactsFor(uid).values()]
      .filter((a) => (status === 'deleted' ? a.status === 'deleted' : a.status === 'active'))
      .map((a) => ({ ...a, tags: tagsFor(uid).get(a.id) ?? [] }));
    return sendJSON(res, 200, {
      contract_version: MANAGEMENT_CONTRACT,
      status,
      count: artifacts.length,
      artifacts,
      viewer_relation: 'owner',
      visibility: 'agent_users',
      can_publish: false,
      publish_mode: 'none',
    });
  }

  const tagCollection = url.pathname.match(/^\/api\/agents\/(\d+)\/artifacts\/tags$/);
  if (tagCollection && req.method === 'GET') {
    const uid = Number(tagCollection[1]);
    const totals = new Map();
    for (const tags of tagsFor(uid).values()) {
      for (const tag of tags) totals.set(tag, (totals.get(tag) ?? 0) + 1);
    }
    const tags = [...totals.entries()]
      .sort((a, b) => b[1] - a[1] || a[0].localeCompare(b[0]))
      .map(([tag, count]) => ({ tag, count }));
    return sendJSON(res, 200, { tags });
  }

  const tagReplace = url.pathname.match(/^\/api\/agents\/(\d+)\/artifacts\/([^/]+)\/tags$/);
  if (tagReplace && req.method === 'PUT') {
    const uid = Number(tagReplace[1]);
    const artifactID = decodeURIComponent(tagReplace[2]);
    if (!artifactsFor(uid).has(artifactID) || artifactsFor(uid).get(artifactID).status !== 'active') {
      return sendJSON(res, 404, { error: '产物不存在', code: 'artifact_not_found' });
    }
    let body = '';
    req.on('data', (chunk) => { body += chunk; });
    req.on('end', () => {
      let parsed;
      try { parsed = JSON.parse(body || '{}'); } catch { parsed = undefined; }
      if (!parsed || typeof parsed !== 'object' || !Array.isArray(parsed.tags)) {
        return sendJSON(res, 400, { error: '标签请求无效', code: 'artifact_tag_request_invalid' });
      }
      const result = normalizeTags(parsed.tags);
      if (result.error) {
        const messages = {
          artifact_tag_invalid: '标签格式无效',
          artifact_tag_limit_exceeded: '标签数量超出限制',
        };
        return sendJSON(res, 400, { error: messages[result.error], code: result.error });
      }
      tagsFor(uid).set(artifactID, result.tags);
      return sendJSON(res, 200, { tags: [...result.tags] });
    });
    return true;
  }

  const tagSystemRoute = url.pathname.match(/^\/api\/agents\/(\d+)\/artifacts\/tags\/(.+)$/);
  if (tagSystemRoute && req.method === 'PUT') {
    const uid = Number(tagSystemRoute[1]);
    const oldTag = decodeURIComponent(tagSystemRoute[2]);
    let body = '';
    req.on('data', (chunk) => { body += chunk; });
    req.on('end', () => {
      let parsed;
      try { parsed = JSON.parse(body || '{}'); } catch { parsed = undefined; }
      const newTag = typeof parsed?.tag === 'string' ? parsed.tag.split(/\s+/).filter(Boolean).join(' ') : '';
      const tags = tagsFor(uid);
      const carrying = (tag) => {
        let n = 0;
        for (const set of tags.values()) if (set.includes(tag)) n++;
        return n;
      };
      if (!newTag) {
        return sendJSON(res, 400, { error: '标签格式无效', code: 'artifact_tag_invalid' });
      }
      if (carrying(oldTag) === 0) {
        return sendJSON(res, 404, { error: '标签不存在', code: 'artifact_tag_not_found' });
      }
      if (newTag === oldTag) {
        return sendJSON(res, 200, { ok: true, renamed: carrying(newTag) });
      }
      for (const [artifactID, set] of tags) {
        if (!set.includes(oldTag)) continue;
        tags.set(artifactID, set.includes(newTag) ? set.filter((item) => item !== oldTag) : set.map((item) => (item === oldTag ? newTag : item)));
      }
      const carried = carrying(newTag);
      if (carried === 0) {
        return sendJSON(res, 404, { error: '标签不存在', code: 'artifact_tag_not_found' });
      }
      return sendJSON(res, 200, { ok: true, renamed: carried });
    });
    return true;
  }

  const tagDeleteEverywhere = url.pathname.match(/^\/api\/agents\/(\d+)\/artifacts\/tags\/(.+)$/);
  if (tagDeleteEverywhere && req.method === 'DELETE') {
    const uid = Number(tagDeleteEverywhere[1]);
    const tag = decodeURIComponent(tagDeleteEverywhere[2]);
    let removed = 0;
    for (const [artifactID, tags] of tagsFor(uid)) {
      if (tags.includes(tag)) {
        tagsFor(uid).set(artifactID, tags.filter((item) => item !== tag));
        removed++;
      }
    }
    if (removed === 0) {
      return sendJSON(res, 404, { error: '标签不存在', code: 'artifact_tag_not_found' });
    }
    return sendJSON(res, 200, { ok: true, removed });
  }

  const tagDelete = url.pathname.match(/^\/api\/agents\/(\d+)\/artifacts\/([^/]+)\/tags\/(.+)$/);
  if (tagDelete && req.method === 'DELETE') {
    const uid = Number(tagDelete[1]);
    const artifactID = decodeURIComponent(tagDelete[2]);
    const tag = decodeURIComponent(tagDelete[3]);
    const tags = tagsFor(uid).get(artifactID) ?? [];
    const next = tags.filter((item) => item !== tag);
    if (next.length === tags.length) {
      return sendJSON(res, 404, { error: '标签不存在', code: 'artifact_tag_not_found' });
    }
    tagsFor(uid).set(artifactID, next);
    return sendJSON(res, 200, { ok: true });
  }

  const artifactDelete = url.pathname.match(/^\/api\/agents\/(\d+)\/artifacts\/([^/]+)$/);
  if (artifactDelete && req.method === 'DELETE') {
    const uid = Number(artifactDelete[1]);
    const artifactID = decodeURIComponent(artifactDelete[2]);
    const artifact = artifactsFor(uid).get(artifactID);
    if (!artifact || artifact.status === 'deleted') {
      return sendJSON(res, 404, { error: '产物不存在', code: 'artifact_not_found' });
    }
    artifact.status = 'deleted';
    artifact.deleted_at = nowISO(0);
    tagsFor(uid).delete(artifactID); // purge-on-delete lifecycle
    return sendJSON(res, 200, { ok: true, artifact: { ...artifact } });
  }

  const artifactRestore = url.pathname.match(/^\/api\/agents\/(\d+)\/artifacts\/([^/]+)\/restore$/);
  if (artifactRestore && req.method === 'POST') {
    const uid = Number(artifactRestore[1]);
    const artifactID = decodeURIComponent(artifactRestore[2]);
    const artifact = artifactsFor(uid).get(artifactID);
    if (!artifact) {
      return sendJSON(res, 404, { error: '产物不存在', code: 'artifact_not_found' });
    }
    artifact.status = 'active';
    delete artifact.deleted_at;
    return sendJSON(res, 200, { ok: true, artifact: { ...artifact } });
  }

  return null;
}

function proxy(req, res) {
  const options = { hostname: '127.0.0.1', port: upstreamPort, path: req.url, method: req.method, headers: req.headers };
  const upstream = http.request(options, (up) => {
    res.writeHead(up.statusCode, up.headers);
    up.pipe(res);
  });
  upstream.on('error', () => {
    res.writeHead(502, { 'Content-Type': 'application/json' });
    res.end(JSON.stringify({ error: 'mock upstream unavailable' }));
  });
  req.pipe(upstream);
}

const server = http.createServer((req, res) => {
  const url = new URL(req.url, `http://127.0.0.1:${port}`);
  if (url.pathname.startsWith('/api/agents/') && url.pathname.includes('/artifacts')) {
    if (handleArtifactAPI(req, res, url)) return;
  }
  proxy(req, res);
});

server.on('upgrade', (req, socket, head) => {
  const options = { hostname: '127.0.0.1', port: upstreamPort, path: req.url, headers: req.headers };
  const upstream = http.request(options);
  upstream.on('upgrade', (upRes, upSocket, upHead) => {
    socket.write(
      `HTTP/1.1 101 Switching Protocols\r\n` +
      Object.entries(upRes.headers).map(([k, v]) => `${k}: ${v}`).join('\r\n') +
      '\r\n\r\n',
    );
    upSocket.pipe(socket);
    socket.pipe(upSocket);
  });
  upstream.on('error', () => socket.destroy());
  upstream.end();
});

server.listen({ port, host: '::', ipv6Only: false }, () => {
  console.log(`[demo] artifact tags demo server on http://127.0.0.1:${port} (upstream ${upstreamOrigin})`);
});
