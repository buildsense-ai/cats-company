// Tests for deploy/prod/ops/renew-worker.sh.
import { test } from "node:test";
import * as assert from "node:assert";
import * as fs from "node:fs";
import * as os from "node:os";
import * as path from "node:path";
import { fileURLToPath } from "node:url";
import { spawnSync } from "node:child_process";

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const scriptPath = path.join(__dirname, "renew-worker.sh");

function bashPath() {
  if (process.platform === "win32") {
    for (const candidate of ["C:\\Program Files\\Git\\bin\\bash.exe", "C:\\Program Files\\Git\\usr\\bin\\bash.exe"]) {
      if (fs.existsSync(candidate)) return candidate;
    }
  }
  return "bash";
}

const FAKE_CTYUN = `
import fs from "node:fs";
const statePath = process.env.FAKE_STATE;
const args = process.argv.slice(2);
const op = args.slice(0, 2).join(" ");
const state = JSON.parse(fs.readFileSync(statePath, "utf8"));
const val = flag => { const i = args.indexOf(flag); return i >= 0 ? args[i + 1] : ""; };
const save = () => fs.writeFileSync(statePath, JSON.stringify(state));
const json = value => process.stdout.write(JSON.stringify(value));
if (op === "ecs ListEcsInstances") {
  const name = val("--instanceName");
  json({ statusCode: "800", returnObj: { results: (state.instances || []).filter(i => !name || i.instanceName === name) } });
} else if (op === "ecs RecoverEcsUnsubscribedInstance") {
  state.recoverCalls = (state.recoverCalls || 0) + 1;
  state.instances = (state.instances || []).map(i => ({ ...i, state: "running", instanceStatus: "running", expiredTime: "2099-01-01T00:00:00Z" }));
  save();
  json({ statusCode: "800", returnObj: { orderInfo: [] } });
} else if (op === "ecs ResubscribeEcsInstance") {
  state.resubscribeCalls = (state.resubscribeCalls || 0) + 1;
  state.instances = (state.instances || []).map(i => ({ ...i, state: "running", instanceStatus: "running", expiredTime: "2099-01-01T00:00:00Z" }));
  save();
  json({ statusCode: "800", returnObj: {} });
} else {
  process.stderr.write("unexpected: " + op);
  process.exit(2);
}
`;

const FAKE_TIMEOUT = `
import { spawnSync } from "node:child_process";
const args = process.argv.slice(2);
let i = 0;
while (i < args.length && (args[i] === "-s" || args[i] === "-k")) i += 2;
if (i + 1 >= args.length) process.exit(2);
const result = spawnSync(args[i + 1], args.slice(i + 2), { stdio: "inherit" });
process.exit(result.status ?? 1);
`;

function writeCommand(bin, name, body) {
  const file = path.join(bin, name);
  fs.writeFileSync(file, `#!/usr/bin/env node\n${body.trim()}\n`);
  fs.chmodSync(file, 0o755);
}

function toMsys(value) {
  const match = /^([A-Za-z]):(.*)$/.exec(value);
  if (!match) return value.replaceAll("\\\\", "/");
  return `/${match[1].toLowerCase()}${match[2].replaceAll("\\\\", "/")}`;
}

function setup(instance) {
  const sandbox = fs.mkdtempSync(path.join(os.tmpdir(), "catsco-renew-"));
  fs.writeFileSync(path.join(sandbox, "package.json"), '{"type":"module"}');
  const bin = path.join(sandbox, "bin");
  fs.mkdirSync(bin);
  writeCommand(bin, "ctyun-cli", FAKE_CTYUN);
  writeCommand(bin, "timeout", FAKE_TIMEOUT);
  writeCommand(bin, "sleep", "process.exit(0);");
  const statePath = path.join(sandbox, "state.json");
  fs.writeFileSync(statePath, JSON.stringify({ instances: [instance] }));
  const gitBins = ["C:\\Program Files\\Git\\usr\\bin", "C:\\Program Files\\Git\\bin"].filter(fs.existsSync);
  return {
    statePath,
    env: {
      ...process.env,
      PATH: [bin, ...gitBins, process.env.PATH || ""].join(path.delimiter),
      CTYUN_WORKER_REGION_ID: "region-test",
      CTYUN_WORKER_PROJECT_ID: "project-test",
      FAKE_STATE: statePath,
    },
  };
}

function run(sb, args) {
  const command = `export PATH="${toMsys(path.dirname(sb.statePath))}/bin:$PATH"; exec "${toMsys(scriptPath)}" "$@"`;
  const result = spawnSync(bashPath(), ["-c", command, "bash", ...args], {
    cwd: path.dirname(sb.statePath),
    encoding: "utf8",
    timeout: 90_000,
    env: sb.env,
  });
  return { status: result.status, stdout: result.stdout || "", stderr: result.stderr || "" };
}

test("renew-worker: dry-run selects provider resubscribe for active instance", () => {
  const sb = setup({ instanceName: "worker-bot-a", instanceID: "i-active", instanceStatus: "running", state: "running" });
  const result = run(sb, ["--name", "bot-a", "--dry-run"]);
  assert.equal(result.status, 0, result.stderr);
  assert.match(result.stdout, /"operation":"resubscribe"/);
  const state = JSON.parse(fs.readFileSync(sb.statePath, "utf8"));
  assert.equal(state.resubscribeCalls || 0, 0);
});

test("renew-worker: dry-run selects recovery for unsubscribed retention instance", () => {
  const sb = setup({ instanceName: "worker-bot-a", instanceID: "i-unsub", instanceStatus: "unsubscribed", state: "unsubscribed", releaseTime: "2099-01-01T00:00:00Z" });
  const result = run(sb, ["--name", "bot-a", "--dry-run"]);
  assert.equal(result.status, 0, result.stderr);
  assert.match(result.stdout, /"operation":"recover"/);
});

test("renew-worker: recovers an unsubscribed instance without creating a replacement", () => {
  const sb = setup({ instanceName: "worker-bot-a", instanceID: "i-unsub", instanceStatus: "unsubscribed", state: "unsubscribed", releaseTime: "2099-01-01T00:00:00Z" });
  const result = run(sb, ["--name", "bot-a"]);
  assert.equal(result.status, 0, `${result.stdout}\n${result.stderr}`);
  assert.match(result.stdout, /"status":"renewed"/);
  const state = JSON.parse(fs.readFileSync(sb.statePath, "utf8"));
  assert.equal(state.recoverCalls, 1);
  assert.equal(state.resubscribeCalls || 0, 0);
  assert.equal(state.instances[0].instanceID, "i-unsub");
  assert.equal(state.instances[0].instanceStatus, "running");
  assert.match(result.stdout, /"expires_at":"2099-01-01T00:00:00Z"/);
});

test("renew-worker: refuses an instance past provider release time", () => {
  const sb = setup({ instanceName: "worker-bot-a", instanceID: "i-expired", instanceStatus: "unsubscribed", state: "unsubscribed", releaseTime: "2020-01-01T00:00:00Z" });
  const result = run(sb, ["--name", "bot-a"]);
  assert.equal(result.status, 1);
  assert.match(result.stderr, /passed Tianyi releaseTime/);
});
