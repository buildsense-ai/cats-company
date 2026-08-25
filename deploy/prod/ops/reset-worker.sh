#!/usr/bin/env bash
# reset-worker.sh — 云托管虚拟员工重置/重装（B4-1d）
#
# 重置是“原实例内重装”：保留天翼云实例、包月订单、到期时间、网络和
# 实例名，只把系统盘重装为指定 worker 镜像，然后重新注入身份并启动
# catsco-agent。它绝不能调用 destroy/unsubscribe/create；销毁实例是
# destroy-worker.sh 的职责。
#
# 用法：
#   reset-worker.sh --name <tenant> [--version <v> | --image-id <id>]
#     [--credential-file <0600-file>] [--login-token <jwt>] [--api-key <key>]
#     [--bot-uid <uid>] [--user-uid <uid>] [--user-name <n>]
#     [--user-display <d>] [--body-id <id>] [--installation-id <id>] [--dry-run]
#
# --version 指定 bake 镜像版本（从 list-worker-images.sh 解析对应 image id），
# 缺省使用最新镜像。
#
# 依赖：ctyun-cli + jq + ssh + ssh-keygen + timeout
# 云环境：CTYUN_WORKER_REGION_ID / _PROJECT_ID；SSH 跳板及应用地址沿用
# provision-worker.sh 的 CTYUN_JUMP_* / CATSCO_WORKER_* 环境变量。
set -Eeuo pipefail

NAME=""
VERSION=""
IMAGE_ID=""
LOGIN_TOKEN=""
BOT_API_KEY=""
BOT_UID=""
USER_UID=""
USER_NAME=""
USER_DISPLAY=""
BODY_ID=""
INSTALLATION_ID=""
CREDENTIAL_FILE=""
DRY_RUN=0

usage() {
  sed -n '2,20p' "$0" | sed 's/^# \{0,1\}//'
}

while (($#)); do
  case "$1" in
    --name) NAME="${2:-}"; shift 2 ;;
    --credential-file) CREDENTIAL_FILE="${2:-}"; shift 2 ;;
    --version) VERSION="${2:-}"; shift 2 ;;
    --image-id) IMAGE_ID="${2:-}"; shift 2 ;;
    --login-token) LOGIN_TOKEN="${2:-}"; shift 2 ;;
    --api-key) BOT_API_KEY="${2:-}"; shift 2 ;;
    --bot-uid) BOT_UID="${2:-}"; shift 2 ;;
    --user-uid) USER_UID="${2:-}"; shift 2 ;;
    --user-name) USER_NAME="${2:-}"; shift 2 ;;
    --user-display) USER_DISPLAY="${2:-}"; shift 2 ;;
    --body-id) BODY_ID="${2:-}"; shift 2 ;;
    --installation-id) INSTALLATION_ID="${2:-}"; shift 2 ;;
    --dry-run) DRY_RUN=1; shift ;;
    -h|--help) usage; exit 0 ;;
    *) echo "error: unknown argument: $1" >&2; usage >&2; exit 2 ;;
  esac
done

if [[ -n "$CREDENTIAL_FILE" ]]; then
  [[ -f "$CREDENTIAL_FILE" && ! -L "$CREDENTIAL_FILE" ]] || { echo "error: credential file is missing" >&2; exit 2; }
  [[ "$(stat -c '%a' "$CREDENTIAL_FILE" 2>/dev/null || echo 000)" == "600" ]] || { echo "error: credential file must be mode 600" >&2; exit 2; }
  read -r LOGIN_TOKEN < "$CREDENTIAL_FILE" || true
  BOT_API_KEY="$(sed -n '2p' "$CREDENTIAL_FILE")"
fi

if [[ -z "$NAME" ]]; then
  echo "error: --name is required" >&2
  usage >&2
  exit 2
fi
if [[ ! "$NAME" =~ ^[a-z0-9][a-z0-9_-]{1,63}$ ]]; then
  echo "error: --name must match ^[a-z0-9][a-z0-9_-]{1,63}\$" >&2
  exit 2
fi

REGION_ID="${CTYUN_WORKER_REGION_ID:-}"
PROJECT_ID="${CTYUN_WORKER_PROJECT_ID:-0}"
if [[ -n "${CTYUN_WORKER_STATE_ROOT:-}" ]]; then
  # Current layout: one shared root with an isolated directory per tenant.
  STATE_ROOT="${CTYUN_WORKER_STATE_ROOT%/}"
  STATE_DIR="${STATE_ROOT}/${NAME}"
elif [[ -n "${CTYUN_WORKER_STATE_DIR:-}" ]]; then
  # Explicit STATE_DIR is a legacy single-tenant override retained for old
  # operator jobs and tests. Do not reinterpret it as a shared root.
  STATE_ROOT="${CTYUN_WORKER_STATE_DIR%/}"
  STATE_DIR="$STATE_ROOT"
else
  STATE_ROOT="/var/lib/catsco-worker"
  STATE_DIR="${STATE_ROOT}/${NAME}"
fi
JUMP_IP="${CTYUN_JUMP_IP:-}"
JUMP_PORT="${CTYUN_JUMP_PORT:-22}"
JUMP_USER="${CTYUN_JUMP_USER:-root}"
JUMP_KEY="${CTYUN_JUMP_KEY:-$STATE_ROOT/jump_host_ed25519}"
HTTP_BASE_URL="${CATSCO_WORKER_HTTP_BASE_URL:-https://app.catsco.cc}"
SERVER_URL="${CATSCO_WORKER_SERVER_URL:-wss://app.catsco.cc/v0/channels}"

# Older production deployments stored a single tenant's state directly under
# CTYUN_WORKER_STATE_ROOT rather than in a tenant subdirectory. Reuse that
# snapshot only when its bot UID matches the reset request; never let one
# tenant borrow another tenant's key.
if [[ -n "${CTYUN_WORKER_STATE_ROOT:-}" && ! -f "$STATE_DIR/inject.env" && -f "$STATE_ROOT/inject.env" ]]; then
  legacy_bot_uid="$(sed -n 's/^CATSCO_BOT_UID=//p' "$STATE_ROOT/inject.env" | tail -n1 || true)"
  if [[ -n "$BOT_UID" && "$legacy_bot_uid" == "$BOT_UID" ]]; then
    STATE_DIR="$STATE_ROOT"
  fi
fi

if [[ -f "$STATE_DIR/inject.env" ]]; then
  [[ -n "$LOGIN_TOKEN" ]] || LOGIN_TOKEN="$(sed -n 's/^CATSCO_USER_TOKEN=//p' "$STATE_DIR/inject.env" | tail -n1)"
  [[ -n "$BOT_API_KEY" ]] || BOT_API_KEY="$(sed -n 's/^CATSCO_API_KEY=//p' "$STATE_DIR/inject.env" | tail -n1)"
  [[ -n "$BOT_UID" ]] || BOT_UID="$(sed -n 's/^CATSCO_BOT_UID=//p' "$STATE_DIR/inject.env" | tail -n1)"
  [[ -n "$USER_UID" ]] || USER_UID="$(sed -n 's/^CATSCO_USER_UID=//p' "$STATE_DIR/inject.env" | tail -n1)"
  [[ -n "$USER_NAME" ]] || USER_NAME="$(sed -n 's/^CATSCO_USER_NAME=//p' "$STATE_DIR/inject.env" | tail -n1)"
  [[ -n "$USER_DISPLAY" ]] || USER_DISPLAY="$(sed -n 's/^CATSCO_USER_DISPLAY_NAME=//p' "$STATE_DIR/inject.env" | tail -n1)"
  [[ -n "$BODY_ID" ]] || BODY_ID="$(sed -n 's/^CATSCO_BODY_ID=//p' "$STATE_DIR/inject.env" | tail -n1)"
  [[ -n "$INSTALLATION_ID" ]] || INSTALLATION_ID="$(sed -n 's/^CATSCO_INSTALLATION_ID=//p' "$STATE_DIR/inject.env" | tail -n1)"
fi
if [[ -z "$BOT_API_KEY" || -z "$LOGIN_TOKEN" ]]; then
  echo "error: --api-key and --login-token are required (no inject.env snapshot found)" >&2
  exit 2
fi
[[ -n "$REGION_ID" ]] || { echo "error: CTYUN_WORKER_REGION_ID is required" >&2; exit 2; }
for cmd in ctyun-cli jq ssh ssh-keygen timeout; do
  command -v "$cmd" >/dev/null 2>&1 || { echo "error: missing required command: $cmd" >&2; exit 2; }
done

OPS_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
if [[ -n "$VERSION" ]]; then
  [[ "$VERSION" =~ ^[A-Za-z0-9._-]+$ ]] || { echo "error: --version must match ^[A-Za-z0-9._-]+\$" >&2; exit 2; }
  if [[ -z "$IMAGE_ID" ]]; then
    LIST_IMAGES_CMD="$(command -v list-worker-images.sh || true)"
    [[ -n "$LIST_IMAGES_CMD" ]] || LIST_IMAGES_CMD="$OPS_DIR/list-worker-images.sh"
    IMAGE_ID="$("$LIST_IMAGES_CMD" | awk -F'\t' -v v="$VERSION" '$3 == v { print $1; exit }')"
    [[ -n "$IMAGE_ID" ]] || { echo "error: no image found for version: $VERSION" >&2; exit 2; }
  fi
elif [[ -z "$IMAGE_ID" ]]; then
  LIST_IMAGES_CMD="$(command -v list-worker-images.sh || true)"
  [[ -n "$LIST_IMAGES_CMD" ]] || LIST_IMAGES_CMD="$OPS_DIR/list-worker-images.sh"
  IMAGE_ID="$("$LIST_IMAGES_CMD" | sort -t $'\t' -k5,5nr | head -n1 | cut -f1)"
  [[ -n "$IMAGE_ID" ]] || { echo "error: no worker image resolved" >&2; exit 1; }
fi

ctyun() {
  local raw status
  raw="$(timeout -s TERM -k 15 120s ctyun-cli "$@" --output json 2>&1)" || {
    echo "error: ctyun-cli failed: $*" >&2
    echo "$raw" >&2
    return 1
  }
  status="$(jq -r '.statusCode // empty' <<<"$raw")"
  if [[ "$status" != "800" ]]; then
    echo "error: Tianyi Cloud API failed: $(jq -r '.errorCode // ""' <<<"$raw") $(jq -r '.message // ""' <<<"$raw")" >&2
    return 1
  fi
  printf '%s' "$raw"
}

INSTANCE_NAME="worker-${NAME}"
find_instance() {
  local resp name="$1"
  resp="$(ctyun ecs ListEcsInstances --regionID "$REGION_ID" --projectID "$PROJECT_ID" \
    --instanceName "$name" --pageNo 1 --pageSize 10)"
  jq -r --arg n "$name" '.returnObj.results[]? | select(.instanceName == $n)' <<<"$resp" || true
}

instance="$(find_instance "$INSTANCE_NAME")"
[[ -n "$instance" ]] || { echo "error: instance $INSTANCE_NAME not found; reset never creates a new instance" >&2; exit 1; }
instance_id="$(jq -r '.instanceID // ""' <<<"$instance")"
state="$(jq -r '.instanceStatus // .state // .status // ""' <<<"$instance" | tr '[:upper:]' '[:lower:]')"
[[ -n "$instance_id" ]] || { echo "error: instance $INSTANCE_NAME has no instanceID" >&2; exit 1; }
case "$state" in
  unsubscribed|released|deleted)
    echo "error: instance $INSTANCE_NAME is $state; reset will not recover or rebill a subscription" >&2
    exit 1
    ;;
esac

if [[ $DRY_RUN -eq 1 ]]; then
  printf '{"status":"dry-run","instance_name":"%s","instance_id":"%s","image_id":"%s","state":"%s"}\n' \
    "$INSTANCE_NAME" "$instance_id" "$IMAGE_ID" "$state"
  exit 0
fi

PRIVATE_KEY="$STATE_DIR/id_rsa"
[[ -f "$PRIVATE_KEY" ]] || { echo "error: tenant private key is missing: $PRIVATE_KEY; refuse reset without SSH recovery" >&2; exit 1; }
chmod 600 "$PRIVATE_KEY"

# RebuildEcsInstance preserves the instance/order/network and only replaces the
# system disk. Stop first so provider state transitions are deterministic.
if [[ "$state" != "stopped" && "$state" != "shutoff" && "$state" != "error" ]]; then
  ctyun ecs StopEcsInstance --regionID "$REGION_ID" --instanceID "$instance_id" --force false >/dev/null
  for _ in $(seq 1 30); do
    sleep 2
    instance="$(find_instance "$INSTANCE_NAME")"
    state="$(jq -r '.instanceStatus // .state // .status // ""' <<<"$instance" | tr '[:upper:]' '[:lower:]')"
    [[ "$state" == "stopped" || "$state" == "shutoff" || "$state" == "error" ]] && break
  done
  [[ "$state" == "stopped" || "$state" == "shutoff" || "$state" == "error" ]] || {
    echo "error: timed out waiting for instance stop (instance_id=$instance_id)" >&2; exit 1;
  }
fi

keypair_name="$(jq -r '.keypairName // ""' <<<"$instance")"
if [[ -z "$keypair_name" ]]; then
  details="$(ctyun ecs GetEcsInstanceDetails --regionID "$REGION_ID" --instanceID "$instance_id")"
  keypair_name="$(jq -r '.returnObj.keypairName // ""' <<<"$details")"
fi
[[ -n "$keypair_name" ]] || { echo "error: instance $INSTANCE_NAME has no keypairName" >&2; exit 1; }
keypair_id="$(ctyun ecs GetEcsKeypairDetails --regionID "$REGION_ID" --projectID "$PROJECT_ID" \
  --keyPairName "$keypair_name" --pageNo 1 --pageSize 10 \
  | jq -r --arg n "$keypair_name" '.returnObj.results[]? | select(.keyPairName == $n) | .keyPairID' | head -n1)"
[[ -n "$keypair_id" ]] || { echo "error: keypair $keypair_name cannot be resolved" >&2; exit 1; }

rebuild="$(ctyun ecs RebuildEcsInstance --regionID "$REGION_ID" --instanceID "$instance_id" \
  --imageID "$IMAGE_ID" --keyPairID "$keypair_id" --monitorService false --userName root)"
job_id="$(jq -r '.returnObj.jobID // empty' <<<"$rebuild")"
echo "[cloud-worker] rebuild submitted instance_id=$instance_id job_id=${job_id:-none}" >&2

INSTANCE_IP=""
for _ in $(seq 1 90); do
  instance="$(find_instance "$INSTANCE_NAME")"
  [[ -n "$instance" ]] || { sleep 10; continue; }
  state="$(jq -r '.instanceStatus // .state // .status // ""' <<<"$instance" | tr '[:upper:]' '[:lower:]')"
  if [[ "$state" == "error" || "$state" == "unsubscribed" ]]; then
    echo "error: rebuild entered terminal state=$state (instance_id=$instance_id)" >&2
    exit 1
  fi
  if [[ "$state" == "running" || "$state" == "active" ]]; then
    INSTANCE_IP="$(jq -r '(.fixedIPList[0] // .privateIP // .floatingIP // .publicIP // "")' <<<"$instance")"
    [[ -n "$INSTANCE_IP" ]] && break
  fi
  sleep 10
done
[[ -n "$INSTANCE_IP" ]] || { echo "error: timed out waiting for rebuilt instance to be running" >&2; exit 1; }

ssh_opts=(-i "$PRIVATE_KEY" -o BatchMode=yes -o ConnectTimeout=10 \
  -o ServerAliveInterval=15 -o ServerAliveCountMax=3 \
  -o StrictHostKeyChecking=accept-new -o UserKnownHostsFile="$STATE_DIR/known_hosts")
if [[ -n "$JUMP_IP" ]]; then
  ssh_opts+=(-o "ProxyCommand=ssh -i ${JUMP_KEY} -p ${JUMP_PORT} -o BatchMode=yes -o ConnectTimeout=10 -o StrictHostKeyChecking=accept-new -o UserKnownHostsFile=${STATE_DIR}/jump_known_hosts -W %h:%p ${JUMP_USER}@${JUMP_IP}")
fi
ssh_run() { timeout -s TERM -k 15 60s ssh "${ssh_opts[@]}" "$@"; }
ssh_ready=""
for _ in $(seq 1 36); do
  if ssh_run "root@$INSTANCE_IP" "cloud-init status 2>/dev/null | grep -q '^status: done'" 2>/dev/null; then
    ssh_ready=1; break
  fi
  sleep 10
done
[[ -n "$ssh_ready" ]] || { echo "error: timed out waiting for SSH/cloud-init on $INSTANCE_IP" >&2; exit 1; }

BODY_ID="${BODY_ID:-$(sed -n 's/^CATSCO_BODY_ID=//p' "$STATE_DIR/inject.env" 2>/dev/null | tail -n1 || true)}"
INSTALLATION_ID="${INSTALLATION_ID:-$(sed -n 's/^CATSCO_INSTALLATION_ID=//p' "$STATE_DIR/inject.env" 2>/dev/null | tail -n1 || true)}"
ENV_CONTENT="$(printf '%s\n' \
  "CATSCO_HTTP_BASE_URL=${HTTP_BASE_URL}" \
  "CATSCO_SERVER_URL=${SERVER_URL}" \
  "CATSCO_API_KEY=${BOT_API_KEY}" \
  "CATSCO_BOT_UID=${BOT_UID}" \
  "CATSCO_BODY_ID=${BODY_ID}" \
  "CATSCO_INSTALLATION_ID=${INSTALLATION_ID}" \
  "CATSCO_USER_TOKEN=${LOGIN_TOKEN}" \
  "CATSCO_USER_UID=${USER_UID}" \
  "CATSCO_USER_NAME=${USER_NAME}" \
  "CATSCO_USER_DISPLAY_NAME=${USER_DISPLAY}" \
  "CATSCO_LOG_UPLOAD_ENABLED=true")"
ssh_run "root@$INSTANCE_IP" "install -d -o catsco-agent -g catsco-agent /srv/catsco-agent && cat > /srv/catsco-agent/.env && chown catsco-agent:catsco-agent /srv/catsco-agent/.env && chmod 600 /srv/catsco-agent/.env" <<<"$ENV_CONTENT"
mkdir -p "$STATE_DIR"
printf '%s\n' "$ENV_CONTENT" > "$STATE_DIR/inject.env"
chmod 600 "$STATE_DIR/inject.env"

LOCAL_CONFIG_JSON="$(jq -n \
  --arg http "$HTTP_BASE_URL" --arg server "$SERVER_URL" --arg token "$LOGIN_TOKEN" \
  --arg uid "$USER_UID" --arg uname "$USER_NAME" --arg display "$USER_DISPLAY" \
  --arg botUid "$BOT_UID" --arg apiKey "$BOT_API_KEY" --arg bodyId "$BODY_ID" \
  --arg installId "$INSTALLATION_ID" --arg tenant "$NAME" \
  --arg now "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
  '{version:1, endpoints:{httpBaseUrl:$http, serverUrl:$server}, account:{token:$token, uid:$uid, username:$uname, displayName:$display}, currentBot:{uid:$botUid, name:"Bot", username:"", apiKey:$apiKey, boundAt:$now, boundByUserUid:$uid, bindingSource:"cloud-reset"}, device:{deviceId:$bodyId, bodyId:$bodyId, installationId:$installId, name:$tenant}, updatedAt:$now}')"
ssh_run "root@$INSTANCE_IP" "install -d -o catsco-agent -g catsco-agent /srv/catsco-agent/.xiaoba && cat > /srv/catsco-agent/.xiaoba/catsco.json && chown catsco-agent:catsco-agent /srv/catsco-agent/.xiaoba/catsco.json && chmod 600 /srv/catsco-agent/.xiaoba/catsco.json" <<<"$LOCAL_CONFIG_JSON"
ssh_run "root@$INSTANCE_IP" "systemctl enable --now catsco-agent.service && sleep 3 && systemctl is-active catsco-agent.service" >/dev/null 2>&1

APP_VERSION="$(ssh_run "root@$INSTANCE_IP" "cat /opt/catsco/current/worker-release.json 2>/dev/null" 2>/dev/null | jq -r '.version // empty' 2>/dev/null || true)"
if [[ "$APP_VERSION" =~ ^[0-9A-Za-z][0-9A-Za-z._+-]{0,63}$ ]]; then
  printf '%s\n' "$APP_VERSION" > "$STATE_DIR/app_version.tmp"
  mv -f "$STATE_DIR/app_version.tmp" "$STATE_DIR/app_version"
fi

printf '{"status":"reinitialized","instance_id":"%s","instance_name":"%s","ip":"%s","image_id":"%s"}\n' \
  "$instance_id" "$INSTANCE_NAME" "$INSTANCE_IP" "$IMAGE_ID"
