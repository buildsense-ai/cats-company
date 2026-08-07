// Tests for deploy/prod/ops/reset-worker.sh.
//
// reset = destroy + provision. Runs the real scripts through Git Bash with
// fake ctyun-cli + fake ssh (same fakes as the provision test).
import { test } from "node:test";
import * as assert from "node:assert";
import * as fs from "node:fs";
import * as os from "node:os";
import * as path from "node:path";
import { fileURLToPath } from "node:url";
import { spawnSync } from "node:child_process";

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const scriptPath = path.join(__dirname, "reset-worker.sh");

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
} else if (op === "ecs ImportEcsKeypair") {
  const name = val("--keyPairName");
  state.keypairs = state.keypairs || [];
  state.keypairs.push({ keyPairName: name, keyPairID: "kp-" + name });
  fs.writeFileSync(statePath, JSON.stringify(state));
  json({ statusCode: "800", returnObj: {} });
} else if (op === "ecs CreateEcsInstance") {
  const name = val("--instanceName");
  const id = "i-" + name;
  state.instances = state.instances || [];
  state.instances.push({ instanceName: name, instanceID: id, state: "running", floatingIP: "10.0.0." + (state.instances.length + 1) });
  fs.writeFileSync(statePath, JSON.stringify(state));
  json({ statusCode: "800", returnObj: { masterResourceID: id } });
} else if (op === "ecs DeleteEcsInstance") {
  state.deletedInstances = state.deletedInstances || [];
  state.deletedInstances.push(val("--instanceID"));
  state.instances = (state.instances || []).filter(i => i.instanceID !== val("--instanceID"));
  fs.writeFileSync(statePath, JSON.stringify(state));
  json({ statusCode: "800", returnObj: {} });
} else if (op === "ecs DeleteEcsKeypair") {
  state.deletedKeypairs = state.deletedKeypairs || [];
  state.deletedKeypairs.push(val("--keyPairName"));
  state.keypairs = (state.keypairs || []).filter(k => k.keyPairName !== val("--keyPairName"));
  fs.writeFileSync(statePath, JSON.stringify(state));
  json({ statusCode: "800", returnObj: {} });
} else {
  process.stderr.write("unexpected: " + op); process.exit(2);
}
`;

const FAKE_SSH = `
import fs from "node:fs";
const statePath = process.env.FAKE_STATE;
const args = process.argv.slice(2);
const rest = args.filter(a => a.startsWith("root@"));
if (rest.length === 0) process.exit(0);
const remote = rest[rest.length - 1];
const idx = args.indexOf(remote);
const cmd = args.slice(idx + 1).join(" ").trim();
const state = JSON.parse(fs.readFileSync(statePath, "utf8"));
state.sshCalls = state.sshCalls || [];
state.sshCalls.push(cmd);
if (cmd.includes("cloud-init status")) {
  // SSH 探测成功
} else if (cmd.includes("cat > /srv/catsco-agent/.env")) {
  const env = fs.readFileSync(0, "utf8");
  state.injectedEnv = env;
  fs.writeFileSync(statePath, JSON.stringify(state));
} else if (cmd.includes("systemctl enable")) {
  state.serviceEnabled = true;
  fs.writeFileSync(statePath, JSON.stringify(state));
  process.stdout.write("active\\n");
}
fs.writeFileSync(statePath, JSON.stringify(state));
process.exit(0);
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

function setupSandbox(state, injectEnv) {
  const sandbox = fs.mkdtempSync(path.join(os.tmpdir(), "catsco-reset-"));
  const bin = path.join(sandbox, "bin");
  fs.mkdirSync(bin);
  writeCommand(bin, "ctyun-cli", FAKE_CTYUN);
  writeCommand(bin, "ssh", FAKE_SSH);
  writeCommand(bin, "scp", "process.exit(0);");
  writeCommand(bin, "timeout", FAKE_TIMEOUT);
  const stateDir = path.join(sandbox, "state");
  fs.mkdirSync(stateDir, { recursive: true });
  if (injectEnv) {
    fs.writeFileSync(path.join(stateDir, "inject.env"), injectEnv, { mode: 0o600 });
  }
  const statePath = path.join(sandbox, "state.json");
  fs.writeFileSync(statePath, JSON.stringify(state || {}));
  const jqDir = process.env.CATSCO_JQ ? path.dirname(process.env.CATSCO_JQ) : "";
  const gitBins = ["C:\\Program Files\\Git\\usr\\bin", "C:\\Program Files\\Git\\bin"].filter((p) => fs.existsSync(p));
  const extraPath = [bin, jqDir, ...gitBins].filter(Boolean).join(path.delimiter);
  return { sandbox, statePath, bin, env: (extra) => ({
    ...process.env,
    PATH: `${extraPath}${path.delimiter}${process.env.PATH || ""}`,
    CTYUN_WORKER_REGION_ID: "region-test",
    CTYUN_WORKER_AZ_NAME: "az-test",
    CTYUN_WORKER_FLAVOR_ID: "f-test",
    CTYUN_WORKER_VPC_ID: "v-test",
    CTYUN_WORKER_SUBNET_ID: "s-test",
    CTYUN_WORKER_SECURITY_GROUP_ID: "g-test",
    // MSYS 形式（/c/...）：脚本里 bash 内建 [[ -f ]] 只认 Unix 路径
    CTYUN_WORKER_STATE_DIR: toMsys(stateDir),
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
    timeout: 90_000,
    env: sandbox.env(extra),
  });
  return {
    status: res.status,
    stdout: (res.stdout || "").replace(/\r/g, ""),
    stderr: (res.stderr || "").replace(/\r/g, ""),
  };
}

const SNAPSHOT = [
  "CATSCO_HTTP_BASE_URL=https://app.catsco.cc",
  "CATSCO_SERVER_URL=wss://app.catsco.cc/v0/channels",
  "CATSCO_API_KEY=SNAPKEY",
  "CATSCO_BOT_UID=99",
  "CATSCO_BODY_ID=body-1",
  "CATSCO_INSTALLATION_ID=inst-1",
  "CATSCO_USER_TOKEN=SNAPJWT",
  "CATSCO_USER_UID=7",
  "CATSCO_USER_NAME=alice",
  "CATSCO_USER_DISPLAY_NAME=Alice",
  "CATSCO_LOG_UPLOAD_ENABLED=true",
].join("\n") + "\n";

test("reset-worker: missing args fails", () => {
  const sb = setupSandbox({});
  const r = run(sb, []);
  assert.equal(r.status, 2, r.stderr);
  assert.match(r.stderr, /required/);
});

test("reset-worker: refuses without credentials and no snapshot", () => {
  const sb = setupSandbox({});
  const r = run(sb, ["--name", "bot-a"]);
  assert.equal(r.status, 2, r.stderr);
  assert.match(r.stderr, /--api-key and --login-token are required/);
});

test("reset-worker: happy path destroys then reprovisions", () => {
  const sb = setupSandbox({});
  const r = run(sb, ["--name", "bot-a", "--login-token", "JWT", "--api-key", "KEY",
    "--bot-uid", "42", "--user-uid", "7", "--user-name", "alice", "--user-display", "Alice", "--image-id", "img-1"]);
  if (r.status !== 0) {
    const dbg = fs.readFileSync(sb.statePath, "utf8");
    assert.equal(r.status, 0, `status=${r.status}\nSTDOUT:\n${r.stdout}\nSTDERR:\n${r.stderr}\nSTATE:\n${dbg}`);
  }
  assert.match(r.stdout, /"status":"provisioned"/);
  assert.match(r.stdout, /"instance_name":"worker-bot-a"/);
  const state = JSON.parse(fs.readFileSync(sb.statePath, "utf8"));
  assert.ok(state.injectedEnv, "env should be re-injected after reset");
  assert.match(state.injectedEnv, /CATSCO_USER_TOKEN=JWT/);
  assert.match(state.injectedEnv, /CATSCO_API_KEY=KEY/);
  assert.match(state.injectedEnv, /CATSCO_BOT_UID=42/);
  assert.equal(state.serviceEnabled, true, "service should be enabled after reset");
  // destroy 在前：旧实例（若有）先删。本测试无旧实例 → destroy 报 not-found 不阻塞
  const inst = (state.instances || []).find(i => i.instanceName === "worker-bot-a");
  assert.ok(inst, "reprovisioned instance should exist");
});

test("reset-worker: reprovisions an existing worker (destroy old first)", () => {
  const old = { instanceName: "worker-bot-a", instanceID: "i-old", state: "running", floatingIP: "10.0.0.9" };
  const sb = setupSandbox({ instances: [old] });
  const r = run(sb, ["--name", "bot-a", "--login-token", "JWT", "--api-key", "KEY", "--image-id", "img-1"]);
  if (r.status !== 0) {
    const dbg = fs.readFileSync(sb.statePath, "utf8");
    assert.equal(r.status, 0, `status=${r.status}\nSTDOUT:\n${r.stdout}\nSTDERR:\n${r.stderr}\nSTATE:\n${dbg}`);
  }
  assert.match(r.stdout, /"status":"provisioned"/);
  const state = JSON.parse(fs.readFileSync(sb.statePath, "utf8"));
  assert.ok((state.deletedInstances || []).includes("i-old"), "old instance destroyed first");
  assert.ok(!(state.instances || []).some(i => i.instanceID === "i-old"), "old instance gone");
  assert.ok(state.injectedEnv, "env re-injected");
});

test("reset-worker: falls back to inject.env snapshot for identity", () => {
  const sb = setupSandbox({}, SNAPSHOT);
  // 不带注入参数 → 从 inject.env 快照读身份
  const r = run(sb, ["--name", "bot-a", "--image-id", "img-1"]);
  if (r.status !== 0) {
    const dbg = fs.readFileSync(sb.statePath, "utf8");
    assert.equal(r.status, 0, `status=${r.status}\nSTDOUT:\n${r.stdout}\nSTDERR:\n${r.stderr}\nSTATE:\n${dbg}`);
  }
  assert.match(r.stdout, /"status":"provisioned"/);
  const state = JSON.parse(fs.readFileSync(sb.statePath, "utf8"));
  assert.match(state.injectedEnv, /CATSCO_USER_TOKEN=SNAPJWT/);
  assert.match(state.injectedEnv, /CATSCO_API_KEY=SNAPKEY/);
  assert.match(state.injectedEnv, /CATSCO_BOT_UID=99/);
});
