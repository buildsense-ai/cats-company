#!/usr/bin/env bash
# Publish only the route manager from the deployed server image. Existing DNS,
# certificates, registry, Nginx configuration and routes are preserved.
set -Eeuo pipefail
[[ "${CATSCO_ARTIFACT_GATEWAY_ENABLED:-0}" == 1 ]] || exit 0
OPS_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
GATEWAY_IP="${CTYUN_JUMP_IP:-}"
GATEWAY_KEY="${CTYUN_JUMP_KEY:-/var/lib/catsco-worker/jump_host_ed25519}"
STATE_ROOT="${CTYUN_WORKER_STATE_ROOT:-/var/lib/catsco-worker}"
[[ -n "$GATEWAY_IP" && -f "$GATEWAY_KEY" ]] || { echo 'error: Artifact gateway SSH configuration missing' >&2; exit 1; }
sha="$(sha256sum "$OPS_DIR/artifact-gateway-route.mjs" | cut -d' ' -f1)"
[[ "$sha" =~ ^[0-9a-f]{64}$ ]] || exit 1
remote_root=/usr/local/lib/catsco-artifact-gateway
target="$remote_root/artifact-gateway-route.mjs"
temporary="$remote_root/route-$sha.mjs"
ssh_opts=(-i "$GATEWAY_KEY" -p "${CTYUN_JUMP_PORT:-22}" -o BatchMode=yes -o ConnectTimeout=10
  -o StrictHostKeyChecking=accept-new -o UserKnownHostsFile="$STATE_ROOT/artifact_gateway_known_hosts")
timeout -s TERM -k 10 60s ssh "${ssh_opts[@]}" "${CTYUN_JUMP_USER:-root}@$GATEWAY_IP" \
  "test -f '$target' && cat > '$temporary' && echo '$sha  $temporary' | sha256sum -c - && /usr/bin/node --check '$temporary' && /usr/bin/node '$temporary' render >/dev/null && chmod 0755 '$temporary' && mv -f '$temporary' '$target'" \
  < "$OPS_DIR/artifact-gateway-route.mjs"
