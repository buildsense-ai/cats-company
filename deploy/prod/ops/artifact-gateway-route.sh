#!/usr/bin/env bash
set -Eeuo pipefail

ACTION="${1:-}"
AGENT_UID="${2:-}"
PRIVATE_IP="${3:-}"
NETWORK_MODE="${4:-private}"
OPS_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

case "$ACTION" in
  register)
    [[ "$AGENT_UID" =~ ^[1-9][0-9]{0,18}$ ]] || { echo "error: invalid Agent UID" >&2; exit 2; }
    node "$OPS_DIR/artifact-gateway-route.mjs" validate-ip "$PRIVATE_IP" "$NETWORK_MODE" >/dev/null
    ;;
  remove|status)
    [[ "$AGENT_UID" =~ ^[1-9][0-9]{0,18}$ ]] || { echo "error: invalid Agent UID" >&2; exit 2; }
    ;;
  sync) ;;
  *) echo "usage: artifact-gateway-route.sh <register UID PRIVATE_IP|remove UID|status UID|sync>" >&2; exit 2 ;;
esac

[[ "${CATSCO_ARTIFACT_GATEWAY_ENABLED:-0}" == "1" ]] \
  || { echo "error: Artifact gateway is not enabled" >&2; exit 1; }

JUMP_IP="${CATSCO_ARTIFACT_GATEWAY_SSH_IP:-${CTYUN_JUMP_IP:-}}"
JUMP_PORT="${CATSCO_ARTIFACT_GATEWAY_SSH_PORT:-${CTYUN_JUMP_PORT:-22}}"
JUMP_USER="${CATSCO_ARTIFACT_GATEWAY_SSH_USER:-${CTYUN_JUMP_USER:-root}}"
JUMP_KEY="${CATSCO_ARTIFACT_GATEWAY_SSH_KEY:-${CTYUN_JUMP_KEY:-/var/lib/catsco-worker/jump_host_ed25519}}"
STATE_ROOT="${CTYUN_WORKER_STATE_ROOT:-/var/lib/catsco-worker}"
REMOTE_COMMAND="${CATSCO_ARTIFACT_GATEWAY_ROUTE_COMMAND:-/usr/local/sbin/catsco-artifact-route}"

[[ -n "$JUMP_IP" && -f "$JUMP_KEY" ]] \
  || { echo "error: jump host endpoint or key is unavailable" >&2; exit 1; }
mkdir -p "$STATE_ROOT"
chmod 600 "$JUMP_KEY"

ssh_opts=(-i "$JUMP_KEY" -p "$JUMP_PORT" -o BatchMode=yes -o ConnectTimeout=10
  -o ServerAliveInterval=15 -o ServerAliveCountMax=3
  -o StrictHostKeyChecking=accept-new
  -o UserKnownHostsFile="$STATE_ROOT/artifact_gateway_known_hosts")

remote=("$REMOTE_COMMAND" "$ACTION")
[[ -z "$AGENT_UID" ]] || remote+=("$AGENT_UID")
[[ -z "$PRIVATE_IP" ]] || remote+=("$PRIVATE_IP" "$NETWORK_MODE")

timeout -s TERM -k 15 90s ssh "${ssh_opts[@]}" "$JUMP_USER@$JUMP_IP" "${remote[*]}"
