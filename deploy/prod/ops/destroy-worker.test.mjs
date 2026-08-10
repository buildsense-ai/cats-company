// Tests for deploy/prod/ops/destroy-worker.sh.
//
// Runs the real bash script through Git Bash with fake ctyun-cli.
import { test } from "node:test";
import * as assert from "node:assert";
import * as fs from "node:fs";
import * as os from "node:os";
import * as path from "node:path";
import { fileURLToPath } from "node:url";
import { spawnSync } from "node:child_process";

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const scriptPath = path.join(__dirname, "destroy-worker.sh");

function bashPath() {
  if (process.platform === "win32") {
    for (const c of ["C:\\Program Files\\Git\\bin\\bash.exe", "C:\\Program Files\\Git\\usr\\bin\\bash.exe"]) {
      if (fs.existsSync(c)) return c;
    }
  }
  return "bash";
}
const BASH = bashPath();

const FAKE_CTYUN = `
import fs from "node:fs";
const statePath = process.env.FAKE_STATE;
const args = process.argv.slice(2);
const op = args.slice(0, 2).join(" ");
const state = JSON.parse(fs.readFileSync(statePath, "utf8"));
const val = f => { const i = args.indexOf(f); return i >= 0 ? args[i + 1] : ""; };
const json = o => process.stdout.write(JSON.stringify(o));
if (op === "ecs ListEcsInstances") {
  const name = val("--instanceName");
  json({ statusCode: "800", returnObj: { results: (state.instances || []).filter(i => !name || i.instanceName === name) } });
} else if (op === "ecs GetEcsKeypairDetails") {
  const name = val("--keyPairName");
  json({ statusCode: "800", returnObj: { results: (state.keypairs || []).filter(k => k.keyPairName === name) } });
} else if (op === "ecs DeleteEcsInstance") {
  if (state.failDeleteInstance) { json({ statusCode: "900", errorCode: "E.DEL", message: "boom" }); process.exit(0); }
  state.deletedInstances = state.deletedInstances || [];
  state.deletedInstances.push(val("--instanceID"));
  state.instances = (state.instances || []).filter(i => i.instanceID !== val("--instanceID"));
  fs.writeFileSync(statePath, JSON.stringify(state));
  json({ statusCode: "800", returnObj: {} });
} else if (op === "ecs DeleteEcsKeypair") {
  if (state.failDeleteKeypair) { json({ statusCode: "900", errorCode: "E.DELKP", message: "boom" }); process.exit(0); }
  state.deletedKeypairs = state.deletedKeypairs || [];
  state.deletedKeypairs.push(val("--keyPairName"));
  state.keypairs = (state.keypairs || []).filter(k => k.keyPairName !== val("--keyPairName"));
  fs.writeFileSync(statePath, JSON.stringify(state));
  json({ statusCode: "800", returnObj: {} });
} else {
  process.stderr.write("unexpected: " + op); process.exit(2);
}
`;

const FAKE_TIMEOUT = `
import { spawnSync } from "node:child_process";
import path from "node:path";
import fs from "node:fs";
const args = process.argv.slice(2);
const idx = args.findIndex(a => !a.startsWith("-"));
if (idx < 0 || !args[idx + 1]) process.exit(2);
const cmd = args[idx + 1];
const rest = args.slice(idx + 2);
if (process.platform === "win32") {
  const here = path.join(path.dirname(process.argv[1]), cmd);
  if (fs.existsSync(here) && !path.extname(here)) {
    const r = spawnSync(process.execPath, [here, ...rest], { stdio: "inherit" });
    process.exit(r.status ?? 1);
  }
}
const r = spawnSync(cmd, rest, { stdio: "inherit" });
process.exit(r.status ?? 1);
`;

function writeCommand(bin, name, body) {
  const p = path.join(bin, name);
  fs.writeFileSync(p, `#!/usr/bin/env node\n${body.trim()}\n`);
  fs.chmodSync(p, 0o755);
  fs.writeFileSync(`${p}.cmd`, `@echo off\r\nnode "%~dp0${name}" %*\r\n`);
}

function setupSandbox(state) {
  const sandbox = fs.mkdtempSync(path.join(os.tmpdir(), "catsco-destroy-"));
  const bin = path.join(sandbox, "bin");
  fs.mkdirSync(bin);
  writeCommand(bin, "ctyun-cli", FAKE_CTYUN);
  writeCommand(bin, "timeout", FAKE_TIMEOUT);
  const statePath = path.join(sandbox, "state.json");
  fs.writeFileSync(statePath, JSON.stringify(state || {}));
  const jqDir = process.env.CATSCO_JQ ? path.dirname(process.env.CATSCO_JQ) : "";
  const gitBins = ["C:\\Program Files\\Git\\usr\\bin", "C:\\Program Files\\Git\\bin"].filter((p) => fs.existsSync(p));
  const extraPath = [bin, jqDir, ...gitBins].filter(Boolean).join(path.delimiter);
  return { sandbox, statePath, bin, env: (extra) => ({
    ...process.env,
    PATH: `${extraPath}${path.delimiter}${process.env.PATH || ""}`,
    CTYUN_WORKER_REGION_ID: "region-test",
    // MSYS 形式（/c/...）：脚本里 bash 内建 [[ -f ]] / [[ -d ]] 只认 Unix 路径
    CTYUN_WORKER_STATE_DIR: toMsys(path.join(sandbox, "state")),
    FAKE_STATE: statePath,
    ...extra,
  }) };
}

function toMsys(p) {
  const m = /^([A-Za-z]):(.*)$/.exec(p);
  if (!m) return p.replace(/\\/g, "/");
  return "/" + m[1].toLowerCase() + m[2].replace(/\\/g, "/");
}

function run(sandbox, args, extra = {}) {
  const cmd = `export PATH="${toMsys(sandbox.bin)}:$PATH"; exec "${toMsys(scriptPath)}" "$@"`;
  const res = spawnSync(BASH, ["-c", cmd, "bash", ...args], {
    cwd: sandbox.sandbox,
    encoding: "utf8",
    timeout: 60_000,
    env: sandbox.env(extra),
  });
  return {
    status: res.status,
    stdout: (res.stdout || "").replace(/\r/g, ""),
    stderr: (res.stderr || "").replace(/\r/g, ""),
  };
}

test("destroy-worker: missing args fails", () => {
  const sb = setupSandbox({});
  const r = run(sb, []);
  assert.equal(r.status, 2, r.stderr);
  assert.match(r.stderr, /required/);
});

test("destroy-worker: not-found when instance missing", () => {
  const sb = setupSandbox({});
  const r = run(sb, ["--name", "bot-a"]);
  assert.equal(r.status, 0, `${r.stdout}\n${r.stderr}`);
  assert.match(r.stdout, /"status":"not-found"/);
  const state = JSON.parse(fs.readFileSync(sb.statePath, "utf8"));
  assert.equal((state.deletedInstances || []).length, 0);
});

test("destroy-worker: dry-run deletes nothing", () => {
  const sb = setupSandbox({
    instances: [{ instanceName: "worker-bot-a", instanceID: "i-1", state: "running", floatingIP: "10.0.0.9" }],
    keypairs: [{ keyPairName: "worker-key-bot-a", keyPairID: "kp-1" }],
  });
  const r = run(sb, ["--name", "bot-a", "--dry-run"]);
  assert.equal(r.status, 0, `${r.stdout}\n${r.stderr}`);
  assert.match(r.stdout, /"status":"dry-run"/);
  const state = JSON.parse(fs.readFileSync(sb.statePath, "utf8"));
  assert.equal((state.deletedInstances || []).length, 0, "no instance deleted in dry-run");
  assert.equal((state.deletedKeypairs || []).length, 0, "no keypair deleted in dry-run");
});

test("destroy-worker: deletes instance + keypair + state dir", () => {
  const sb = setupSandbox({
    instances: [{ instanceName: "worker-bot-a", instanceID: "i-1", state: "running", floatingIP: "10.0.0.9" }],
    keypairs: [{ keyPairName: "worker-key-bot-a", keyPairID: "kp-1" }],
  });
  // 模拟 provision 留下的 state 目录
  const st = path.join(sb.sandbox, "state", "bot-a");
  fs.mkdirSync(st, { recursive: true });
  fs.writeFileSync(path.join(st, "id_rsa"), "fake-key");

  const r = run(sb, ["--name", "bot-a"]);
  assert.equal(r.status, 0, `${r.stdout}\n${r.stderr}`);
  assert.match(r.stdout, /"status":"destroyed"/);
  assert.match(r.stdout, /"instance_id":"i-1"/);
  const state = JSON.parse(fs.readFileSync(sb.statePath, "utf8"));
  assert.deepEqual(state.deletedInstances, ["i-1"]);
  assert.deepEqual(state.deletedKeypairs, ["worker-key-bot-a"]);
  assert.ok(!fs.existsSync(st), "state dir should be cleaned up");
});

test("destroy-worker: keypair still cleaned when instance already gone", () => {
  const sb = setupSandbox({
    keypairs: [{ keyPairName: "worker-key-bot-a", keyPairID: "kp-1" }],
  });
  const r = run(sb, ["--name", "bot-a"]);
  assert.equal(r.status, 0, `${r.stdout}\n${r.stderr}`);
  assert.match(r.stdout, /"status":"not-found"/);
  const state = JSON.parse(fs.readFileSync(sb.statePath, "utf8"));
  assert.deepEqual(state.deletedKeypairs, ["worker-key-bot-a"]);
});

test("destroy-worker: instance delete failure fails closed", () => {
  const sb = setupSandbox({
    instances: [{ instanceName: "worker-bot-a", instanceID: "i-1", state: "running", floatingIP: "10.0.0.9" }],
    keypairs: [{ keyPairName: "worker-key-bot-a", keyPairID: "kp-1" }],
    failDeleteInstance: true,
  });
  const r = run(sb, ["--name", "bot-a"]);
  assert.notEqual(r.status, 0, `${r.stdout}\n${r.stderr}`);
  assert.match(r.stderr, /instance delete failed/);
  const state = JSON.parse(fs.readFileSync(sb.statePath, "utf8"));
  assert.equal((state.deletedKeypairs || []).length, 0, "keypair cleanup should not run after instance delete failure");
});

test("destroy-worker: keypair delete failure fails closed with aggregate error", () => {
  const sb = setupSandbox({
    instances: [{ instanceName: "worker-bot-a", instanceID: "i-1", state: "running", floatingIP: "10.0.0.9" }],
    keypairs: [{ keyPairName: "worker-key-bot-a", keyPairID: "kp-1" }],
    failDeleteKeypair: true,
  });
  const r = run(sb, ["--name", "bot-a"]);
  console.log("DBG status=" + r.status);
  console.log("DBG stdout=" + JSON.stringify(r.stdout));
  console.log("DBG stderr=" + JSON.stringify(r.stderr));
  console.log("DBG state=" + fs.readFileSync(sb.statePath, "utf8"));
  assert.notEqual(r.status, 0, `${r.stdout}\n${r.stderr}`);
  assert.match(r.stderr, /key pair delete failed/);
  const state = JSON.parse(fs.readFileSync(sb.statePath, "utf8"));
  assert.deepEqual(state.deletedInstances, ["i-1"], "instance delete happened before keypair failure");
});
