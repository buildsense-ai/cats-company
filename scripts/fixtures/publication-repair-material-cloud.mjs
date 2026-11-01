// Test-only peer: real Cloud intake/store/material handler and PostgreSQL/TLS.
// Upstream CatsCo grant is synthetic; this is not Runtime/model execution.
import assert from 'node:assert/strict';
import { createHash, randomUUID } from 'node:crypto';
import https from 'node:https';
import path from 'node:path';
import { pathToFileURL } from 'node:url';
import { createInterface } from 'node:readline';

assert.ok(process.env.SKILLHUB_REPAIR_MATERIAL_ROOT, 'Cloud checkout required');
assert.ok(process.env.SKILLHUB_TEST_DATABASE_URL, 'PostgreSQL required');
const load = file => import(pathToFileURL(path.join(process.env.SKILLHUB_REPAIR_MATERIAL_ROOT, file)));
const { loadConfig } = await load('packages/config/index.js');
const { createStore, closePools } = await load('packages/database/store.js');
const { createDelegatedPublicationIntake } = await load('packages/publication-delegation/intake.js');
const { createDelegatedPublicationControlStore } = await load('packages/publication-delegation/control-store.js');
const { validatePublishRequest } = await load('packages/publish-validation/index.js');
const { createPublicationRepairMaterialHandler } = await load('apps/api/publication-repair-material.js');
const { publicationPeerTLS } = await load('tests/helpers/publication-peer-tls.js');
const id = () => randomUUID().replaceAll('-', '');
const sha = data => createHash('sha256').update(data).digest('hex');
const issuer = 'https://catsco.invalid', secret = 'pubmaterial_' + 'a'.repeat(64);
const config = loadConfig({ databaseUrl: process.env.SKILLHUB_TEST_DATABASE_URL, nodeEnv: 'test',
  publicationTasksEnabled: true, publicationPublishingEnabled: true });
const store = createStore(config), tls = await publicationPeerTLS();
let owner, receipt, original, cancelled, handler, lose = false, requests = 0;
const server = https.createServer(tls, async (req, res) => {
  requests++;
  const end = res.end.bind(res);
  res.end = (...args) => {
    if (lose && res.statusCode === 200) { lose = false; res.destroy(); return res; }
    return end(...args);
  };
  try { if (!await handler(req, res)) { res.writeHead(404); res.end(); } }
  catch { res.destroy(); }
});
let cleaned = false;
async function cleanup() {
  if (cleaned) return;
  cleaned = true;
  server.closeAllConnections();
  if (server.listening) await new Promise(resolve => server.close(resolve));
  try {
    if (owner) {
      await store.query('DELETE FROM publication_delegations WHERE task_id IN (SELECT id FROM publication_tasks WHERE owner_user_id=$1)', [owner]);
      await store.query('DELETE FROM users WHERE id=$1', [owner]);
    }
  } finally { await closePools(); await tls.close(); }
}
process.once('SIGTERM', () => { cleanup().then(() => process.exit(0), () => process.exit(1)); });
const emit = data => process.stdout.write(JSON.stringify(data) + '\n');
try {
  await new Promise(resolve => server.listen(0, '127.0.0.1', resolve));
  const audience = `https://127.0.0.1:${server.address().port}`;
  const settings = { publicationRepairMaterial: { enabled: true, issuer, audience, peerSecret: secret, trustedProxyAddresses: [] } };
  handler = createPublicationRepairMaterialHandler(settings, store);
  const suffix = id(), actor = String(BigInt('0x' + suffix.slice(0, 12)) + 1000n);
  const identity = await store.claimCatsCoPublisherIdentity({ catsCoUid: actor, catsCoIssuer: issuer,
    namespace: 'material-pair-' + suffix, displayName: 'Material paired fixture', contact: '',
    internalEmail: suffix + '@example.invalid', passwordHash: 'fixture' });
  owner = identity.user.id;
  // Deliberately not sorted: report index and hash sort have different roles.
  // Non-BMP/U+E000 distinguish UTF-8 byte ordering from JS UTF-16 sorting;
  // U+2028/U+2029/& prove JSON.stringify vs Go escape fidelity in hashes.
  const originalFiles = [
    { path: '😀.txt', contentBase64: Buffer.from('Non-BMP text. 中文').toString('base64') },
    { path: '\ue000.txt', content: 'BMP private-use name.' },
    { path: 'SKILL.md', contentBase64: Buffer.from('---\nname: material-pair-fixture\n---\nPrivate candidate; no runtime description.').toString('base64') },
    { path: 'refs/&\u2028\u2029.txt', content: 'Examples <>&\r\n\twith literal � replacement and \\ud800 text.' }
  ];
  const raw = Buffer.from(JSON.stringify({ source: { type: 'files', files: originalFiles } }));
  const validation = validatePublishRequest(raw.toString());
  assert.equal(validation.status, 422);
  receipt = { schemaVersion: 1, entryRequestId: 'pubentry_' + id(), candidateRevision: 1, acceptanceEpoch: 1,
    requestHash: sha(raw), sourceSnapshotHash: validation.body.error.validation.candidateHash, requestBytes: raw.length,
    delegationId: 'pubgrant_' + id(), idempotencyKey: 'pubcandidate_' + id() };
  const accept = createDelegatedPublicationIntake({ enabled: true, config, store, issuer, audience,
    introspectionClient: { async checkUploadGrant(r) {
      return { issuer, audience, action: 'candidate.upload', actorCatsCoUid: actor, botUid: '999',
        entryRequestId: r.entryRequestId, candidateRevision: 1, authorityEpoch: r.authorityEpoch,
        requestHash: sha(r.rawBody), requestBytes: r.rawBody.length, sourceSnapshotHash: r.sourceSnapshotHash,
        idempotencyKey: r.idempotencyKey, delegationId: receipt.delegationId,
        observedAt: new Date().toISOString(), expiresAt: new Date(Date.now() + 90000).toISOString() };
    } } });
  const { association } = await accept({ rawBody: raw, sourceSnapshotHash: receipt.sourceSnapshotHash,
    entryRequestId: receipt.entryRequestId, token: 'pubupload_' + id() + id(),
    idempotencyKey: receipt.idempotencyKey, authorityEpoch: 1 });
  receipt.taskId = association.taskId; receipt.eventId = association.eventId;
  const task = async () => (await store.query('SELECT * FROM publication_tasks WHERE id=$1', [receipt.taskId])).rows[0];
  original = await task();
  emit({ issuer, audience, ca: tls.cert.toString(), receipt, raw: raw.toString('base64'), report: validation.body.error.validation });
  const lines = createInterface({ input: process.stdin, crlfDelay: Infinity });
  for await (const line of lines) {
    assert.ok(line.length < 256);
    const { command } = JSON.parse(line);
    switch (command) {
      case 'lose': lose = true; break;
      case 'restart': handler = createPublicationRepairMaterialHandler(settings, createStore(config)); break;
      case 'disable': await store.query("UPDATE publisher_profiles SET status='disabled' WHERE user_id=$1", [owner]); break;
      case 'enable': await store.query("UPDATE publisher_profiles SET status='active' WHERE user_id=$1", [owner]); break;
      case 'cancel':
        await createDelegatedPublicationControlStore({ enabled: true, store, issuer, audience }).cancel(receipt);
        cancelled = await task();
        break;
      case 'state': break;
      default: throw Error('unknown fixture command');
    }
    const current = await task();
    const unchanged = JSON.stringify(current) === JSON.stringify(original);
    const commits = (await store.query('SELECT count(*)::int AS n FROM publication_commits WHERE task_id=$1', [receipt.taskId])).rows[0].n;
    const permits = (await store.query('SELECT count(*)::int AS n FROM publication_delegated_permits WHERE task_id=$1', [receipt.taskId])).rows[0].n;
    emit({ command, requests, unchanged, commits, permits, state: current.state,
      cancelledUnchanged: cancelled !== undefined && JSON.stringify(current) === JSON.stringify(cancelled) });
  }
} finally { await cleanup(); }
