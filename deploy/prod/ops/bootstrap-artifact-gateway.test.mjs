import assert from "node:assert/strict";
import fs from "node:fs";
import test from "node:test";

const script = fs.readFileSync(
  new URL("./bootstrap-artifact-gateway.sh", import.meta.url),
  "utf8",
);

test("gateway forwards the worker health contract for registered Agent hosts", () => {
  const healthStart = script.indexOf("location = /__artifact_health {");
  const artifactStart = script.indexOf("location /artifacts/ {");
  const fallbackStart = script.indexOf("location / { return 404; }");

  assert.ok(healthStart >= 0, "worker health location is missing");
  assert.ok(healthStart < artifactStart, "worker health must be an exact route before Artifact files");
  assert.ok(healthStart < fallbackStart, "worker health must not fall through to the 404 location");

  const healthBlock = script.slice(healthStart, artifactStart);
  assert.match(healthBlock, /\$catsco_artifact_route_found = 0/);
  assert.match(healthBlock, /proxy_pass http:\/\/\\\$catsco_artifact_backend/);
});
