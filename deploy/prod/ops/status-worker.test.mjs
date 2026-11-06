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

function toMsys(value) {
  const match = /^([A-Za-z]):(.*)$/.exec(value);
  if (!match) return value.replace(/\\/g, "/");
  return `/${match[1].toLowerCase()}${match[2].replace(/\\/g, "/")}`;
}

const FAKE_CTYUN = `
import fs from "node:fs";
const args = process.argv.slice(2);
if (args[0] === "ecs" && args[1] === "ListEcsInstances") {
  const pageNo = parseInt(args[args.indexOf("--pageNo") + 1] || "1", 10);
  const pageSize = parseInt(args[args.indexOf("--pageSize") + 1] || "100", 10);
  const region = args[args.indexOf("--regionID") + 1] || "";
  const state = JSON.parse(fs.readFileSync(process.env.FAKE_STATE, "utf8"));
  // Per-region inventories let a test prove that a second region is queried.
  const list = (state.regions && state.regions[region]) || state.instances || [];
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

const FAKE_REMOTE_COMMANDS = `#!/usr/bin/env bash
timeout() {
while [[ $# -gt 0 ]]; do
  case "$1" in
    --*) echo "fake busybox timeout rejects GNU option: $1" >&2; exit 2 ;;
    -s|-k) shift 2 ;;
    -s*|-k*) shift ;;
    *) break ;;
  esac
done
[[ $# -ge 2 ]] || exit 2
shift
"$@"
}

ssh() {
  local arg ip version=""
  for arg in "$@"; do
    [[ "$arg" == root@* ]] || continue
    ip="\${arg#root@}"
    version="$(printf '%s' "\${FAKE_APP_VERSIONS:-{}}" | jq -r --arg ip "$ip" '.[$ip] // empty')"
    break
  done
  [[ -z "$version" ]] || printf '{"version":"%s"}' "$version"
}
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

function runScript(t, { env = {}, fakeState = {}, appVersions = {}, remoteVersions = {}, deployments = {}, ctyunBody = FAKE_CTYUN } = {}) {
  const tmp = fs.mkdtempSync(path.join(fs.realpathSync(path.join(__dirname, "..")), "status-test-"));
  fs.writeFileSync(path.join(tmp, "package.json"), '{"type":"module"}');
  if (t) t.after(() => fs.rmSync(tmp, { recursive: true, force: true }));
  const bin = path.join(tmp, "bin");
  fs.mkdirSync(bin, { recursive: true });
  writeCommand(bin, "ctyun-cli", ctyunBody);
  writeShell(bin, "list-worker-images.sh", FAKE_IMAGES);
  const bashEnv = path.join(tmp, "fake-remote-commands.sh");
  fs.writeFileSync(bashEnv, FAKE_REMOTE_COMMANDS);
  fs.writeFileSync(path.join(tmp, "state.json"), JSON.stringify(fakeState));
  const stateRoot = path.join(tmp, "worker-state");
  fs.mkdirSync(stateRoot);
  for (const [tenant, version] of Object.entries(appVersions)) {
    const tenantDir = path.join(stateRoot, tenant);
    fs.mkdirSync(tenantDir);
    fs.writeFileSync(path.join(tenantDir, "app_version"), `${version}\n`);
  }
  // deployment.json snapshots pin the region a worker was CREATED in. A fleet
  // can span two regions (early workers on the env default, later ones
  // elsewhere), so status must read these or it reports live machines as
  // missing.
  for (const [tenant, snapshot] of Object.entries(deployments)) {
    const tenantDir = path.join(stateRoot, tenant);
    fs.mkdirSync(tenantDir, { recursive: true });
    const body = typeof snapshot === "string"
      ? snapshot
      : JSON.stringify({
          profile: snapshot.profile || "public_ip",
          env: {
            CTYUN_WORKER_REGION_ID: snapshot.region,
            CTYUN_WORKER_PROJECT_ID: snapshot.project ?? "0",
          },
        });
    fs.writeFileSync(path.join(tenantDir, "deployment.json"), body);
  }
  for (const instance of fakeState.instances || []) {
    const ip = env.CTYUN_WORKER_EXT_IP === "1" ? instance.floatingIP : (instance.fixedIPList?.[0] || "");
    if (!remoteVersions[ip] || !instance.instanceName?.startsWith("worker-")) continue;
    const tenantDir = path.join(stateRoot, instance.instanceName.slice("worker-".length));
    fs.mkdirSync(tenantDir, { recursive: true });
    fs.writeFileSync(path.join(tenantDir, "id_rsa"), "fake-key\n");
  }
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
      CTYUN_WORKER_STATE_ROOT: toMsys(stateRoot),
      FAKE_APP_VERSIONS: JSON.stringify(remoteVersions),
      BASH_ENV: toMsys(bashEnv),
      ...env,
    },
    encoding: "utf8",
  });
  return r;
}

const inst = (name, status, imageID, ip = "") => ({
  instanceName: name,
  instanceStatus: status,
  image: { imageID },
  fixedIPList: ip ? [ip] : [],
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
    appVersions: { aaa: "1.4.9", bbb: "1.4.8" },
  });
  assert.equal(r.status, 0, r.stderr);
  const lines = r.stdout.trim().split("\n").filter(Boolean);
  assert.equal(lines.length, 2);
  assert.equal(lines[0], "worker-aaa\trunning\timg-running-1\t1.4.8\t1.4.9\t\t");
  assert.equal(lines[1], "worker-bbb\tcreating\timg-old-2\t1.4.7\t1.4.8");
});

test("status-worker: discovers and caches an old worker's active application version", (t) => {
  const r = runScript(t, {
    fakeState: { instances: [inst("worker-legacy", "running", "img-running-1", "10.0.0.7")] },
    remoteVersions: { "10.0.0.7": "1.4.7" },
  });
  assert.equal(r.status, 0, r.stderr);
  assert.equal(r.stdout.trim(), "worker-legacy\trunning\timg-running-1\t1.4.8\t1.4.7\t10.0.0.7", r.stderr);
});

test("status-worker: public profile probes the EIP instead of the private IP", t => {
  const worker = {...inst("worker-public", "running", "img-running-1", "172.27.7.2"),floatingIP:"8.8.8.8"};
  const r = runScript(t,{env:{CTYUN_WORKER_EXT_IP:"1"},fakeState:{instances:[worker]},remoteVersions:{"8.8.8.8":"1.5.0"}});
  assert.equal(r.status,0,r.stderr);
  assert.equal(r.stdout.trim(),"worker-public\trunning\timg-running-1\t1.4.8\t1.5.0\t172.27.7.2\t8.8.8.8");
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
  assert.ok(line.split("\t").length <= 5);
});

test("status-worker: queries every region a tenant snapshot pins", (t) => {
  // Production reality (2026-11-03): four workers live in the env-default region
  // and nine were created in a second region. Querying only the default made the
  // control plane show the nine as missing, which reads as "machine gone".
  const r = runScript(t, {
    fakeState: {
      regions: {
        "200000002530": [inst("worker-legacy", "running", "img-running-1")],
        "200000004421": [inst("worker-migrated", "running", "img-old-2")],
      },
    },
    deployments: { "bot-migrated": { region: "200000004421", project: "637cfbd046df" } },
  });
  assert.equal(r.status, 0, r.stderr);
  const names = r.stdout.trim().split("\n").filter(Boolean).map(l => l.split("\t")[0]).sort();
  assert.deepEqual(names, ["worker-legacy", "worker-migrated"]);
});

test("status-worker: a snapshot pinned to the default region does not duplicate it", (t) => {
  const r = runScript(t, {
    fakeState: { regions: { "200000002530": [inst("worker-solo", "running", "img-running-1")] } },
    deployments: { "bot-solo": { region: "200000002530", project: "0" } },
  });
  assert.equal(r.status, 0, r.stderr);
  const lines = r.stdout.trim().split("\n").filter(Boolean);
  assert.equal(lines.length, 1, "the same region must not be queried twice");
});

test("status-worker: ignores a malformed or hostile deployment snapshot", (t) => {
  // deployment.json is input on a trust boundary. A corrupt file must not crash
  // the snapshot, and a path-like region must not be forwarded to the CLI.
  const r = runScript(t, {
    fakeState: { regions: { "200000002530": [inst("worker-ok", "running", "img-running-1")] } },
    deployments: {
      "bot-broken": "{not json",
      "bot-hostile": { region: "../../etc/passwd", project: "0" },
      "bot-empty": { region: "", project: "0" },
    },
  });
  assert.equal(r.status, 0, r.stderr);
  const names = r.stdout.trim().split("\n").filter(Boolean).map(l => l.split("\t")[0]);
  assert.deepEqual(names, ["worker-ok"]);
});
