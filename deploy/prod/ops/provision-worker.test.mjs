// Tests for deploy/prod/ops/provision-worker.sh.
//
// Runs the real bash script through Git Bash with fake ctyun-cli + fake ssh.
// Requires jq on PATH (CATSCO_JQ or PATH). Uses the real ssh-keygen from Git
// Bash for the key pair step.
import { test } from "node:test";
import * as assert from "node:assert";
import * as fs from "node:fs";
import * as os from "node:os";
import * as path from "node:path";
import { fileURLToPath } from "node:url";
import { spawnSync } from "node:child_process";

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const scriptPath = path.join(__dirname, "provision-worker.sh");

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
  state.listCalls = (state.listCalls || 0) + 1;
  fs.writeFileSync(statePath, JSON.stringify(state));
  const instances = state.hideInstancesOnList ? [] : (state.instances || []);
  json({ statusCode: "800", returnObj: { results: instances.filter(i => !name || i.instanceName === name) } });
} else if (op === "ecs GetEcsKeypairDetails") {
  const name = val("--keyPairName");
  json({ statusCode: "800", returnObj: { results: (state.keypairs || []).filter(k => k.keyPairName === name) } });
} else if (op === "ecs ImportEcsKeypair") {
  if (state.failImport) { json({ statusCode: "900", errorCode: "E.IMPORT", message: "boom" }); process.exit(0); }
  const name = val("--keyPairName");
  state.keypairs = state.keypairs || [];
  state.keypairs.push({ keyPairName: name, keyPairID: "kp-" + name });
  fs.writeFileSync(statePath, JSON.stringify(state));
  json({ statusCode: "800", returnObj: {} });
} else if (op === "ecs CreateEcsInstance") {
  if (state.failCreate) { json({ statusCode: "900", errorCode: "E.CREATE", message: "boom" }); process.exit(0); }
  const name = val("--instanceName");
  const id = "i-" + name;
  state.instances = state.instances || [];
  state.createArgs = args.join(" ");
  // 包月模式（--onDemand false）实例带 expiredTime；按量不带
  const inst = { instanceName: name, instanceID: id, state: "running", floatingIP: "10.0.0." + (state.instances.length + 1) };
  if (val("--onDemand") === "false") inst.expiredTime = "2030-01-01T00:00:00Z";
  state.instances.push(inst);
  fs.writeFileSync(statePath, JSON.stringify(state));
  json({ statusCode: "800", returnObj: { masterResourceID: id } });
} else if (op === "ecs UpdateEcsAutoRenewConfig") {
  if (state.failAutoRenew) { json({ statusCode: "900", errorCode: "E.RENEW", message: "boom" }); process.exit(0); }
  state.renewCalls = state.renewCalls || [];
  state.renewCalls.push({ instanceIDList: val("--instanceIDList"), autoRenewStatus: val("--autoRenewStatus"), autoRenewCycleType: val("--autoRenewCycleType"), autoRenewCycleCount: val("--autoRenewCycleCount") });
  fs.writeFileSync(statePath, JSON.stringify(state));
  json({ statusCode: "800", returnObj: {} });
} else if (op === "ecs UnsubscribeEcsInstance") {
  state.unsubscribedInstances = state.unsubscribedInstances || [];
  state.unsubscribedInstances.push(val("--instanceID"));
  state.instances = (state.instances || []).filter(i => i.instanceID !== val("--instanceID"));
  fs.writeFileSync(statePath, JSON.stringify(state));
  json({ statusCode: "800", returnObj: {} });
} else if (op === "ecs DeleteEcsInstance") {
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

const FAKE_SSH = `
import fs from "node:fs";
const statePath = process.env.FAKE_STATE;
const args = process.argv.slice(2);
// 找远程命令：最后一个参数是 "root@ip 命令..." 或分离
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
  // .env 注入：同步读整个 stdin（here-string 以 EOF 结束）
  const env = fs.readFileSync(0, "utf8");
  state.injectedEnv = env;
  if (state.failSshAfterCloudInit) {
    if (state.hideInstancesAfterSshFailure) state.hideInstancesOnList = true;
    fs.writeFileSync(statePath, JSON.stringify(state));
    process.exit(1);
  }
  fs.writeFileSync(statePath, JSON.stringify(state));
} else if (cmd.includes("cat > /srv/catsco-agent/.xiaoba/catsco.json")) {
  // localConfig（bootstrap 身份）写入
  const cfg = fs.readFileSync(0, "utf8");
  state.localConfig = cfg;
  fs.writeFileSync(statePath, JSON.stringify(state));
} else if (cmd.includes("systemctl enable")) {
  state.serviceEnabled = true;
  fs.writeFileSync(statePath, JSON.stringify(state));
  process.stdout.write("active\\n");
} else if (cmd.includes("worker-release.json")) {
  process.stdout.write(JSON.stringify({ version: "1.4.8" }));
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
// Run fake node scripts in this bin dir directly (avoids .cmd %* arg mangling
// and real ssh.exe/ctyun-cli.exe taking precedence over the fakes).
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
  const sandbox = fs.mkdtempSync(path.join(os.tmpdir(), "catsco-prov-"));
  const bin = path.join(sandbox, "bin");
  fs.mkdirSync(bin);
  writeCommand(bin, "ctyun-cli", FAKE_CTYUN);
  writeCommand(bin, "ssh", FAKE_SSH);
  writeCommand(bin, "scp", "process.exit(0);");
  writeCommand(bin, "sleep", "process.exit(0);");
  writeCommand(bin, "timeout", FAKE_TIMEOUT);
  const statePath = path.join(sandbox, "state.json");
  fs.writeFileSync(statePath, JSON.stringify(state || {}));
  const jqDir = process.env.CATSCO_JQ ? path.dirname(process.env.CATSCO_JQ) : "";
  // Git Bash usr/bin so the fake timeout shim (node spawnSync) can find the
  // real ssh-keygen when the bash script invokes it.
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
    CTYUN_WORKER_STATE_DIR: path.join(sandbox, "state"),
    FAKE_STATE: statePath,
    ...extra,
  }) };
}

// Windows path -> MSYS path (C:\a\b -> /c/a/b) so bash can exec it.
function toMsys(p) {
  const m = /^([A-Za-z]):(.*)$/.exec(p);
  if (!m) return p.replace(/\\/g, "/");
  return "/" + m[1].toLowerCase() + m[2].replace(/\\/g, "/");
}

function run(sandbox, args, extra = {}) {
  // Git Bash reorders PATH at startup (puts /usr/bin first), so re-export the
  // fake bin to the front inside the session to override real timeout/ssh.
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

test("provision-worker: missing args fails", () => {
  const sb = setupSandbox({});
  const r = run(sb, []);
  assert.equal(r.status, 2, r.stderr);
  assert.match(r.stderr, /required/);
});

test("provision-worker: idempotent when instance exists", () => {
  const sb = setupSandbox({ instances: [{ instanceName: "worker-bot-a", instanceID: "i-existing", state: "running", floatingIP: "10.0.0.9" }] });
  const r = run(sb, ["--name", "bot-a", "--login-token", "t", "--api-key", "k"]);
  assert.equal(r.status, 0, r.stderr);
  assert.match(r.stdout, /"status":"exists"/);
  const state = JSON.parse(fs.readFileSync(sb.statePath, "utf8"));
  assert.ok(!state.injectedEnv, "no env injected on existing worker");
});

test("provision-worker: refuses when same-name instance exists but not running", () => {
  const sb = setupSandbox({ instances: [{ instanceName: "worker-bot-a", instanceID: "i-stopped", state: "stopped", floatingIP: "10.0.0.9" }] });
  const r = run(sb, ["--name", "bot-a", "--login-token", "t", "--api-key", "k", "--image-id", "img-1"]);
  assert.notEqual(r.status, 0, `${r.stdout}\n${r.stderr}`);
  assert.match(r.stderr, /not running/);
  const state = JSON.parse(fs.readFileSync(sb.statePath, "utf8"));
  assert.ok(!state.injectedEnv, "must not provision over a non-running instance");
});

test("provision-worker: dry-run resolves image and creates nothing", () => {
  const sb = setupSandbox({});
  // fake list-worker-images not present; dry-run needs an image → provide --image-id
  const r = run(sb, ["--name", "bot-a", "--login-token", "t", "--api-key", "k", "--image-id", "img-1", "--dry-run"]);
  assert.equal(r.status, 0, r.stderr);
  assert.match(r.stdout, /"status":"dry-run"/);
  assert.match(r.stdout, /"image_id":"img-1"/);
  const state = JSON.parse(fs.readFileSync(sb.statePath, "utf8"));
  assert.equal((state.instances || []).length, 0);
});

test("provision-worker: happy path creates instance, injects env, enables service", () => {
  const sb = setupSandbox({});
  const r = run(sb, ["--name", "bot-a", "--login-token", "USERJWT", "--api-key", "BOTKEY",
    "--bot-uid", "42", "--user-uid", "7", "--user-name", "alice", "--user-display", "Alice",
    "--image-id", "img-1", "--body-id", "body-1", "--installation-id", "inst-1"]);
  if (r.status !== 0) {
    const dbg = fs.readFileSync(sb.statePath, "utf8");
    assert.equal(r.status, 0, `status=${r.status}\nSTDOUT:\n${r.stdout}\nSTDERR:\n${r.stderr}\nSTATE:\n${dbg}`);
  }
  // stdout 必须是纯 JSON（systemctl is-active 的输出不得污染约定）
  const parsed = JSON.parse(r.stdout.trim());
  assert.equal(parsed.status, "provisioned");
  assert.equal(parsed.instance_name, "worker-bot-a");

  const state = JSON.parse(fs.readFileSync(sb.statePath, "utf8"));
  assert.ok(state.injectedEnv, "env should be injected");
  assert.match(state.injectedEnv, /CATSCO_USER_TOKEN=USERJWT/);
  assert.match(state.injectedEnv, /CATSCO_API_KEY=BOTKEY/);
  assert.match(state.injectedEnv, /CATSCO_BOT_UID=42/);
  assert.match(state.injectedEnv, /CATSCO_USER_UID=7/);
  assert.match(state.injectedEnv, /CATSCO_BODY_ID=body-1/);
  assert.match(state.injectedEnv, /CATSCO_INSTALLATION_ID=inst-1/);
  assert.equal(state.serviceEnabled, true, "service should be enabled");
  assert.equal(fs.readFileSync(path.join(sb.sandbox, "state", "app_version"), "utf8"), "1.4.8\n");

  // localConfig（bootstrap 身份）：worker catsco 命令依赖它（bodyId + 绑定确认）
  assert.ok(state.localConfig, "localConfig should be written");
  const lc = JSON.parse(state.localConfig);
  assert.equal(lc.version, 1);
  assert.equal(lc.currentBot.uid, "42");
  assert.equal(lc.currentBot.apiKey, "BOTKEY");
  assert.equal(lc.currentBot.boundByUserUid, "7");
  assert.equal(lc.currentBot.bindingSource, "cloud-provision");
  assert.equal(lc.device.bodyId, "body-1");
  assert.equal(lc.device.installationId, "inst-1");
  assert.equal(lc.account.token, "USERJWT");
  assert.equal(lc.account.uid, "7");
  assert.equal(lc.endpoints.serverUrl, "wss://app.catsco.cc/v0/channels");
  assert.ok((state.keypairs || []).some(k => k.keyPairName === "worker-key-bot-a"), "key pair created");
});

test("provision-worker: state root isolates credentials by tenant", () => {
  const sb = setupSandbox({});
  const stateRoot = path.join(sb.sandbox, "tenant-state");
  const result = run(
    sb,
    ["--name", "bot-a", "--login-token", "USERJWT", "--api-key", "BOTKEY", "--image-id", "img-1"],
    { CTYUN_WORKER_STATE_ROOT: toMsys(stateRoot), CTYUN_WORKER_STATE_DIR: "" },
  );
  assert.equal(result.status, 0, `${result.stdout}\n${result.stderr}`);
  assert.ok(fs.existsSync(path.join(stateRoot, "bot-a", "id_rsa")));
  assert.ok(fs.existsSync(path.join(stateRoot, "bot-a", "inject.env")));
  assert.equal(fs.existsSync(path.join(stateRoot, "inject.env")), false, "identity snapshot must never be shared at the root");
});

test("provision-worker: replaces an orphaned legacy keypair before creating an instance", () => {
  const sb = setupSandbox({
    keypairs: [{ keyPairName: "worker-key-bot-a", keyPairID: "kp-legacy" }],
  });
  const stateRoot = path.join(sb.sandbox, "legacy-tenant-state");
  const r = run(
    sb,
    ["--name", "bot-a", "--login-token", "USERJWT", "--api-key", "BOTKEY", "--image-id", "img-1"],
    { CTYUN_WORKER_STATE_ROOT: toMsys(stateRoot), CTYUN_WORKER_STATE_DIR: "" },
  );
  assert.equal(r.status, 0, `${r.stdout}\n${r.stderr}`);
  assert.match(r.stderr, /replacing orphaned key pair worker-key-bot-a/);

  const state = JSON.parse(fs.readFileSync(sb.statePath, "utf8"));
  assert.deepEqual(state.deletedKeypairs, ["worker-key-bot-a"]);
  assert.equal(state.keypairs.length, 1, "the orphan pair must be replaced, not duplicated");
  assert.equal(state.keypairs[0].keyPairName, "worker-key-bot-a");
  assert.match(state.createArgs, /--keyPairID kp-worker-key-bot-a/);
  assert.ok(fs.existsSync(path.join(stateRoot, "bot-a", "id_rsa")));
});

test("provision-worker: fails closed when an orphaned keypair cannot be replaced", () => {
  const sb = setupSandbox({
    failDeleteKeypair: true,
    keypairs: [{ keyPairName: "worker-key-bot-a", keyPairID: "kp-legacy" }],
  });
  const r = run(sb, ["--name", "bot-a", "--login-token", "USERJWT", "--api-key", "BOTKEY", "--image-id", "img-1"]);
  assert.notEqual(r.status, 0, `${r.stdout}\n${r.stderr}`);
  assert.match(r.stderr, /Tianyi Cloud API failed: E\.DELKP/);

  const state = JSON.parse(fs.readFileSync(sb.statePath, "utf8"));
  assert.equal((state.instances || []).length, 0, "no billable instance may be created without a recoverable key");
  assert.deepEqual(state.keypairs, [{ keyPairName: "worker-key-bot-a", keyPairID: "kp-legacy" }]);
});

test("provision-worker: create failure fails closed and cleans up", () => {
  const sb = setupSandbox({ failCreate: true });
  const r = run(sb, ["--name", "bot-a", "--login-token", "t", "--api-key", "k", "--image-id", "img-1"]);
  assert.notEqual(r.status, 0, `${r.stdout}\n${r.stderr}`);
  assert.match(r.stderr, /Tianyi Cloud API failed|provision failed/);
  const state = JSON.parse(fs.readFileSync(sb.statePath, "utf8"));
  // 失败清理：key pair 删除被调用（实例未创建成功）
  assert.ok((state.deletedKeypairs || []).includes("worker-key-bot-a"), "key pair should be cleaned up");
});

test("provision-worker: monthly billing by default with auto-renew", () => {
  const sb = setupSandbox({});
  const r = run(sb, ["--name", "bot-a", "--login-token", "t", "--api-key", "k",
    "--bot-uid", "42", "--user-uid", "7", "--image-id", "img-1", "--body-id", "b", "--installation-id", "i"]);
  if (r.status !== 0) assert.equal(r.status, 0, r.stderr);
  const state = JSON.parse(fs.readFileSync(sb.statePath, "utf8"));
  assert.match(state.createArgs, /--onDemand false/);
  assert.match(state.createArgs, /--cycleType MONTH/);
  assert.match(state.createArgs, /--cycleCount 1/);
  assert.match(state.createArgs, /--extIP 0/);
  assert.ok(!state.createArgs.includes("--bandwidth"), "private-IP provisioning must not request public bandwidth");
  assert.equal((state.renewCalls || []).length, 1, "auto-renew must be configured once");
  assert.equal(state.renewCalls[0].autoRenewStatus, "1");
  assert.equal(state.renewCalls[0].autoRenewCycleType, "MONTH");
  assert.ok((state.instances || [])[0].expiredTime, "monthly instance should carry expiredTime");
});

test("provision-worker: public IP is an explicit legacy override", () => {
  const sb = setupSandbox({});
  const r = run(sb, ["--name", "bot-a", "--login-token", "t", "--api-key", "k",
    "--bot-uid", "42", "--user-uid", "7", "--image-id", "img-1", "--body-id", "b", "--installation-id", "i"],
    { CTYUN_WORKER_EXT_IP: "1" });
  if (r.status !== 0) assert.equal(r.status, 0, r.stderr);
  const state = JSON.parse(fs.readFileSync(sb.statePath, "utf8"));
  assert.match(state.createArgs, /--extIP 1/);
  assert.match(state.createArgs, /--bandwidth 10/);
});

test("provision-worker: ondemand billing mode keeps on-demand and skips auto-renew", () => {
  const sb = setupSandbox({});
  const r = run(sb, ["--name", "bot-a", "--login-token", "t", "--api-key", "k",
    "--bot-uid", "42", "--user-uid", "7", "--image-id", "img-1", "--body-id", "b", "--installation-id", "i"],
    { CTYUN_WORKER_BILLING_MODE: "ondemand" });
  if (r.status !== 0) assert.equal(r.status, 0, r.stderr);
  const state = JSON.parse(fs.readFileSync(sb.statePath, "utf8"));
  assert.match(state.createArgs, /--onDemand true/);
  assert.ok(!(state.createArgs || "").includes("--cycleType"), "ondemand must not pass cycleType");
  assert.equal((state.renewCalls || []).length, 0, "no auto-renew for on-demand instances");
  assert.ok(!(state.instances || [])[0].expiredTime, "on-demand instance has no expiredTime");
});

test("provision-worker: AUTO_RENEW=0 disables auto-renew for monthly billing", () => {
  const sb = setupSandbox({});
  const r = run(sb, ["--name", "bot-a", "--login-token", "t", "--api-key", "k",
    "--bot-uid", "42", "--user-uid", "7", "--image-id", "img-1", "--body-id", "b", "--installation-id", "i"],
    { CTYUN_WORKER_AUTO_RENEW: "0" });
  if (r.status !== 0) assert.equal(r.status, 0, r.stderr);
  const state = JSON.parse(fs.readFileSync(sb.statePath, "utf8"));
  assert.match(state.createArgs, /--onDemand false/);
  assert.equal((state.renewCalls || []).length, 0, "auto-renew must be skipped");
});

test("provision-worker: auto-renew failure warns but does not fail provisioning", () => {
  const sb = setupSandbox({ failAutoRenew: true });
  const r = run(sb, ["--name", "bot-a", "--login-token", "t", "--api-key", "k",
    "--bot-uid", "42", "--user-uid", "7", "--image-id", "img-1", "--body-id", "b", "--installation-id", "i"]);
  if (r.status !== 0) assert.equal(r.status, 0, `${r.stdout}\n${r.stderr}`);
  assert.match(r.stderr, /warning: auto-renew configuration failed/);
  assert.match(r.stdout, /"status":"provisioned"/);
  const state = JSON.parse(fs.readFileSync(sb.statePath, "utf8"));
  assert.ok(state.injectedEnv, "provisioning must complete despite auto-renew failure");
});

test("provision-worker: post-create failure unsubscribes monthly instance in cleanup", () => {
  const sb = setupSandbox({ failSshAfterCloudInit: true });
  const r = run(sb, ["--name", "bot-a", "--login-token", "t", "--api-key", "k",
    "--bot-uid", "42", "--user-uid", "7", "--image-id", "img-1", "--body-id", "b", "--installation-id", "i"]);
  assert.notEqual(r.status, 0, `${r.stdout}\n${r.stderr}`);
  assert.match(r.stderr, /provision failed/);
  const state = JSON.parse(fs.readFileSync(sb.statePath, "utf8"));
  assert.ok((state.unsubscribedInstances || []).includes("i-worker-bot-a"), "monthly instance must be unsubscribed, not deleted");
  assert.ok(!(state.deletedInstances || []).includes("i-worker-bot-a"), "monthly instance must not go through DeleteEcsInstance");
});

test("provision-worker: cleanup unsubscribes monthly instance when the instance list becomes stale", () => {
  const sb = setupSandbox({ failSshAfterCloudInit: true, hideInstancesAfterSshFailure: true });
  const r = run(sb, ["--name", "bot-a", "--login-token", "t", "--api-key", "k",
    "--bot-uid", "42", "--user-uid", "7", "--image-id", "img-1", "--body-id", "b", "--installation-id", "i"]);
  assert.notEqual(r.status, 0, `${r.stdout}\n${r.stderr}`);
  assert.match(r.stderr, /provision failed/);
  const state = JSON.parse(fs.readFileSync(sb.statePath, "utf8"));
  assert.ok(state.listCalls >= 5, "cleanup should retry the eventually consistent instance list");
  assert.deepEqual(state.unsubscribedInstances, ["i-worker-bot-a"], "known monthly instance ID must still be unsubscribed");
  assert.equal((state.deletedInstances || []).length, 0, "monthly fallback must never use DeleteEcsInstance");
});

test("provision-worker: cleanup deletes on-demand instance when the instance list becomes stale", () => {
  const sb = setupSandbox({ failSshAfterCloudInit: true, hideInstancesAfterSshFailure: true });
  const r = run(sb, ["--name", "bot-a", "--login-token", "t", "--api-key", "k",
    "--bot-uid", "42", "--user-uid", "7", "--image-id", "img-1", "--body-id", "b", "--installation-id", "i"],
    { CTYUN_WORKER_BILLING_MODE: "ondemand" });
  assert.notEqual(r.status, 0, `${r.stdout}\n${r.stderr}`);
  assert.match(r.stderr, /provision failed/);
  const state = JSON.parse(fs.readFileSync(sb.statePath, "utf8"));
  assert.deepEqual(state.deletedInstances, ["i-worker-bot-a"], "known on-demand instance ID must still be deleted");
  assert.equal((state.unsubscribedInstances || []).length, 0, "on-demand fallback must not use UnsubscribeEcsInstance");
});
