#!/usr/bin/env bash
set -Eeuo pipefail

WORKER_IP=""
AGENT_UID=""
SSH_KEY=""
KNOWN_HOSTS=""

while (($#)); do
  case "$1" in
    --worker-ip) WORKER_IP="${2:-}"; shift 2 ;;
    --agent-uid) AGENT_UID="${2:-}"; shift 2 ;;
    --ssh-key) SSH_KEY="${2:-}"; shift 2 ;;
    --known-hosts) KNOWN_HOSTS="${2:-}"; shift 2 ;;
    *) echo "error: unknown argument: $1" >&2; exit 2 ;;
  esac
done

OPS_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
NETWORK_MODE=private
[[ "${CTYUN_WORKER_EXT_IP:-0}" != 1 ]] || NETWORK_MODE=public
node "$OPS_DIR/artifact-gateway-route.mjs" validate-ip "$WORKER_IP" "$NETWORK_MODE" >/dev/null
[[ "$AGENT_UID" =~ ^[1-9][0-9]{0,18}$ ]] \
  || { echo "error: --agent-uid must be a positive integer" >&2; exit 2; }
[[ -f "$SSH_KEY" ]] || { echo "error: worker SSH key is unavailable" >&2; exit 2; }
[[ -n "$KNOWN_HOSTS" ]] || { echo "error: --known-hosts is required" >&2; exit 2; }

OPS_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
STATIC_SOURCE="$OPS_DIR/artifact-static-host.mjs"
ROUTE_CLIENT="$OPS_DIR/artifact-gateway-route.sh"
[[ -f "$STATIC_SOURCE" && -x "$ROUTE_CLIENT" ]] \
  || { echo "error: Artifact worker assets are incomplete" >&2; exit 1; }

JUMP_IP="${CTYUN_JUMP_IP:-}"
JUMP_PORT="${CTYUN_JUMP_PORT:-22}"
JUMP_USER="${CTYUN_JUMP_USER:-root}"
JUMP_KEY="${CTYUN_JUMP_KEY:-/var/lib/catsco-worker/jump_host_ed25519}"
DATA_DIR="${CATSCO_WORKER_ARTIFACT_DATA_DIR:-${CATSCO_ARTIFACT_DATA_DIR:-/srv/catsco-agent/.local/share/catsco/cloud-html-artifact}}"
BACKEND_PORT="${CATSCO_ARTIFACT_BACKEND_PORT:-19990}"
[[ "$BACKEND_PORT" =~ ^[1-9][0-9]{0,4}$ && "$BACKEND_PORT" -le 65535 ]] \
  || { echo "error: CATSCO_ARTIFACT_BACKEND_PORT is invalid" >&2; exit 2; }
STATIC_ROOT="$DATA_DIR/artifacts"
RUNTIME_DIR="/opt/catsco/artifact-host"
RUNTIME_FILE="$RUNTIME_DIR/artifact-static-host.mjs"
SERVICE_NAME="catsco-artifact-host.service"
NODE_PATH="/opt/catsco/current/runtime/node/bin/node"

chmod 600 "$SSH_KEY"
mkdir -p "$(dirname "$KNOWN_HOSTS")"
ssh_opts=(-i "$SSH_KEY" -o BatchMode=yes -o ConnectTimeout=10
  -o ServerAliveInterval=15 -o ServerAliveCountMax=3
  -o StrictHostKeyChecking=accept-new -o UserKnownHostsFile="$KNOWN_HOSTS")
if [[ -n "$JUMP_IP" ]]; then
  [[ -f "$JUMP_KEY" ]] || { echo "error: jump host SSH key is unavailable" >&2; exit 2; }
  chmod 600 "$JUMP_KEY"
  ssh_opts+=(-o "ProxyCommand=ssh -i ${JUMP_KEY} -p ${JUMP_PORT} -o BatchMode=yes -o ConnectTimeout=10 -o StrictHostKeyChecking=accept-new -o UserKnownHostsFile=${KNOWN_HOSTS}.jump -W %h:%p ${JUMP_USER}@${JUMP_IP}")
fi
ssh_run() { timeout -s TERM -k 15 90s ssh "${ssh_opts[@]}" "$@"; }

ssh_run "root@$WORKER_IP" \
  "install -d -m 0755 '$RUNTIME_DIR' && cat > '$RUNTIME_FILE' && chmod 0755 '$RUNTIME_FILE'" \
  < "$STATIC_SOURCE"

UNIT_CONTENT="$(printf '%s\n' \
  '[Unit]' \
  'Description=CatsCo Artifact static host' \
  'After=network-online.target' \
  'Wants=network-online.target' \
  '' \
  '[Service]' \
  'Type=simple' \
  'User=catsco-agent' \
  'Group=catsco-agent' \
  "ExecStart=$NODE_PATH $RUNTIME_FILE serve --root $STATIC_ROOT --port $BACKEND_PORT" \
  'Restart=always' \
  'RestartSec=3' \
  "WorkingDirectory=$DATA_DIR" \
  '' \
  '[Install]' \
  'WantedBy=multi-user.target')"

ssh_run "root@$WORKER_IP" \
  "install -d -m 0755 -o catsco-agent -g catsco-agent '$DATA_DIR' '$STATIC_ROOT' '$DATA_DIR/artifact-management' '$DATA_DIR/artifact-trash' && cat > '/etc/systemd/system/$SERVICE_NAME' && chmod 0644 '/etc/systemd/system/$SERVICE_NAME' && systemctl daemon-reload && systemctl enable --now '$SERVICE_NAME' && systemctl restart '$SERVICE_NAME'" \
  <<<"$UNIT_CONTENT"

ssh_run "root@$WORKER_IP" \
  "'$NODE_PATH' '$RUNTIME_FILE' probe --url 'http://127.0.0.1:$BACKEND_PORT/__artifact_health' --port '$BACKEND_PORT' --timeout-ms 15000"

GATEWAY_IP="${CATSCO_ARTIFACT_GATEWAY_SSH_IP:-$JUMP_IP}"
GATEWAY_KEY="${CATSCO_ARTIFACT_GATEWAY_SSH_KEY:-$JUMP_KEY}"
GATEWAY_PORT="${CATSCO_ARTIFACT_GATEWAY_SSH_PORT:-$JUMP_PORT}"
GATEWAY_USER="${CATSCO_ARTIFACT_GATEWAY_SSH_USER:-$JUMP_USER}"
if [[ -n "$GATEWAY_IP" ]]; then
  jump_opts=(-i "$GATEWAY_KEY" -p "$GATEWAY_PORT" -o BatchMode=yes -o ConnectTimeout=10
    -o StrictHostKeyChecking=accept-new -o UserKnownHostsFile="${KNOWN_HOSTS}.jump")
  timeout -s TERM -k 15 30s ssh "${jump_opts[@]}" "$GATEWAY_USER@$GATEWAY_IP" \
    "curl --fail --silent --show-error --max-time 5 'http://$WORKER_IP:$BACKEND_PORT/__artifact_health' >/dev/null"
fi

"$ROUTE_CLIENT" register "$AGENT_UID" "$WORKER_IP" "$NETWORK_MODE" >/dev/null
printf '{"ok":true,"status":"ready","agent_uid":"%s","worker_ip":"%s"}\n' "$AGENT_UID" "$WORKER_IP"
