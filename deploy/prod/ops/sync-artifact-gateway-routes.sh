#!/usr/bin/env bash
set -Eeuo pipefail

[[ "${CATSCO_ARTIFACT_GATEWAY_ENABLED:-0}" == "1" ]] \
  || { echo '{"ok":true,"status":"disabled"}'; exit 0; }
for command in ctyun-cli jq; do
  command -v "$command" >/dev/null 2>&1 || { echo "error: missing required command: $command" >&2; exit 2; }
done

OPS_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
STATE_ROOT="${CTYUN_WORKER_STATE_ROOT:-/var/lib/catsco-worker}"
REGION_ID="${CTYUN_WORKER_REGION_ID:-}"
PROJECT_ID="${CTYUN_WORKER_PROJECT_ID:-0}"
[[ -n "$REGION_ID" ]] || { echo "error: CTYUN_WORKER_REGION_ID is required" >&2; exit 2; }

ctyun() {
  local raw status
  raw="$(timeout -s TERM -k 15 120s ctyun-cli "$@" --output json 2>&1)" || {
    echo "error: ctyun-cli failed: $*" >&2; echo "$raw" >&2; return 1
  }
  status="$(jq -r '(.statusCode // "") | tostring' <<<"$raw")"
  [[ "$status" == "800" ]] || { echo "error: Tianyi Cloud API failed" >&2; return 1; }
  printf '%s' "$raw"
}

routes='{}'
shopt -s nullglob
for state_dir in "$STATE_ROOT"/*; do
  [[ -d "$state_dir" && -f "$state_dir/inject.env" ]] || continue
  tenant="$(basename "$state_dir")"
  agent_uid="$(sed -n 's/^CATSCO_BOT_UID=//p' "$state_dir/inject.env" | tail -n1)"
  [[ "$agent_uid" =~ ^[1-9][0-9]{0,18}$ ]] || continue
  response="$(ctyun ecs ListEcsInstances --regionID "$REGION_ID" --projectID "$PROJECT_ID" \
    --instanceName "worker-$tenant" --pageNo 1 --pageSize 10)"
  instance="$(jq -c --arg name "worker-$tenant" '.returnObj.results[]? | select(.instanceName == $name)' <<<"$response" | head -n1)"
  [[ -n "$instance" ]] || continue
  state="$(jq -r '.instanceStatus // .state // ""' <<<"$instance" | tr '[:upper:]' '[:lower:]')"
  [[ "$state" == "running" || "$state" == "active" ]] || continue
  private_ip="$(jq -r '(.fixedIPList[0] // .privateIP // "")' <<<"$instance")"
  [[ "$private_ip" =~ ^(10\.|192\.168\.|172\.(1[6-9]|2[0-9]|3[01])\.) ]] || continue
  routes="$(jq -c --arg uid "$agent_uid" --arg ip "$private_ip" '. + {($uid):$ip}' <<<"$routes")"
done

printf '%s\n' "$routes" | "$OPS_DIR/artifact-gateway-route.sh" sync
