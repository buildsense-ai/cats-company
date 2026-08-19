import { test } from "node:test";
import * as assert from "node:assert";
import { createHash } from "node:crypto";
import * as fs from "node:fs";
import * as os from "node:os";
import * as path from "node:path";
import { fileURLToPath } from "node:url";
import { spawnSync } from "node:child_process";

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const scriptPath = path.join(__dirname, "deploy-worker-version.sh");
const COMMIT = "0123456789abcdef0123456789abcdef01234567";

function bashPath() {
  if (process.platform === "win32") {
    for (const candidate of ["C:\\Program Files\\Git\\bin\\bash.exe", "C:\\Program Files\\Git\\usr\\bin\\bash.exe"]) {
      if (fs.existsSync(candidate)) return candidate;
    }
  }
  return "bash";
}
const BASH = bashPath();

function toMsys(value) {
  const match = /^([A-Za-z]):(.*)$/.exec(value);
  if (!match) return value.replace(/\\/g, "/");
  return `/${match[1].toLowerCase()}${match[2].replace(/\\/g, "/")}`;
}

function writeCommand(bin, name, body) {
  const command = path.join(bin, name);
  fs.writeFileSync(command, `#!/usr/bin/env node\n${body.trim()}\n`);
  fs.chmodSync(command, 0o755);
  fs.writeFileSync(`${command}.cmd`, `@echo off\r\nnode "%~dp0${name}" %*\r\n`);
}

const FAKE_CTYUN = `
const args = process.argv.slice(2);
const value = flag => { const i = args.indexOf(flag); return i >= 0 ? args[i + 1] : ""; };
if (args.slice(0, 2).join(" ") !== "ecs ListEcsInstances") process.exit(2);
const name = value("--instanceName");
process.stdout.write(JSON.stringify({
  statusCode: "800",
  returnObj: { results: name === "worker-bot-a" ? [{ instanceName: name, fixedIPList: ["10.0.0.9"] }] : [] },
}));
`;

const FAKE_SSH = `
import fs from "node:fs";
const args = process.argv.slice(2);
const remoteIndex = args.findIndex(value => value.startsWith("root@"));
if (remoteIndex < 0) process.exit(0);
const command = args.slice(remoteIndex + 1).join(" ");
const state = JSON.parse(fs.readFileSync(process.env.FAKE_STATE, "utf8"));
state.sshCalls = state.sshCalls || [];
state.sshCalls.push(command);
if (command.includes("readlink -f /opt/catsco/current") && command.includes("systemctl is-active --quiet")) {
  fs.writeFileSync(process.env.FAKE_STATE, JSON.stringify(state));
  process.exit(state.currentRelease ? 0 : 1);
}
if (command.includes("test -f") && command.includes("worker-release.json")) {
  fs.writeFileSync(process.env.FAKE_STATE, JSON.stringify(state));
  process.exit(state.localRelease ? 0 : 1);
}
if (command.includes("ln -sfn") && command.includes("systemctl restart")) {
  state.localActivations = (state.localActivations || 0) + 1;
}
if (command.includes("update-worker-artifact.sh") || command.includes("catsco-version-bot-a")) {
  state.updateCalls = (state.updateCalls || 0) + 1;
}
fs.writeFileSync(process.env.FAKE_STATE, JSON.stringify(state));
process.exit(0);
`;

const FAKE_SCP = `
import fs from "node:fs";
const state = JSON.parse(fs.readFileSync(process.env.FAKE_STATE, "utf8"));
state.scpCalls = (state.scpCalls || 0) + 1;
fs.writeFileSync(process.env.FAKE_STATE, JSON.stringify(state));
`;

const FAKE_TOS_FETCH = `
import fs from "node:fs";
const args = process.argv.slice(2);
const value = flag => { const i = args.indexOf(flag); return i >= 0 ? args[i + 1] : ""; };
const output = value("--output");
const key = value("--key");
const state = JSON.parse(fs.readFileSync(process.env.FAKE_STATE, "utf8"));
state.manifestDownloads = state.manifestDownloads || 0;
state.artifactDownloads = state.artifactDownloads || 0;
state.tosFetchArgs = state.tosFetchArgs || [];
state.tosFetchArgs.push(args);
if (key.endsWith("/manifest.json")) {
  state.manifestDownloads += 1;
  fs.writeFileSync(output, JSON.stringify({
    version: "1.4.9",
    commit: process.env.FIXTURE_COMMIT,
    sha256: process.env.FIXTURE_SHA,
    artifactFile: "catsco-worker-1.4.9.tar.gz",
  }));
} else {
  state.artifactDownloads += 1;
  fs.copyFileSync(process.env.FIXTURE_ARTIFACT, output);
}
fs.writeFileSync(process.env.FAKE_STATE, JSON.stringify(state));
`;

const FAKE_TIMEOUT = `
import { spawnSync } from "node:child_process";
import path from "node:path";
import fs from "node:fs";
let args = process.argv.slice(2);
let i = 0;
while (i < args.length) {
  if (args[i] === "-s" || args[i] === "-k") { i += 2; continue; }
  if (args[i].startsWith("-s") || args[i].startsWith("-k")) { i += 1; continue; }
  break;
}
const command = args[i + 1];
const rest = args.slice(i + 2);
if (process.platform === "win32") {
  const localCommand = path.join(path.dirname(process.argv[1]), command);
  if (fs.existsSync(localCommand) && !path.extname(localCommand)) {
    const result = spawnSync(process.execPath, [localCommand, ...rest], { stdio: "inherit" });
    process.exit(result.status ?? 1);
  }
}
const result = spawnSync(command, rest, { stdio: "inherit" });
process.exit(result.status ?? 1);
`;

function setupSandbox(state = {}) {
  const sandbox = fs.mkdtempSync(path.join(os.tmpdir(), "catsco-deploy-version-"));
  const bin = path.join(sandbox, "bin");
  const stateDir = path.join(sandbox, "state");
  const cacheDir = path.join(sandbox, "cache");
  fs.mkdirSync(bin);
  fs.mkdirSync(stateDir);
  fs.writeFileSync(path.join(stateDir, "id_rsa"), "fake-key\n");
  writeCommand(bin, "ctyun-cli", FAKE_CTYUN);
  writeCommand(bin, "ssh", FAKE_SSH);
  writeCommand(bin, "scp", FAKE_SCP);
  writeCommand(bin, "tos-fetch", FAKE_TOS_FETCH);
  writeCommand(bin, "timeout", FAKE_TIMEOUT);

  const listReleases = path.join(sandbox, "list-releases.sh");
  fs.writeFileSync(listReleases, "#!/usr/bin/env sh\nprintf '%s\\n' '1.4.8\t100' '1.4.9\t200'\n");
  fs.chmodSync(listReleases, 0o755);

  const fixtureRoot = path.join(sandbox, "fixture");
  const updaterDir = path.join(fixtureRoot, "app", "scripts");
  fs.mkdirSync(updaterDir, { recursive: true });
  fs.writeFileSync(path.join(updaterDir, "update-worker-artifact.sh"), "#!/usr/bin/env bash\nexit 0\n");
  const artifact = path.join(sandbox, "catsco-worker-1.4.9.tar.gz");
  const tar = spawnSync(BASH, ["-c", `tar -czf '${toMsys(artifact)}' -C '${toMsys(fixtureRoot)}' app`], { encoding: "utf8" });
  assert.equal(tar.status, 0, tar.stderr);
  const sha = createHash("sha256").update(fs.readFileSync(artifact)).digest("hex");

  const statePath = path.join(sandbox, "state.json");
  fs.writeFileSync(statePath, JSON.stringify(state));
  const jqDir = process.env.CATSCO_JQ ? path.dirname(process.env.CATSCO_JQ) : "";
  const commandPath = [bin, jqDir, process.env.PATH || ""].filter(Boolean).join(path.delimiter);
  return {
    sandbox,
    bin,
    stateDir,
    cacheDir,
    statePath,
    env: {
      ...process.env,
      PATH: commandPath,
      CTYUN_WORKER_REGION_ID: "region-test",
      CTYUN_WORKER_STATE_DIR: toMsys(stateDir),
      CATSCO_WORKER_RELEASES_SCRIPT: toMsys(listReleases),
      CATSCO_WORKER_ARTIFACT_CACHE_DIR: toMsys(cacheDir),
      CATSCO_WORKER_ARTIFACT_BUCKET: "worker-private-test",
      CATSCO_WORKER_ARTIFACT_PREFIX: "update/worker",
      CATSCO_WORKER_ARTIFACT_REGION: "cn-test",
      CATSCO_WORKER_ARTIFACT_ENDPOINT: "https://tos-cn-test.example",
      CATSCO_WORKER_ARTIFACT_ACCESS_KEY_ID: "test-access-key",
      CATSCO_WORKER_ARTIFACT_SECRET_ACCESS_KEY: "test-secret-key",
      FAKE_STATE: statePath,
      FIXTURE_ARTIFACT: artifact,
      FIXTURE_COMMIT: COMMIT,
      FIXTURE_SHA: sha,
    },
  };
}

function run(sb, args) {
  const command = `export PATH="${toMsys(sb.bin)}:$PATH"; exec "${toMsys(scriptPath)}" "$@"`;
  const result = spawnSync(BASH, ["-c", command, "bash", ...args], {
    cwd: sb.sandbox,
    env: sb.env,
    encoding: "utf8",
    timeout: 60_000,
  });
  return { status: result.status, stdout: result.stdout || "", stderr: result.stderr || "" };
}

function readState(sb) {
  return JSON.parse(fs.readFileSync(sb.statePath, "utf8"));
}

test("deploy-worker-version: omitted version selects the newest published application release", () => {
  const sb = setupSandbox();
  const result = run(sb, ["--name", "bot-a", "--dry-run"]);
  assert.equal(result.status, 0, result.stderr);
  assert.match(result.stdout, /"version":"1\.4\.9"/);
  assert.equal(readState(sb).manifestDownloads || 0, 0);
});

test("deploy-worker-version: explicit application version does not require a matching image", () => {
  const sb = setupSandbox({ localRelease: false });
  sb.env.CATSCO_WORKER_RELEASES_SCRIPT = toMsys(path.join(sb.sandbox, "missing-list-releases.sh"));

  const result = run(sb, ["--name", "bot-a", "--version", "1.4.9"]);
  assert.equal(result.status, 0, `${result.stdout}\n${result.stderr}`);
  assert.match(result.stdout, /"status":"updated"/);
  assert.equal(readState(sb).manifestDownloads, 1);
});

test("deploy-worker-version: reuses an existing worker-local release", () => {
  const sb = setupSandbox({ localRelease: true });
  const result = run(sb, ["--name", "bot-a", "--version", "1.4.9"]);
  assert.equal(result.status, 0, `${result.stdout}\n${result.stderr}`);
  assert.match(result.stdout, /reused-local-release/);
  const state = readState(sb);
  assert.equal(state.localActivations, 1);
  assert.equal(state.artifactDownloads || 0, 0);
  assert.equal(state.scpCalls || 0, 0);
});

test("deploy-worker-version: current active release is a no-op", () => {
  const sb = setupSandbox({ currentRelease: true, localRelease: true });
  const result = run(sb, ["--name", "bot-a", "--version", "1.4.9"]);
  assert.equal(result.status, 0, `${result.stdout}\n${result.stderr}`);
  assert.match(result.stdout, /already-current/);
  const state = readState(sb);
  assert.equal(state.localActivations || 0, 0, "same version must not restart the service");
  assert.equal(state.artifactDownloads || 0, 0);
  assert.equal(state.scpCalls || 0, 0);
});

test("deploy-worker-version: lazily downloads once and reuses the shared cache", () => {
  const sb = setupSandbox({ localRelease: false });
  const first = run(sb, ["--name", "bot-a", "--version", "1.4.9"]);
  assert.equal(first.status, 0, `${first.stdout}\n${first.stderr}`);
  assert.equal(readState(sb).artifactDownloads, 1);

  const second = run(sb, ["--name", "bot-a", "--version", "1.4.9"]);
  assert.equal(second.status, 0, `${second.stdout}\n${second.stderr}`);
  const state = readState(sb);
  assert.equal(state.artifactDownloads, 1, "valid shared artifact must not be downloaded twice");
  assert.equal(state.scpCalls, 4, "each uncached worker install transfers artifact + updater");
  assert.deepEqual(
    state.tosFetchArgs.slice(0, 2).map(args => args[args.indexOf("--key") + 1]),
    [
      "update/worker/1.4.9/manifest.json",
      "update/worker/1.4.9/catsco-worker-1.4.9.tar.gz",
    ],
  );
  assert.equal(JSON.stringify(state.tosFetchArgs).includes("test-secret-key"), false, "credentials must not enter argv");
});

test("deploy-worker-version: corrupted shared cache is downloaded again", () => {
  const sb = setupSandbox({ localRelease: false });
  const first = run(sb, ["--name", "bot-a", "--version", "1.4.9"]);
  assert.equal(first.status, 0, first.stderr);
  const cached = path.join(sb.cacheDir, "1.4.9", "catsco-worker-1.4.9.tar.gz");
  fs.writeFileSync(cached, "corrupted");

  const second = run(sb, ["--name", "bot-a", "--version", "1.4.9"]);
  assert.equal(second.status, 0, `${second.stdout}\n${second.stderr}`);
  assert.equal(readState(sb).artifactDownloads, 2);
});
