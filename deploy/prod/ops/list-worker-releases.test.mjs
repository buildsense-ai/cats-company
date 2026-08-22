import { test } from "node:test";
import * as assert from "node:assert";
import * as fs from "node:fs";
import * as os from "node:os";
import * as path from "node:path";
import { fileURLToPath } from "node:url";
import { spawnSync } from "node:child_process";

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const scriptPath = path.join(__dirname, "list-worker-releases.sh");

function bashPath() {
  if (process.platform === "win32") {
    for (const candidate of ["C:\\Program Files\\Git\\bin\\bash.exe", "C:\\Program Files\\Git\\usr\\bin\\bash.exe"]) {
      if (fs.existsSync(candidate)) return candidate;
    }
  }
  return "bash";
}

function toMsys(value) {
  const match = /^([A-Za-z]):(.*)$/.exec(value);
  if (!match) return value.replace(/\\/g, "/");
  return `/${match[1].toLowerCase()}${match[2].replace(/\\/g, "/")}`;
}

test("list-worker-releases: emits only release commit markers", () => {
  const sandbox = fs.mkdtempSync(path.join(os.tmpdir(), "catsco-release-list-"));
  const bin = path.join(sandbox, "bin");
  fs.mkdirSync(bin);
  const fakeFetch = path.join(bin, "tos-fetch");
  fs.writeFileSync(fakeFetch, "#!/usr/bin/env bash\nprintf '%s\\n' \"$FAKE_TOS_LIST\"\n");
  fs.chmodSync(fakeFetch, 0o755);

  const listing = [
    "update/worker/1.4.9/manifest.json\t1787066647",
    "update/worker/1.4.9/catsco-worker.tar.gz\t1787066600",
    "update/worker/1.4.8/manifest.json\t1786066647",
    "update/worker/bad/version/manifest.json\t1788066647",
    "update/desktop/1.4.9/manifest.json\t1787066647",
  ].join("\n");
  const command = `export PATH="${toMsys(bin)}:$PATH"; exec "${toMsys(scriptPath)}"`;
  const result = spawnSync(bashPath(), ["-c", command], {
    encoding: "utf8",
    env: { ...process.env, CATSCO_WORKER_ARTIFACT_BUCKET: "worker-private-test", FAKE_TOS_LIST: listing },
  });

  assert.equal(result.status, 0, result.stderr);
  assert.equal(result.stdout.replace(/\r/g, ""), "1.4.9\t1787066647\n1.4.8\t1786066647\n");
});
