// Tests for deploy/prod/ops/rollback-worker.sh.
//
// Runs the real bash script through Git Bash with fake ctyun-cli + fake ssh.
import { test } from "node:test";
import * as assert from "node:assert";
import * as fs from "node:fs";
import * as os from "node:os";
import * as path from "node:path";
import { fileURLToPath } from "node:url";
import { spawnSync } from "node:child_process";

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const scriptPath = path.join(__dirname, "rollback-worker.sh");

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
if (cmd.includes("ls -1d /opt/catsco/releases")) {
  const releases = state.releases || ["v1.4.8-abc123", "v1.4.7-def456"];
  const m = cmd.match(/releases\\/([A-Za-z0-9._-]+)\\*/);
  const hits = m ? releases.filter(r => r.startsWith(m[1])) : releases;
  process.stdout.write(hits.map(r => r + "\\n").join(""));
} else if (cmd.includes("ln -sfn")) {
  const m = cmd.match(/\\/opt\\/catsco\\/releases\\/([^ ]+) \\/opt\\/catsco\\/current/);
  state.rolledBack = m ? m[1] : "";
  state.serviceRestarted = true;
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
// BusyBox-style timeout: accept only -s SIG / -k KILL_SECS (+ short -sSIG/
// -kSECS). GNU-only long options (--signal/--kill-after) MUST fail — the
// production alpine image ships BusyBox timeout.
let i = 0;
while (i < args.length) {
  const a = args[i];
  if (a.startsWith("--")) {
    process.stderr.write("fake busybox timeout rejects GNU option: " + a + "\\n");
    process.exit(2);
  }
  if (a === "-s" || a === "-k") { i += 2; continue; }
  if (a.startsWith("-s") || a.startsWith("-k")) { i += 1; continue; }
  break; // first non-option = SECS
}
if (i >= args.length || i + 1 >= args.length) process.exit(2);
const cmd = args[i + 1];
const rest = args.slice(i + 2);
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
  const sandbox = fs.mkdtempSync(path.join(os.tmpdir(), "catsco-rollback-"));
  const bin = path.join(sandbox, "bin");
  fs.mkdirSync(bin);
  writeCommand(bin, "ctyun-cli", FAKE_CTYUN);
  writeCommand(bin, "ssh", FAKE_SSH);
  writeCommand(bin, "timeout", FAKE_TIMEOUT);
  // provision 会留下 STATE_DIR/id_rsa；测试默认放一个假私钥
  const stateDir = path.join(sandbox, "state");
  fs.mkdirSync(stateDir, { recursive: true });
  fs.writeFileSync(path.join(stateDir, "id_rsa"), "fake-private-key\n");
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

const INSTANCE = { instanceName: "worker-bot-a", instanceID: "i-1", state: "running", floatingIP: "10.0.0.9" };

test("rollback-worker: missing args fails", () => {
  const sb = setupSandbox({});
  const r = run(sb, []);
  assert.equal(r.status, 2, r.stderr);
  assert.match(r.stderr, /required/);
});

test("rollback-worker: instance not found fails", () => {
  const sb = setupSandbox({});
  const r = run(sb, ["--name", "bot-a"]);
  assert.notEqual(r.status, 0, `${r.stdout}\n${r.stderr}`);
  assert.match(r.stderr, /not found/);
});

test("rollback-worker: missing private key fails", () => {
  const sb = setupSandbox({ instances: [INSTANCE] });
  fs.rmSync(path.join(sb.sandbox, "state"), { recursive: true, force: true });
  const r = run(sb, ["--name", "bot-a"]);
  assert.notEqual(r.status, 0, `${r.stdout}\n${r.stderr}`);
  assert.match(r.stderr, /private key not found/);
});

test("rollback-worker: lists versions without --version", () => {
  const sb = setupSandbox({ instances: [INSTANCE], releases: ["v1.4.8-abc123", "v1.4.7-def456"] });
  const r = run(sb, ["--name", "bot-a"]);
  assert.equal(r.status, 0, `${r.stdout}\n${r.stderr}`);
  assert.match(r.stdout, /"status":"list"/);
  assert.match(r.stdout, /v1\.4\.8-abc123/);
  assert.match(r.stdout, /v1\.4\.7-def456/);
});

test("rollback-worker: switches current and restarts service", () => {
  const sb = setupSandbox({ instances: [INSTANCE], releases: ["v1.4.8-abc123", "v1.4.7-def456"] });
  const r = run(sb, ["--name", "bot-a", "--version", "v1.4.7"]);
  assert.equal(r.status, 0, `${r.stdout}\n${r.stderr}`);
  assert.match(r.stdout, /"status":"rolled-back"/);
  assert.match(r.stdout, /"version":"v1\.4\.7-def456"/);
  const state = JSON.parse(fs.readFileSync(sb.statePath, "utf8"));
  assert.equal(state.rolledBack, "v1.4.7-def456");
  assert.equal(state.serviceRestarted, true);
});

test("rollback-worker: unknown version fails without touching service", () => {
  const sb = setupSandbox({ instances: [INSTANCE], releases: ["v1.4.8-abc123"] });
  const r = run(sb, ["--name", "bot-a", "--version", "v9.9.9"]);
  assert.notEqual(r.status, 0, `${r.stdout}\n${r.stderr}`);
  assert.match(r.stderr, /release v9\.9\.9 not found/);
  const state = JSON.parse(fs.readFileSync(sb.statePath, "utf8"));
  assert.ok(!state.rolledBack, "no rollback should happen for unknown version");
});

test("rollback-worker: dry-run reports target without switching", () => {
  const sb = setupSandbox({ instances: [INSTANCE], releases: ["v1.4.8-abc123", "v1.4.7-def456"] });
  const r = run(sb, ["--name", "bot-a", "--version", "v1.4.7", "--dry-run"]);
  assert.equal(r.status, 0, `${r.stdout}\n${r.stderr}`);
  assert.match(r.stdout, /"status":"dry-run"/);
  const state = JSON.parse(fs.readFileSync(sb.statePath, "utf8"));
  assert.ok(!state.rolledBack, "no rollback in dry-run");
});
