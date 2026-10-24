// Isolated contract bridge: imports the pinned XiaoBa implementation, never a
// deployed account. stdout contains protocol JSON only, not source bytes.
import fs from 'node:fs';
import path from 'node:path';
import readline from 'node:readline';
import assert from 'node:assert/strict';
import { createRequire } from 'node:module';

const [xiaoba, root, mode] = process.argv.slice(2);
assert.equal(fs.readdirSync(root).length, 0, 'test-owned empty directory required');
const require = createRequire(path.join(xiaoba, 'package.json'));
require('tsx/cjs');
const { createCatsCoLocalConfigService } = require(path.join(xiaoba, 'src/catscompany/local-config.ts'));
const { BotSkillWorkspaceService } = require(path.join(xiaoba, 'src/bot-skills/workspace.ts'));
const { SkillHubThinRpcHandler, SKILLHUB_THIN_RPC_TOOLS } = require(path.join(xiaoba, 'src/catscompany/skillhub-rpc.ts'));
const { PUBLICATION_CAPTURE, PUBLICATION_REPAIR_SOURCE } = require(path.join(xiaoba, 'src/skillhub/publication-capture.ts'));
const source = path.join(root, 'skills', 'paired-demo');
fs.mkdirSync(source, { recursive: true });
fs.writeFileSync(path.join(source, 'SKILL.md'), mode === 'malformed'
  ? '---\nname: [broken YAML\n---\n'
  : '---\nname: paired-demo\ndescription: Isolated repair fixture\n---\n# Demo');
fs.writeFileSync(path.join(source, 'run.cjs'), 'require("fs").writeFileSync(__dirname + "/EXECUTED", "bad")');
new BotSkillWorkspaceService(root, path.join(root, 'skills')).activate('42');
const config = createCatsCoLocalConfigService({ runtimeRoot: root });
config.save({ version: 1,
  currentBot: { uid: '42', apiKey: 'paired-test-only', boundByUserUid: '7' },
  account: { uid: '7', token: 'paired-test-only', username: 'paired-owner' },
  device: { bodyId: 'body-42', installationId: 'install-42' },
});
const options = { runtimeRoot: root, publicationProbeEnabled: true, publicationCaptureEnabled: true, publicationRepairSourceEnabled: true };
let handler = new SkillHubThinRpcHandler(options);
const listing = await handler.execute({ type: 'request', request_id: 'paired-list',
  target_owner_user_id: 'usr7', target_device_id: 'install-42',
  tool_name: SKILLHUB_THIN_RPC_TOOLS.workspace, expires_at: Date.now() + 30_000,
  payload: { bot_uid: '42' },
});
const item = listing.skills[0];
assert.ok(item?.publication_source);
assert.ok(handler.supports(PUBLICATION_REPAIR_SOURCE));
const send = value => process.stdout.write(`${JSON.stringify(value)}\n`);
send({ source: item.publication_source });
const statePath = path.join(root, 'data', 'bot-skills', 'active.json');
const originalState = fs.readFileSync(statePath, 'utf8');
const files = dir => fs.readdirSync(dir).sort().map(name => {
  const file = path.join(dir, name);
  return [name, fs.statSync(file).isDirectory() ? files(file) : fs.readFileSync(file).toString('base64')];
});
for await (const line of readline.createInterface({ input: process.stdin, crlfDelay: Infinity })) {
  const input = JSON.parse(line);
  if (input.action === 'rpc') {
    const before = files(path.join(root, 'skills'));
    const request = input.request;
    const reply = { type: 'result', request_id: request.request_id, tool_name: request.tool_name,
      target_owner_user_id: request.target_owner_user_id, target_device_id: request.target_device_id, device_id: request.device_id };
    try { reply.result = await handler.execute(request); }
    catch (error) { reply.error = { code: error.code, message: error.message }; }
    assert.deepEqual(files(path.join(root, 'skills')), before, 'RPC changed source files');
    if (reply.result) assert.equal(Object.keys(reply.result).length, request.tool_name === PUBLICATION_CAPTURE ? 6 : 7);
    send({ reply });
    continue;
  }
  switch (input.action) {
    case 'change': fs.appendFileSync(path.join(source, 'SKILL.md'), '\nchanged'); break;
    case 'remove': fs.renameSync(source, path.join(root, 'skills', 'renamed')); break;
    case 'disable': handler = new SkillHubThinRpcHandler({ ...options, publicationRepairSourceEnabled: false }); break;
    case 'owner': config.save({ ...config.load(), currentBot: { uid: '42', apiKey: 'paired-test-only', boundByUserUid: '8' } }); break;
    case 'switching': fs.writeFileSync(statePath, JSON.stringify({ ...JSON.parse(originalState), activeBotId: '43' })); break;
    case 'restart': handler = new SkillHubThinRpcHandler(options); break;
    case 'recover': fs.writeFileSync(statePath, originalState); handler = new SkillHubThinRpcHandler(options); break;
    default: throw new Error('Unknown action');
  }
  send({ ok: true });
}
