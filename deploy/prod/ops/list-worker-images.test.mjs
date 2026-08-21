// Tests for deploy/prod/ops/list-worker-images.sh.
//
// Runs the real bash script through Git Bash with a fake ctyun-cli (and a
// passthrough timeout shim). Requires `jq` on PATH (the server image installs
// it; locally point CATSCO_JQ at a jq.exe or add it to PATH).
import { test } from "node:test";
import * as assert from "node:assert";
import * as fs from "node:fs";
import * as os from "node:os";
import * as path from "node:path";
import { fileURLToPath } from "node:url";
import { spawnSync } from "node:child_process";

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const scriptPath = path.join(__dirname, "list-worker-images.sh");

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
if (args[0] === "ims" && args[1] === "ListImage") {
  const pageNo = parseInt(args[args.indexOf("--pageNo") + 1] || "1", 10);
  const pageSize = parseInt(args[args.indexOf("--pageSize") + 1] || "200", 10);
  const state = JSON.parse(fs.readFileSync(process.env.FAKE_STATE, "utf8"));
  const list = state.images || [];
  const start = (pageNo - 1) * pageSize;
  const totalPage = Math.ceil(list.length / pageSize);
  process.stdout.write(JSON.stringify({
    statusCode: "800",
    message: "SUCCESS",
    returnObj: { images: list.slice(start, start + pageSize), totalPage },
  }));
} else {
  process.stderr.write("unexpected fake op: " + args.join(" "));
  process.exit(2);
}
`;

const FAKE_TIMEOUT = `
import { spawnSync } from "node:child_process";
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
const r = spawnSync(cmd, args.slice(i + 2), { stdio: "inherit" });
process.exit(r.status ?? 1);
`;

function writeCommand(bin, name, body) {
  const p = path.join(bin, name);
  fs.writeFileSync(p, `#!/usr/bin/env node\n${body.trim()}\n`);
  fs.chmodSync(p, 0o755);
  fs.writeFileSync(`${p}.cmd`, `@echo off\r\nnode "%~dp0${name}" %*\r\n`);
}

function runScript(images) {
  const sandbox = fs.mkdtempSync(path.join(os.tmpdir(), "catsco-list-img-"));
  try {
    const bin = path.join(sandbox, "bin");
    fs.mkdirSync(bin);
    writeCommand(bin, "ctyun-cli", FAKE_CTYUN);
    writeCommand(bin, "timeout", FAKE_TIMEOUT);
    const state = path.join(sandbox, "state.json");
    fs.writeFileSync(state, JSON.stringify({ images }));

    // jq on PATH: prefer CATSCO_JQ (a jq.exe) so the script finds it.
    const jqDir = process.env.CATSCO_JQ
      ? path.dirname(process.env.CATSCO_JQ)
      : "";
    const extraPath = [bin, jqDir].filter(Boolean).join(path.delimiter);
    const res = spawnSync(BASH, [scriptPath], {
      cwd: sandbox,
      encoding: "utf8",
      timeout: 30_000,
      env: {
        ...process.env,
        PATH: `${extraPath}${path.delimiter}${process.env.PATH || ""}`,
        CTYUN_WORKER_REGION_ID: "region-test",
        CTYUN_IMAGE_PROJECT_ID: "image-project-test",
        FAKE_STATE: state,
      },
    });
    // Git Bash on Windows emits CRLF; normalize so TSV assertions are stable.
    return {
      status: res.status,
      stdout: (res.stdout || "").replace(/\r/g, ""),
      stderr: (res.stderr || "").replace(/\r/g, ""),
    };
  } finally {
    fs.rmSync(sandbox, { recursive: true, force: true });
  }
}

function workerImage(id, name, version, commit, createdTime, labels = true) {
  return {
    imageID: id,
    imageName: name,
    imageStatus: "active",
    createdTime,
    labels: labels
      ? [
          { labelKey: "bake", labelValue: "b-" + id },
          { labelKey: "version", labelValue: version },
          { labelKey: "commit", labelValue: commit },
        ]
      : [],
  };
}

test("list-worker-images: filters bake channel and emits TSV rows", () => {
  const images = [
    workerImage("img-3", "catsco-worker-1-4-8-a", "1.4.8", "c3", 300),
    workerImage("img-2", "catsco-worker-1-4-7-a", "1.4.7", "c2", 200),
    workerImage("img-1", "catsco-worker-1-4-6-a", "1.4.6", "c1", 100),
    { imageID: "img-other", imageName: "catsco-unrelated", imageStatus: "active", createdTime: 999, labels: [{ labelKey: "bake", labelValue: "bx" }] },
    workerImage("img-nolabel", "catsco-worker-manual", "", "", 500, false),
  ];
  const r = runScript(images);
  assert.equal(r.status, 0, `${r.stdout}\n${r.stderr}`);
  const lines = r.stdout.trim().split("\n").filter(Boolean);
  assert.equal(lines.length, 3, `want 3 rows, got:\n${r.stdout}`);
  assert.match(lines[0], /^img-3\tcatsco-worker-1-4-8-a\t1\.4\.8\tc3\t300\tactive$/);
  assert.match(lines[2], /^img-1\tcatsco-worker-1-4-6-a\t1\.4\.6\tc1\t100\tactive$/);
});

test("list-worker-images: empty result is silent success", () => {
  const r = runScript([]);
  assert.equal(r.status, 0, `${r.stdout}\n${r.stderr}`);
  assert.equal(r.stdout.trim(), "");
});

test("list-worker-images: pagination across two pages", () => {
  const images = [];
  for (let i = 0; i < 250; i++) {
    images.push(workerImage(`img-${String(i).padStart(3, "0")}`, `catsco-worker-1-0-${i}`, "1.0", `c${i}`, 1000 + i));
  }
  const r = runScript(images);
  assert.equal(r.status, 0, `${r.stdout}\n${r.stderr}`);
  const lines = r.stdout.trim().split("\n").filter(Boolean);
  assert.equal(lines.length, 250, `want 250 rows, got ${lines.length}`);
});

test("list-worker-images: API failure fails closed", () => {
  const sandbox = fs.mkdtempSync(path.join(os.tmpdir(), "catsco-list-img-"));
  try {
    const bin = path.join(sandbox, "bin");
    fs.mkdirSync(bin);
    writeCommand(bin, "ctyun-cli", `
import fs from "node:fs";
process.stdout.write(JSON.stringify({ statusCode: "900", errorCode: "E.FAKE", message: "boom", returnObj: {} }));
`);
    writeCommand(bin, "timeout", FAKE_TIMEOUT);
    const state = path.join(sandbox, "state.json");
    fs.writeFileSync(state, "{}");
    const jqDir = process.env.CATSCO_JQ ? path.dirname(process.env.CATSCO_JQ) : "";
    const extraPath = [bin, jqDir].filter(Boolean).join(path.delimiter);
    const res = spawnSync(BASH, [scriptPath], {
      cwd: sandbox,
      encoding: "utf8",
      timeout: 30_000,
      env: {
        ...process.env,
        PATH: `${extraPath}${path.delimiter}${process.env.PATH || ""}`,
        CTYUN_WORKER_REGION_ID: "region-test",
        CTYUN_IMAGE_PROJECT_ID: "image-project-test",
        FAKE_STATE: state,
      },
    });
    const stdout = (res.stdout || "").replace(/\r/g, "");
    const stderr = (res.stderr || "").replace(/\r/g, "");
    assert.notEqual(res.status, 0, `expected failure:\n${stdout}\n${stderr}`);
    assert.match(stderr, /Tianyi Cloud API failed|error/);
  } finally {
    fs.rmSync(sandbox, { recursive: true, force: true });
  }
});

test("list-worker-images: missing image project fails closed", () => {
  const sandbox = fs.mkdtempSync(path.join(os.tmpdir(), "catsco-list-img-"));
  try {
    const bin = path.join(sandbox, "bin");
    fs.mkdirSync(bin);
    const jqDir = process.env.CATSCO_JQ ? path.dirname(process.env.CATSCO_JQ) : "";
    const res = spawnSync(BASH, [scriptPath], {
      cwd: sandbox,
      encoding: "utf8",
      timeout: 30_000,
      env: {
        ...process.env,
        PATH: `${bin}${path.delimiter}${jqDir}${path.delimiter}${process.env.PATH || ""}`,
        CTYUN_WORKER_REGION_ID: "region-test",
        CTYUN_IMAGE_PROJECT_ID: "",
      },
    });
    assert.equal(res.status, 2);
    assert.match(res.stderr || "", /CTYUN_IMAGE_PROJECT_ID is required/);
  } finally {
    fs.rmSync(sandbox, { recursive: true, force: true });
  }
});
