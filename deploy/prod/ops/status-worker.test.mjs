// Tests for deploy/prod/ops/status-worker.sh.
//
// Runs the real bash script through Git Bash with a fake ctyun-cli and a fake
// list-worker-images.sh. Requires `jq` on PATH (locally point CATSCO_JQ at a
// jq.exe or add it to PATH).
import { test } from "node:test";
import * as assert from "node:assert";
import * as fs from "node:fs";
import * as path from "node:path";
import { fileURLToPath } from "node:url";
import { spawnSync } from "node:child_process";

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const scriptPath = path.join(__dirname, "status-worker.sh");

function bashPath() {
  if (process.platform === "win32") {
    const candidates = [
      "C:\\Program Files\\Git\\bin\\bash.exe",
      "C:\\Program Files\\Git\\usr\\bin\\bash.exe",
    ];
    for (const c of candidates) {
      if (fs.existsSync(c)) return c;
    }
  }
  return "bash";
}

const BASH = bashPath();

const FAKE_CTYUN = `
import fs from "node:fs";
const args = process.argv.slice(2);
if (args[0] === "ecs" && args[1] === "ListEcsInstances") {
  const pageNo = parseInt(args[args.indexOf("--pageNo") + 1] || "1", 10);
  const pageSize = parseInt(args[args.indexOf("--pageSize") + 1] || "100", 10);
  const state = JSON.parse(fs.readFileSync(process.env.FAKE_STATE, "utf8"));
  const list = state.instances || [];
  const start = (pageNo - 1) * pageSize;
  const totalPage = Math.ceil(list.length / pageSize);
  process.stdout.write(JSON.stringify({
    statusCode: "800",
    message: "SUCCESS",
    returnObj: {
      results: list.slice(start, start + pageSize),
      totalPage,
    },
  }));
} else {
  process.stderr.write("unexpected fake op: " + args.join(" "));
  process.exit(2);
}
`;

const FAKE_IMAGES = `#!/usr/bin/env bash
# fake list-worker-images.sh: emits imageID<TAB>name<TAB>version<TAB>commit<TAB>createdTime<TAB>status
cat <<'EOF'
img-running-1	catsco-worker-1-4-8-f3f1f3e6	1.4.8	f3f1f3e6	1786066647	active
img-old-2	catsco-worker-1-4-7-abcd1234	1.4.7	abcd1234	1785066647	active
EOF
`;

const FAKE_TIMEOUT = `
import { spawnSync } from "node:child_process";
const args = process.argv.slice(2);
let i = 0;
while (i < args.length) {
  const a = args[i];
  if (a.startsWith("--")) {
    process.stderr.write("fake busybox timeout rejects GNU option: " + a + "\\n");
    process.exit(2);
  }
  if (a === "-s" || a === "-k") { i += 2; continue; }
  if (a.startsWith("-s") || a.startsWith("-k")) { i += 1; continue; }
  break;
}
if (i >= args.length || i + 1 >= args.length) process.exit(2);
const cmd = args[i + 1];
const r = spawnSync(cmd, args.slice(i + 2), { stdio: "inherit" });
process.exit(r.status ?? 1);
`;

function writeCommand(bin, name, body) {
  const p = path.join(bin, name);
  fs.writeFileSync(p, `#!/usr/bin/env node\n${body.trim()}\n`);
  fs.chmodSync(p, 0o755);
  fs.writeFileSync(`${p}.cmd`, `@echo off\r\nnode "%~dp0${name}" %*\r\n`);
}

function writeShell(bin, name, body) {
  const p = path.join(bin, name);
  fs.writeFileSync(p, body);
  fs.chmodSync(p, 0o755);
}

function runScript(t, { env = {}, fakeState = {}, ctyunBody = FAKE_CTYUN } = {}) {
  const tmp = fs.mkdtempSync(path.join(fs.realpathSync(path.join(__dirname, "..")), "status-test-"));
  if (t) t.after(() => fs.rmSync(tmp, { recursive: true, force: true }));
  const bin = path.join(tmp, "bin");
  fs.mkdirSync(bin, { recursive: true });
  writeCommand(bin, "ctyun-cli", ctyunBody);
  writeShell(bin, "list-worker-images.sh", FAKE_IMAGES);
  writeCommand(bin, "timeout", FAKE_TIMEOUT);
  fs.writeFileSync(path.join(tmp, "state.json"), JSON.stringify(fakeState));
  const gitBin = process.platform === "win32"
    ? path.dirname(BASH)
    : "";
  const jqDir = process.env.CATSCO_JQ ? path.dirname(process.env.CATSCO_JQ) : "";
  const r = spawnSync(BASH, [scriptPath], {
    cwd: tmp,
    env: {
      ...process.env,
      PATH: [bin, jqDir, gitBin, process.env.PATH].filter(Boolean).join(path.delimiter),
      FAKE_STATE: path.join(tmp, "state.json"),
      CTYUN_WORKER_REGION_ID: "200000002530",
      CTYUN_WORKER_PROJECT_ID: "0",
      CTYUN_IMAGE_PROJECT_ID: "0",
      ...env,
    },
    encoding: "utf8",
  });
  return r;
}

const inst = (name, status, imageID) => ({
  instanceName: name,
  instanceStatus: status,
  image: { imageID },
});

test("status-worker: emits TSV with version joined from bake images", (t) => {
  const r = runScript(t, {
    fakeState: {
      instances: [
        inst("worker-aaa", "running", "img-running-1"),
        inst("worker-bbb", "creating", "img-old-2"),
        inst("somebody-else", "running", "img-running-1"), // filtered out
      ],
    },
  });
  assert.equal(r.status, 0, r.stderr);
  const lines = r.stdout.trim().split("\n").filter(Boolean);
  assert.equal(lines.length, 2);
  assert.equal(lines[0], "worker-aaa\trunning\timg-running-1\t1.4.8");
  assert.equal(lines[1], "worker-bbb\tcreating\timg-old-2\t1.4.7");
});

test("status-worker: no workers yields empty output and exit 0", (t) => {
  const r = runScript(t, { fakeState: { instances: [] } });
  assert.equal(r.status, 0, r.stderr);
  assert.equal(r.stdout.trim(), "");
});

test("status-worker: paginates across pages", (t) => {
  const many = [];
  for (let i = 0; i < 120; i += 1) {
    many.push(inst(`worker-p${i}`, "running", "img-running-1"));
  }
  const r = runScript(t, { fakeState: { instances: many } });
  assert.equal(r.status, 0, r.stderr);
  const lines = r.stdout.trim().split("\n").filter(Boolean);
  assert.equal(lines.length, 120);
  assert.ok(lines[0].startsWith("worker-p0\trunning\t"));
  assert.ok(lines[119].startsWith("worker-p119\trunning\t"));
});

test("status-worker: ctyun failure fails closed with non-zero exit", (t) => {
  const failing = FAKE_CTYUN.replace(
    'process.stdout.write(JSON.stringify({',
    'process.stderr.write("boom"); process.exit(3); process.stdout.write(JSON.stringify({',
  );
  const r = runScript(t, { fakeState: { instances: [] }, ctyunBody: failing });
  assert.notEqual(r.status, 0);
  assert.match(r.stderr, /ctyun-cli failed|Tianyi Cloud API failed/);
});

test("status-worker: instance without imageID omits trailing empty columns", (t) => {
  const r = runScript(t, {
    fakeState: { instances: [inst("worker-noid", "running", "")] },
  });
  assert.equal(r.status, 0, r.stderr);
  // bash read drops trailing empty fields; the consumer (Go strings.Split)
  // treats missing columns as empty — status must be intact.
  const line = r.stdout.trim();
  assert.equal(line.split("\t")[0], "worker-noid");
  assert.equal(line.split("\t")[1], "running");
  assert.ok(line.split("\t").length <= 4);
});
