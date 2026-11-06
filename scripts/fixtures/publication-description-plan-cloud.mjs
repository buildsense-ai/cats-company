import https from 'node:https';
import path from 'node:path';
import fs from 'node:fs/promises';
import readline from 'node:readline';
import { randomUUID } from 'node:crypto';
import { pathToFileURL } from 'node:url';

const root = process.argv[2];
const moduleAt = relative => import(pathToFileURL(path.join(root, relative)).href);
const { default: pg } = await moduleAt('node_modules/pg/lib/index.js');
const { loadConfig } = await moduleAt('packages/config/index.js');
const { createStore, closePools } = await moduleAt('packages/database/store.js');
const { createPublicationDescriptionDerivationHandler } = await moduleAt('apps/api/publication-description-derivation.js');
const { descriptionCopyFixture } = await moduleAt('tests/helpers/description-copy.js');
const { delegatedFixture } = await moduleAt('tests/helpers/delegated-publication.js');
const { derivePublicationDescription } = await moduleAt('packages/publication-tasks/description-derivation.js');
const { publicationPeerTLS } = await moduleAt('tests/helpers/publication-peer-tls.js');
if (!process.env.SKILLHUB_TEST_DATABASE_URL) throw Error('Real PostgreSQL DSN is mandatory');
const admin = new pg.Client({ connectionString: process.env.SKILLHUB_TEST_DATABASE_URL });
await admin.connect();
const schema = 'description_peer_plan_' + randomUUID().replaceAll('-', '');
await admin.query(`CREATE SCHEMA "${schema}"`);
const tls = await publicationPeerTLS();
let handler, dropNext = false, intentHash = 'a'.repeat(64), authorizedAt = new Date().toISOString();
const server = https.createServer(tls, (req, res) => {
  if (dropNext) { dropNext = false; res.end = () => res.destroy(); }
  void handler(req, res).then(handled => { if (!handled) { res.writeHead(404); res.end(); } });
});
try {
  await admin.query(`SET search_path TO "${schema}"`);
  const migrations = path.join(root, 'infra/migrations');
  for (const name of (await fs.readdir(migrations)).filter(f => /^\d+_.+\.sql$/.test(f)).sort()) {
    await admin.query(await fs.readFile(path.join(migrations, name), 'utf8'));
  }
  await new Promise(resolve => server.listen(0, '127.0.0.1', resolve));
  const origin = `https://127.0.0.1:${server.address().port}`, issuer = 'https://cats.example';
  const url = new URL(process.env.SKILLHUB_TEST_DATABASE_URL); url.searchParams.set('options', '-c search_path=' + schema);
  const config = loadConfig({ databaseUrl: url.href, nodeEnv: 'test', publicationTasksEnabled: true, publicationPublishingEnabled: true,
    publicationDescriptionDerivationEnabled: true, publicationDescriptionPlanEnabled: true,
    publicationDescriptionCommitAdmissionEnabled: true, publicationDescriptionReleasePermitEnabled: true,
    publicationDescriptionReleaseStatusEnabled: true,
    publicationDescriptionDerivationIssuer: issuer, publicationDescriptionDerivationAudience: origin,
    publicationDescriptionDerivationPeerSecret: 'pubderive_' + 'a'.repeat(64), publicationDescriptionDerivationTrustedProxyAddresses: [] });
  const store = createStore(config), copy = descriptionCopyFixture();
  const f = await delegatedFixture(store, config, { issuer, audience: origin, rawBody: Buffer.from(copy.original.rawJson) });
  const source = await store.getPublicationDescriptionDerivationSource(f.receipt.taskId);
  const originalValidation = (source.validation_result ?? source.intake_result).body.error.validation;
  const derived = derivePublicationDescription({ ...copy.original, validation: originalValidation });
  if (derived.status !== 200) throw Error('Real retained-report derivation failed');
  const request = { ...copy.request, taskId: f.receipt.taskId, expectedReceipt: derived.derived.receipt,
    derivedBytes: Buffer.byteLength(derived.derived.rawJson) };
  handler = createPublicationDescriptionDerivationHandler(config, store, { releasePermitClient: {
    async authorize(request) {
      return { schemaVersion: 1, entryId: request.entryId, applicationId: request.applicationId,
        target: request.target, intentHash, permitId: 'pubdescpermit_' + 'b'.repeat(32), authorizedAt };
    }
  } });
  console.log(JSON.stringify({ origin, certificate: tls.cert.toString(), request, publicationReceipt: f.receipt,
    originalValidation, derivedRawJson: derived.derived.rawJson }));
  let applicationId;
  for await (const line of readline.createInterface({ input: process.stdin, crlfDelay: Infinity })) {
    const command = JSON.parse(line);
    if (command.action === 'bind' && /^pubdesc_[a-f0-9]{64}$/.test(command.applicationId) && !applicationId) {
      applicationId = command.applicationId;
      await store.retainPublicationDescriptionDerivation({ applicationId, taskId: f.receipt.taskId,
        proposal: request.proposal, derived: derived.derived });
    } else if (command.action === 'set-intent-hash' && /^[a-f0-9]{64}$/.test(command.intentHash) &&
      typeof command.authorizedAt === 'string' && /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d{1,9})?Z$/.test(command.authorizedAt)) {
      intentHash = command.intentHash; authorizedAt = command.authorizedAt;
    }
    else if (command.action === 'lose-next') dropNext = true;
    else if (command.action === 'cancel') await store.query("UPDATE publication_tasks SET state='cancelled',delegated_cancelled_at=now(),candidate=NULL,candidate_bytes=0 WHERE id=$1", [f.receipt.taskId]);
    else if (command.action === 'publisher') await store.query("UPDATE publisher_profiles SET status='suspended' WHERE id=$1", [f.profile.id]);
    else if (command.action === 'disable') handler = createPublicationDescriptionDerivationHandler({ ...config,
      publicationDescriptionDerivation: { ...config.publicationDescriptionDerivation, planEnabled: false } }, store);
    else if (command.action === 'stats') {
      const counts = {};
      for (const [key, table] of Object.entries({ plans: 'publication_description_release_plans', reservations: 'publication_version_reservations', commits: 'publication_commits', derivedCommits: 'publication_description_release_commits', jobs: 'jobs', originalPlans: 'publication_delegated_plans' })) {
        if (key === 'derivedCommits' && !(await store.query('SELECT to_regclass($1) AS table_name', [table])).rows[0].table_name) {
          counts[key] = 0;
          continue;
        }
        counts[key] = Number((await store.query(`SELECT count(*) AS count FROM ${table}`)).rows[0].count);
      }
      const current = await store.getPublicationDescriptionDerivationSource(f.receipt.taskId);
      console.log(JSON.stringify({ ok: true, ...counts, originalUnchanged: current?.candidate?.equals(f.input.rawBody) === true }));
      continue;
    } else throw Error('Unexpected fixture control');
    console.log(JSON.stringify({ ok: true }));
  }
} finally {
  server.closeAllConnections();
  if (server.listening) await new Promise(resolve => server.close(resolve));
  await closePools(); await admin.query(`DROP SCHEMA "${schema}" CASCADE`); await admin.end(); await tls.close();
}
