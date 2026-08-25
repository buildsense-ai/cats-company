#!/usr/bin/env bash
# provision-worker.sh — 云托管虚拟员工供给（B4-1b）
#
# 为一个 tenant 新建云实例（天翼云 worker 私有镜像），SSH 注入创建者登录凭证
# + bot 连接凭证到 /srv/catsco-agent/.env，并启用 catsco-agent.service。
#
# 用法：
#   provision-worker.sh --name <tenant> [--credential-file <0600-file>] \
#     [--login-token <jwt>] [--api-key <bot-key>] [--bot-uid <uid>] [--user-uid <uid>] \
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
BODY_ID=""
INSTALLATION_ID=""
CREDENTIAL_FILE=""
DRY_RUN=0

usage() {
  sed -n '2,14p' "$0" | sed 's/^# \{0,1\}//'
}

while (($#)); do
  case "$1" in
    --name) NAME="${2:-}"; shift 2 ;;
    --credential-file) CREDENTIAL_FILE="${2:-}"; shift 2 ;;
    --login-token) LOGIN_TOKEN="${2:-}"; shift 2 ;;
    --api-key) BOT_API_KEY="${2:-}"; shift 2 ;;
    --bot-uid) BOT_UID="${2:-}"; shift 2 ;;
    --user-uid) USER_UID="${2:-}"; shift 2 ;;
    --user-name) USER_NAME="${2:-}"; shift 2 ;;
    --user-display) USER_DISPLAY="${2:-}"; shift 2 ;;
    --image-id) IMAGE_ID="${2:-}"; shift 2 ;;
    --body-id) BODY_ID="${2:-}"; shift 2 ;;
    --installation-id) INSTALLATION_ID="${2:-}"; shift 2 ;;
    --dry-run) DRY_RUN=1; shift ;;
    -h|--help) usage; exit 0 ;;
    *) echo "error: unknown argument: $1" >&2; usage >&2; exit 2 ;;
  esac
done

# Credentials supplied through a root-owned 0600 file never appear in the
# process argv (/proc). The two-line format is deliberately tiny: line 1 is
# the worker owner token and line 2 is the bot API key. Legacy argv flags remain
# supported for operator scripts and backwards compatibility.
if [[ -n "$CREDENTIAL_FILE" ]]; then
  [[ -f "$CREDENTIAL_FILE" && ! -L "$CREDENTIAL_FILE" ]] || { echo "error: credential file is missing" >&2; exit 2; }
  [[ "$(stat -c '%a' "$CREDENTIAL_FILE" 2>/dev/null || echo 000)" == "600" ]] || { echo "error: credential file must be mode 600" >&2; exit 2; }
  read -r LOGIN_TOKEN < "$CREDENTIAL_FILE" || true
  BOT_API_KEY="$(sed -n '2p' "$CREDENTIAL_FILE")"
fi

REGION_ID="${CTYUN_WORKER_REGION_ID:-}"
PROJECT_ID="${CTYUN_WORKER_PROJECT_ID:-0}"
AZ_NAME="${CTYUN_WORKER_AZ_NAME:-}"
FLAVOR_ID="${CTYUN_WORKER_FLAVOR_ID:-}"
VPC_ID="${CTYUN_WORKER_VPC_ID:-}"
SUBNET_ID="${CTYUN_WORKER_SUBNET_ID:-}"
SECURITY_GROUP_ID="${CTYUN_WORKER_SECURITY_GROUP_ID:-}"
# 默认使用内网 IP（NAT/跳板架构），不消耗公网 IP 配额。只有显式设为
# 1 时才申请公网 IP 与带宽。
EXT_IP="${CTYUN_WORKER_EXT_IP:-0}"
if [[ "$EXT_IP" != "0" && "$EXT_IP" != "1" ]]; then
  echo "error: CTYUN_WORKER_EXT_IP must be 0 or 1" >&2
  exit 2
fi
# 计费模式（平台按月售卖，默认 month）：month = 包月 + 到期时间，ondemand = 按量
# CTYUN_WORKER_CYCLE_COUNT：包月时长（月），默认 1
# CTYUN_WORKER_AUTO_RENEW 已废弃并忽略。云托管实例严禁自动续费，
# 到期后的冻结/释放由天翼云策略和 CatsCompany 生命周期任务处理。
BILLING_MODE="${CTYUN_WORKER_BILLING_MODE:-month}"
CYCLE_COUNT="${CTYUN_WORKER_CYCLE_COUNT:-1}"
if [[ "$BILLING_MODE" != "month" && "$BILLING_MODE" != "ondemand" ]]; then
  echo "error: CTYUN_WORKER_BILLING_MODE must be month or ondemand" >&2
  exit 2
fi
if [[ ! "$CYCLE_COUNT" =~ ^[0-9]+$ || "$CYCLE_COUNT" -lt 1 || "$CYCLE_COUNT" -gt 60 ]]; then
  echo "error: CTYUN_WORKER_CYCLE_COUNT must be 1-60" >&2
  exit 2
fi
# SSH 跳板（NAT 架构）：凭据一律来自服务器环境变量，仓库不硬编码任何 IP/密钥。
# CTYUN_JUMP_IP：跳板机公网入口 IP；空 = 直连公网 IP（旧模式）
# CTYUN_JUMP_PORT / CTYUN_JUMP_USER / CTYUN_JUMP_KEY：跳板机连接参数
JUMP_IP="${CTYUN_JUMP_IP:-}"
JUMP_PORT="${CTYUN_JUMP_PORT:-22}"
JUMP_USER="${CTYUN_JUMP_USER:-root}"
JUMP_KEY="${CTYUN_JUMP_KEY:-/var/lib/catsco-worker/jump_host_ed25519}"
HTTP_BASE_URL="${CATSCO_WORKER_HTTP_BASE_URL:-https://app.catsco.cc}"
SERVER_URL="${CATSCO_WORKER_SERVER_URL:-wss://app.catsco.cc/v0/channels}"

# --- 校验 ---
if [[ -z "$NAME" || -z "$LOGIN_TOKEN" || -z "$BOT_API_KEY" ]]; then
  echo "error: --name and credentials (--credential-file or both --login-token/--api-key) are required" >&2
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
# 供私钥/known_hosts/身份快照持久化。生产使用 STATE_ROOT 按 tenant
# 隔离；STATE_DIR 保留为测试和旧运维命令的精确目录覆盖。
if [[ -n "${CTYUN_WORKER_STATE_ROOT:-}" ]]; then
  STATE_DIR="${CTYUN_WORKER_STATE_ROOT%/}/${NAME}"
else
  STATE_DIR="${CTYUN_WORKER_STATE_DIR:-/var/lib/catsco-worker/${NAME}}"
fi

# --- 工具 ---
ctyun() {
  local raw status
  raw="$(timeout -s TERM -k 15 120s ctyun-cli "$@" --output json 2>&1)" || {
    echo "error: ctyun-cli failed: $*" >&2; echo "$raw" >&2; return 1
  }
  status="$(jq -r '.statusCode // empty' <<<"$raw")"
  if [[ "$status" != "800" ]]; then
    echo "error: Tianyi Cloud API failed: $(jq -r '.errorCode // ""' <<<"$raw") $(jq -r '.message // ""' <<<"$raw")" >&2
    return 1
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

# 可移植 UUID（clientToken/身份）：优先内核文件，其次 uuidgen，最后时间/pid/随机兜底
gen_uuid() {
  if [[ -r /proc/sys/kernel/random/uuid ]]; then
    cat /proc/sys/kernel/random/uuid
  elif command -v uuidgen >/dev/null 2>&1; then
    uuidgen
  else
    printf 'catsco-%s-%s-%s\n' "$$" "$(date +%s)" "${RANDOM}${RANDOM}"
  fi
}

delete_instance_by_id() {
  # 实测（2026-08-07/08-13）：两个 API 都需 clientToken 且不接受 --projectID。
  # billing_mode 来自创建请求，可在实例目录暂时不可见时作为可靠兜底。
  local del_id="$1"
  local billing_mode="$2"
  [[ -n "$del_id" ]] || return 1
  if [[ "$billing_mode" == "month" ]]; then
    ctyun ecs UnsubscribeEcsInstance --regionID "$REGION_ID" \
      --clientToken "$(gen_uuid)" --instanceID "$del_id" >/dev/null 2>&1
  else
    ctyun ecs DeleteEcsInstance --regionID "$REGION_ID" \
      --clientToken "$(gen_uuid)" --instanceID "$del_id" >/dev/null 2>&1
  fi
}

delete_instance() {
  # 按实例目录里的计费事实删除：包月（expiredTime 非空）→ 退订；按量 → 直接删除。
  local inst_json="$1"
  local del_id=""
  local billing_mode="ondemand"
  del_id="$(jq -r '.instanceID // ""' <<<"$inst_json")"
  [[ -n "$del_id" ]] || return 1
  if [[ -n "$(jq -r '.expiredTime // ""' <<<"$inst_json")" ]]; then
    billing_mode="month"
  fi
  delete_instance_by_id "$del_id" "$billing_mode"
}

cleanup_failed() {
  # fail-closed：删除刚创建的实例 + key pair（尽力，聚合报错）
  local errors=""
  if [[ -n "${CREATED_INSTANCE_ID:-}" ]]; then
    # 天翼云创建接口与实例目录存在短暂最终一致性。先重试目录；仍不可见时，
    # 使用运行等待阶段记住的 instanceID，最后才回退创建响应的 masterResourceID。
    local inst=""
    local attempt
    local cleanup_id="${CREATED_INSTANCE_UUID:-$CREATED_INSTANCE_ID}"
    for attempt in 1 2 3; do
      inst="$(find_instance "$INSTANCE_NAME" 2>/dev/null || true)"
      [[ -n "$inst" ]] && break
      [[ "$attempt" == "3" ]] || sleep 2
    done
    if [[ -n "$inst" ]] && delete_instance "$inst"; then
      : # ok
    elif [[ -z "$inst" && -n "$cleanup_id" ]] && delete_instance_by_id "$cleanup_id" "$BILLING_MODE"; then
      : # ok
    else
      errors="instance delete failed; "
    fi
  fi
  # 只在本次新建 key pair 时删除（复用对象可能仍绑定其他实例，不清理）
  if [[ -n "${KEYPAIR_NAME:-}" && "${KEYPAIR_CREATED:-0}" == "1" ]]; then
    # 实测（2026-08-07）：DeleteEcsKeypair 不接受 --projectID，会报 unknown flag
    if ctyun ecs DeleteEcsKeypair --regionID "$REGION_ID" --keyPairName "$KEYPAIR_NAME" >/dev/null 2>&1; then
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
  # 幂等只接受 running/active；残留的 stopped/error 同名实例不能当"已供给"
  # （真实 API 状态字段是 instanceStatus，2026-08-07 云端实测确认）
  existing_state="$(jq -r '.instanceStatus // .state // .status // ""' <<<"$existing")"
  if [[ "$existing_state" != "running" && "$existing_state" != "active" ]]; then
    echo "error: instance $INSTANCE_NAME already exists but is not running (state=$existing_state); handle it first" >&2
    exit 1
  fi
  echo "{\"status\":\"exists\",\"instance_id\":\"$(jq -r '.instanceID // ""' <<<"$existing")\",\"instance_name\":\"$INSTANCE_NAME\",\"state\":\"$existing_state\"}"
  exit 0
fi

# --- 2. resolve 镜像（指定或最新） ---
if [[ -z "$IMAGE_ID" ]]; then
  # list 输出 TSV，第 5 列为 createdTime（数字毫秒）→ 按最新排序取第一
  IMAGE_ID="$(timeout -s TERM -k 15 90s /opt/catsco/ops/list-worker-images.sh 2>/dev/null \
    | sort -t $'\t' -k5,5nr | head -n1 | cut -f1)"
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
[[ ! -f "$PRIVATE_KEY" ]] || chmod 600 "$PRIVATE_KEY"

# Tenant state became isolated after the first cloud-worker implementation.
# A legacy or partially cleaned tenant can therefore retain its cloud key pair
# after the only matching local private key has disappeared. Reusing that pair
# would create an instance that this control plane can never SSH into. The
# idempotency check above already proved that no same-name tenant instance is
# running, so replace the orphan pair before creating any billable resource.
if [[ -n "$keypair_id" ]] && ! ssh-keygen -y -f "$PRIVATE_KEY" >/dev/null 2>&1; then
  echo "warning: replacing orphaned key pair $KEYPAIR_NAME because the tenant private key is unavailable" >&2
  ctyun ecs DeleteEcsKeypair --regionID "$REGION_ID" --keyPairName "$KEYPAIR_NAME" >/dev/null
  keypair_id=""
fi

if [[ -z "$keypair_id" ]]; then
  # 本次新建的 key pair 才允许失败清理删除（复用的不动，可能仍绑其他实例）
  KEYPAIR_CREATED=1
  # 清除本地残留私钥，避免 ssh-keygen 在非交互环境询问覆盖
  rm -f "$PRIVATE_KEY" "${PRIVATE_KEY}.pub"
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
# reset 重装时由调用方显式传入（保持 bot 身份不变）；缺省才生成新 UUID
BODY_ID="${BODY_ID:-$(gen_uuid)}"
INSTALLATION_ID="${INSTALLATION_ID:-$(gen_uuid)}"

# extIP=0 时不传公网相关参数（带宽/线路/计费），API 会拒绝这些组合
# 计费：month = 包月（--onDemand false + cycleType/cycleCount），ondemand = 按量
create_args=(ecs CreateEcsInstance \
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
  --extIP "$EXT_IP" \
  --monitorService false \
  --securityProduct false \
  --trustInstance false \
  --labelList "[{\"labelKey\":\"purpose\",\"labelValue\":\"catsco-worker\"},{\"labelKey\":\"tenant\",\"labelValue\":\"$NAME\"}]")
if [[ "$BILLING_MODE" == "month" ]]; then
  # 包月：onDemand=false 时 cycleType/cycleCount 生效（MONTH/YEAR）
  create_args+=(--onDemand false --cycleType MONTH --cycleCount "$CYCLE_COUNT")
else
  create_args+=(--onDemand true)
fi
if [[ "$EXT_IP" == "1" ]]; then
  create_args+=(--bandwidth 10 --ipVersion ipv4 --lineType standalone --demandBillingType upflowc)
fi
create_resp="$(ctyun "${create_args[@]}")"
CREATED_INSTANCE_ID="$(jq -r '.returnObj.masterResourceID // empty' <<<"$create_resp")"
[[ -n "$CREATED_INSTANCE_ID" ]] || { echo "error: CreateEcsInstance did not return masterResourceID" >&2; exit 1; }
CREATED_INSTANCE_UUID=""

# --- 5. 等实例 running + 等 SSH（cloud-init done） ---
INSTANCE_IP=""
for _ in $(seq 1 60); do
  inst="$(find_instance "$INSTANCE_NAME")"
  if [[ -n "$inst" ]]; then
    resolved_instance_id="$(jq -r '.instanceID // ""' <<<"$inst")"
    [[ -z "$resolved_instance_id" ]] || CREATED_INSTANCE_UUID="$resolved_instance_id"
    state="$(jq -r '.instanceStatus // .state // .status // ""' <<<"$inst")"
    # 内网模式：fixedIPList[0] 是 VPC 内网 IP；公网模式回退 floatingIP
    ip="$(jq -r '(.fixedIPList[0] // .privateIP // .floatingIP // .publicIP // "")' <<<"$inst")"
    if [[ "$state" == "running" || "$state" == "active" ]]; then
      [[ -n "$ip" ]] && { INSTANCE_IP="$ip"; break; }
    fi
  fi
  sleep 10
done
[[ -n "$INSTANCE_IP" ]] || { echo "error: timed out waiting for instance to be running" >&2; exit 1; }

# SSH 跳板（NAT 架构）：ProxyCommand 经跳板机转发，凭据全来自环境变量
# （不依赖容器内 ~/.ssh/config，容器重建后无需手工恢复）
ssh_opts=(-i "$PRIVATE_KEY" -o BatchMode=yes -o ConnectTimeout=10 \
  -o ServerAliveInterval=15 -o ServerAliveCountMax=3 \
  -o StrictHostKeyChecking=accept-new -o UserKnownHostsFile="$STATE_DIR/known_hosts")
if [[ -n "$JUMP_IP" ]]; then
  ssh_opts+=(-o "ProxyCommand=ssh -i ${JUMP_KEY} -p ${JUMP_PORT} -o BatchMode=yes -o ConnectTimeout=10 -o StrictHostKeyChecking=accept-new -o UserKnownHostsFile=${STATE_DIR}/jump_known_hosts -W %h:%p ${JUMP_USER}@${JUMP_IP}")
fi
# ssh 统一走 timeout 限时（防挂死；与 bake 脚本一致）
ssh_run() {
  timeout -s TERM -k 15 60s ssh "${ssh_opts[@]}" "$@"
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
# 用 printf 逐行生成（heredoc 会在值内二次展开 $ 反引号，破坏含特殊字符的
# api-key/token）；%s 只做一次展开，值原样保留
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

# 保存注入快照（供 reset-worker.sh 无参数重建时复用身份），chmod 600
printf '%s\n' "$ENV_CONTENT" > "$STATE_DIR/inject.env"
chmod 600 "$STATE_DIR/inject.env"

# --- 6.5 写 localConfig（bootstrap 身份，必需） ---
# worker 的 catsco 命令（ExecStart: dist/index.js catsco）通过
# resolveCatsCoRuntimeConfig 解析凭证，且该入口不开 migrateLegacyEnvBinding：
#   - bodyId 只从 localConfig.device.bodyId 读（.env 里的 CATSCO_BODY_ID 不生效）
#   - botUid/apiKey 需要 hasConfirmedLocalBotBinding（localConfig.currentBot）
# 只写 .env 时 connector 不 ready，worker 会 exit(1) "配置缺失"。
# 因此 bootstrap 必须写 /srv/catsco-agent/.xiaoba/catsco.json（version 1 schema，
# runtimeRoot = XIAOBA_USER_DATA_DIR = /srv/catsco-agent）。
LOCAL_CONFIG_JSON="$(jq -n \
  --arg http "$HTTP_BASE_URL" \
  --arg server "$SERVER_URL" \
  --arg token "$LOGIN_TOKEN" \
  --arg uid "$USER_UID" \
  --arg uname "$USER_NAME" \
  --arg display "$USER_DISPLAY" \
  --arg botUid "$BOT_UID" \
  --arg apiKey "$BOT_API_KEY" \
  --arg bodyId "$BODY_ID" \
  --arg installId "$INSTALLATION_ID" \
  --arg tenant "$NAME" \
  --arg now "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
  '{version:1, endpoints:{httpBaseUrl:$http, serverUrl:$server}, account:{token:$token, uid:$uid, username:$uname, displayName:$display}, currentBot:{uid:$botUid, name:"Bot", username:"", apiKey:$apiKey, boundAt:$now, boundByUserUid:$uid, bindingSource:"cloud-provision"}, device:{deviceId:$bodyId, bodyId:$bodyId, installationId:$installId, name:$tenant}, updatedAt:$now}')"
ssh_run "root@$INSTANCE_IP" "install -d -o catsco-agent -g catsco-agent /srv/catsco-agent/.xiaoba && cat > /srv/catsco-agent/.xiaoba/catsco.json && chown catsco-agent:catsco-agent /srv/catsco-agent/.xiaoba/catsco.json && chmod 600 /srv/catsco-agent/.xiaoba/catsco.json" <<<"$LOCAL_CONFIG_JSON"

# --- 7. 启用 service ---
# 输出重定向：is-active 的 stdout（"active"）会污染下方 JSON 约定，仅保留退出码
ssh_run "root@$INSTANCE_IP" "systemctl enable --now catsco-agent.service && sleep 3 && systemctl is-active catsco-agent.service" >/dev/null 2>&1

APP_VERSION="$(ssh_run "root@$INSTANCE_IP" \
  "cat /opt/catsco/current/worker-release.json 2>/dev/null" 2>/dev/null \
  | jq -r '.version // empty' 2>/dev/null || true)"
if [[ "$APP_VERSION" =~ ^[0-9A-Za-z][0-9A-Za-z._+-]{0,63}$ ]]; then
  printf '%s\n' "$APP_VERSION" > "$STATE_DIR/app_version.tmp"
  mv -f "$STATE_DIR/app_version.tmp" "$STATE_DIR/app_version"
else
  echo "warning: provisioned application version could not be persisted" >&2
fi

echo "{\"status\":\"provisioned\",\"instance_id\":\"$CREATED_INSTANCE_ID\",\"instance_name\":\"$INSTANCE_NAME\",\"ip\":\"$INSTANCE_IP\",\"image_id\":\"$IMAGE_ID\"}"
trap - EXIT
