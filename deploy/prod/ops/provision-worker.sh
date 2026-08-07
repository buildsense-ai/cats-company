#!/usr/bin/env bash
# provision-worker.sh — 云托管虚拟员工供给（B4-1b）
#
# 为一个 tenant 新建云实例（天翼云 worker 私有镜像），SSH 注入创建者登录凭证
# + bot 连接凭证到 /srv/catsco-agent/.env，并启用 catsco-agent.service。
#
# 用法：
#   provision-worker.sh --name <tenant> --login-token <jwt> \
#     --api-key <bot-key> [--bot-uid <uid>] [--user-uid <uid>] \
#     [--user-name <n>] [--user-display <d>] [--image-id <id>] [--dry-run]
#
# 幂等：实例名 worker-<tenant> 已存在（running/active）则跳过并报告。
# fail-closed：任一步失败 → 清理已创建的实例与 key pair → 退出非 0。
#
# 依赖：ctyun-cli + jq + ssh/ssh-keygen/scp（server 镜像已装）
# 凭据：CTYUN_AK/CTYUN_SK（ctyun-cli 环境变量或 ~/.ctyun-cli.yaml）
# 云环境：CTYUN_WORKER_REGION_ID / _PROJECT_ID / _AZ_NAME / _FLAVOR_ID /
#         _VPC_ID / _SUBNET_ID / _SECURITY_GROUP_ID
set -Eeuo pipefail

NAME=""
LOGIN_TOKEN=""
BOT_API_KEY=""
BOT_UID=""
USER_UID=""
USER_NAME=""
USER_DISPLAY=""
IMAGE_ID=""
DRY_RUN=0

usage() {
  sed -n '2,14p' "$0" | sed 's/^# \{0,1\}//'
}

while (($#)); do
  case "$1" in
    --name) NAME="${2:-}"; shift 2 ;;
    --login-token) LOGIN_TOKEN="${2:-}"; shift 2 ;;
    --api-key) BOT_API_KEY="${2:-}"; shift 2 ;;
    --bot-uid) BOT_UID="${2:-}"; shift 2 ;;
    --user-uid) USER_UID="${2:-}"; shift 2 ;;
    --user-name) USER_NAME="${2:-}"; shift 2 ;;
    --user-display) USER_DISPLAY="${2:-}"; shift 2 ;;
    --image-id) IMAGE_ID="${2:-}"; shift 2 ;;
    --dry-run) DRY_RUN=1; shift ;;
    -h|--help) usage; exit 0 ;;
    *) echo "error: unknown argument: $1" >&2; usage >&2; exit 2 ;;
  esac
done

REGION_ID="${CTYUN_WORKER_REGION_ID:-}"
PROJECT_ID="${CTYUN_WORKER_PROJECT_ID:-0}"
AZ_NAME="${CTYUN_WORKER_AZ_NAME:-}"
FLAVOR_ID="${CTYUN_WORKER_FLAVOR_ID:-}"
VPC_ID="${CTYUN_WORKER_VPC_ID:-}"
SUBNET_ID="${CTYUN_WORKER_SUBNET_ID:-}"
SECURITY_GROUP_ID="${CTYUN_WORKER_SECURITY_GROUP_ID:-}"
HTTP_BASE_URL="${CATSCO_WORKER_HTTP_BASE_URL:-https://app.catsco.cc}"
SERVER_URL="${CATSCO_WORKER_SERVER_URL:-wss://app.catsco.cc/v0/channels}"

# --- 校验 ---
if [[ -z "$NAME" || -z "$LOGIN_TOKEN" || -z "$BOT_API_KEY" ]]; then
  echo "error: --name, --login-token and --api-key are required" >&2
  usage >&2
  exit 2
fi
# tenant 名约束（出现在实例名/标签/key pair 名/SSH argv）
if [[ ! "$NAME" =~ ^[a-z0-9][a-z0-9_-]{1,63}$ ]]; then
  echo "error: --name must match ^[a-z0-9][a-z0-9_-]{1,63}\$" >&2
  exit 2
fi
for required in ctyun-cli jq ssh ssh-keygen; do
  command -v "$required" >/dev/null 2>&1 || { echo "error: missing required command: $required" >&2; exit 2; }
done
for v in "$REGION_ID" "$AZ_NAME" "$FLAVOR_ID" "$VPC_ID" "$SUBNET_ID" "$SECURITY_GROUP_ID"; do
  [[ -n "$v" ]] || { echo "error: CTYUN_WORKER_* environment is incomplete" >&2; exit 2; }
done

INSTANCE_NAME="worker-${NAME}"
# 供私钥/known_hosts 持久化（生产应为挂载卷；测试/本地可覆盖）
STATE_DIR="${CTYUN_WORKER_STATE_DIR:-/var/lib/catsco-worker/${NAME}}"

# --- 工具 ---
ctyun() {
  local raw status
  raw="$(timeout --signal=TERM --kill-after=15s 120s ctyun-cli "$@" --output json 2>&1)" || {
    echo "error: ctyun-cli failed: $*" >&2; echo "$raw" >&2; exit 1
  }
  status="$(jq -r '.statusCode // empty' <<<"$raw")"
  if [[ "$status" != "800" ]]; then
    echo "error: Tianyi Cloud API failed: $(jq -r '.errorCode // ""' <<<"$raw") $(jq -r '.message // ""' <<<"$raw")" >&2
    exit 1
  fi
  printf '%s' "$raw"
}

find_instance() {
  # 返回实例 JSON（首个匹配名），无则空
  local resp name
  name="$1"
  resp="$(ctyun ecs ListEcsInstances --regionID "$REGION_ID" --projectID "$PROJECT_ID" \
    --instanceName "$name" --pageNo 1 --pageSize 10)"
  jq -r --arg n "$name" '.returnObj.results[]? | select(.instanceName == $n)' <<<"$resp" || true
}

cleanup_failed() {
  # fail-closed：删除刚创建的实例 + key pair（尽力，聚合报错）
  local errors=""
  if [[ -n "${CREATED_INSTANCE_ID:-}" ]]; then
    if ctyun ecs DeleteEcsInstance --regionID "$REGION_ID" --projectID "$PROJECT_ID" \
        --instanceID "$CREATED_INSTANCE_ID" >/dev/null 2>&1; then
      : # ok
    else
      errors="instance delete failed; "
    fi
  fi
  if [[ -n "${KEYPAIR_NAME:-}" ]]; then
    if ctyun ecs DeleteEcsKeypair --regionID "$REGION_ID" --projectID "$PROJECT_ID" \
        --keyPairName "$KEYPAIR_NAME" >/dev/null 2>&1; then
      : # ok
    else
      errors="${errors}key pair delete failed; "
    fi
  fi
  echo "error: provision failed; cleanup done (${errors:-ok})" >&2
}

trap 'if [[ $? -ne 0 && $DRY_RUN -eq 0 ]]; then cleanup_failed; fi' EXIT

# --- 1. 幂等检查 ---
existing="$(find_instance "$INSTANCE_NAME")"
if [[ -n "$existing" ]]; then
  echo "{\"status\":\"exists\",\"instance_id\":\"$(jq -r '.instanceID // ""' <<<"$existing")\",\"instance_name\":\"$INSTANCE_NAME\"}"
  exit 0
fi

# --- 2. resolve 镜像（指定或最新） ---
if [[ -z "$IMAGE_ID" ]]; then
  IMAGE_ID="$(timeout --signal=TERM --kill-after=15s 90s /opt/catsco/ops/list-worker-images.sh 2>/dev/null | head -n1 | cut -f1)"
fi
[[ -n "$IMAGE_ID" ]] || { echo "error: no worker image resolved (set --image-id or run list-worker-images)" >&2; exit 1; }

if [[ $DRY_RUN -eq 1 ]]; then
  echo "{\"status\":\"dry-run\",\"instance_name\":\"$INSTANCE_NAME\",\"image_id\":\"$IMAGE_ID\"}"
  exit 0
fi

# --- 3. key pair（固定名 worker-key-<tenant>，已存在则复用） ---
KEYPAIR_NAME="worker-key-${NAME}"
mkdir -p "$STATE_DIR"
PRIVATE_KEY="$STATE_DIR/id_rsa"

keypair_id="$(ctyun ecs GetEcsKeypairDetails --regionID "$REGION_ID" --projectID "$PROJECT_ID" \
  --keyPairName "$KEYPAIR_NAME" --pageNo 1 --pageSize 10 \
  | jq -r --arg n "$KEYPAIR_NAME" '.returnObj.results[]? | select(.keyPairName == $n) | .keyPairID' | head -n1)"
if [[ -z "$keypair_id" ]]; then
  ssh-keygen -q -t rsa -b 3072 -N "" -C "catsco-worker-${NAME}" -f "$PRIVATE_KEY"
  chmod 600 "$PRIVATE_KEY"
  PUBLIC_KEY="$(cat "${PRIVATE_KEY}.pub")"
  ctyun ecs ImportEcsKeypair --regionID "$REGION_ID" --projectID "$PROJECT_ID" \
    --keyPairName "$KEYPAIR_NAME" --keyPairDescription "CatsCo cloud worker $NAME" \
    --publicKey "$PUBLIC_KEY" >/dev/null
  keypair_id="$(ctyun ecs GetEcsKeypairDetails --regionID "$REGION_ID" --projectID "$PROJECT_ID" \
    --keyPairName "$KEYPAIR_NAME" --pageNo 1 --pageSize 10 \
    | jq -r --arg n "$KEYPAIR_NAME" '.returnObj.results[]? | select(.keyPairName == $n) | .keyPairID' | head -n1)"
  [[ -n "$keypair_id" ]] || { echo "error: key pair could not be resolved after import" >&2; exit 1; }
fi

# --- 4. 创建实例（私有 worker 镜像 → imageType 0） ---
# 可移植 UUID：优先内核文件，其次 uuidgen，最后时间/pid/随机兜底
# （BODY_ID/INSTALLATION_ID 只需全局唯一；clientToken 用于幂等重试）
gen_uuid() {
  if [[ -r /proc/sys/kernel/random/uuid ]]; then
    cat /proc/sys/kernel/random/uuid
  elif command -v uuidgen >/dev/null 2>&1; then
    uuidgen
  else
    printf 'catsco-%s-%s-%s\n' "$$" "$(date +%s)" "${RANDOM}${RANDOM}"
  fi
}

BODY_ID="$(gen_uuid)"
INSTALLATION_ID="$(gen_uuid)"

create_resp="$(ctyun ecs CreateEcsInstance \
  --regionID "$REGION_ID" \
  --projectID "$PROJECT_ID" \
  --clientToken "$(gen_uuid)" \
  --azName "$AZ_NAME" \
  --displayName "$INSTANCE_NAME" \
  --instanceName "$INSTANCE_NAME" \
  --instanceDescription "CatsCo cloud worker $NAME" \
  --flavorID "$FLAVOR_ID" \
  --imageID "$IMAGE_ID" \
  --imageType 0 \
  --bootDiskType "SSD" \
  --bootDiskSize 100 \
  --vpcID "$VPC_ID" \
  --networkCardList "[{\"isMaster\":true,\"subnetID\":\"$SUBNET_ID\"}]" \
  --secGroupList "[\"$SECURITY_GROUP_ID\"]" \
  --keyPairID "$keypair_id" \
  --onDemand true \
  --extIP 1 \
  --bandwidth 10 \
  --ipVersion ipv4 \
  --lineType standalone \
  --demandBillingType upflowc \
  --monitorService false \
  --securityProduct false \
  --trustInstance false \
  --labelList "[{\"labelKey\":\"purpose\",\"labelValue\":\"catsco-worker\"},{\"labelKey\":\"tenant\",\"labelValue\":\"$NAME\"}]")"
CREATED_INSTANCE_ID="$(jq -r '.returnObj.masterResourceID // empty' <<<"$create_resp")"
[[ -n "$CREATED_INSTANCE_ID" ]] || { echo "error: CreateEcsInstance did not return masterResourceID" >&2; exit 1; }

# --- 5. 等实例 running + 等 SSH（cloud-init done） ---
INSTANCE_IP=""
for _ in $(seq 1 60); do
  inst="$(find_instance "$INSTANCE_NAME")"
  if [[ -n "$inst" ]]; then
    state="$(jq -r '.state // .status // ""' <<<"$inst")"
    ip="$(jq -r '.floatingIP // .publicIP // ""' <<<"$inst")"
    if [[ "$state" == "running" || "$state" == "active" ]]; then
      [[ -n "$ip" ]] && { INSTANCE_IP="$ip"; break; }
    fi
  fi
  sleep 10
done
[[ -n "$INSTANCE_IP" ]] || { echo "error: timed out waiting for instance to be running" >&2; exit 1; }

ssh_opts=(-i "$PRIVATE_KEY" -o BatchMode=yes -o ConnectTimeout=10 \
  -o ServerAliveInterval=15 -o ServerAliveCountMax=3 \
  -o StrictHostKeyChecking=accept-new -o UserKnownHostsFile="$STATE_DIR/known_hosts")
# ssh 统一走 timeout 限时（防挂死；与 bake 脚本一致）
ssh_run() {
  timeout --signal=TERM --kill-after=15s 60s ssh "${ssh_opts[@]}" "$@"
}
ssh_ready=""
for _ in $(seq 1 36); do
  if ssh_run "root@$INSTANCE_IP" "cloud-init status 2>/dev/null | grep -q '^status: done'" 2>/dev/null; then
    ssh_ready=1; break
  fi
  sleep 10
done
[[ -n "$ssh_ready" ]] || { echo "error: timed out waiting for SSH/cloud-init on $INSTANCE_IP" >&2; exit 1; }

# --- 6. 注入 .env（创建者登录凭证 + bot 连接凭证） ---
ENV_CONTENT="$(cat <<EOF
CATSCO_HTTP_BASE_URL=${HTTP_BASE_URL}
CATSCO_SERVER_URL=${SERVER_URL}
CATSCO_API_KEY=${BOT_API_KEY}
CATSCO_BOT_UID=${BOT_UID}
CATSCO_BODY_ID=${BODY_ID}
CATSCO_INSTALLATION_ID=${INSTALLATION_ID}
CATSCO_USER_TOKEN=${LOGIN_TOKEN}
CATSCO_USER_UID=${USER_UID}
CATSCO_USER_NAME=${USER_NAME}
CATSCO_USER_DISPLAY_NAME=${USER_DISPLAY}
CATSCO_LOG_UPLOAD_ENABLED=true
EOF
)"
ssh_run "root@$INSTANCE_IP" "install -d -o catsco-agent -g catsco-agent /srv/catsco-agent && cat > /srv/catsco-agent/.env && chown catsco-agent:catsco-agent /srv/catsco-agent/.env && chmod 600 /srv/catsco-agent/.env" <<<"$ENV_CONTENT"

# 保存注入快照（供 reset-worker.sh 无参数重建时复用身份），chmod 600
printf '%s\n' "$ENV_CONTENT" > "$STATE_DIR/inject.env"
chmod 600 "$STATE_DIR/inject.env"

# --- 7. 启用 service ---
ssh_run "root@$INSTANCE_IP" "systemctl enable --now catsco-agent.service && sleep 3 && systemctl is-active catsco-agent.service"

echo "{\"status\":\"provisioned\",\"instance_id\":\"$CREATED_INSTANCE_ID\",\"instance_name\":\"$INSTANCE_NAME\",\"ip\":\"$INSTANCE_IP\",\"image_id\":\"$IMAGE_ID\"}"
trap - EXIT
