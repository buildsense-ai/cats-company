#!/usr/bin/env bash
# destroy-worker.sh — 云托管虚拟员工销毁（B4-1c）
#
# 按实例名 worker-<tenant> 找到云实例并删除，同时清理 key pair
# worker-key-<tenant> 与本地 state 目录。fail-closed：删除失败聚合报告。
#
# 用法：
#   destroy-worker.sh --name <tenant> [--dry-run]
#
# 幂等：实例/key pair 不存在按"已清理"处理（exit 0）。
#
# 依赖：ctyun-cli + jq + timeout
# 凭据：CTYUN_AK/CTYUN_SK（ctyun-cli 环境变量或 ~/.ctyun-cli.yaml）
# 云环境：CTYUN_WORKER_REGION_ID / _PROJECT_ID
set -Eeuo pipefail

NAME=""
DRY_RUN=0

usage() {
  sed -n '2,14p' "$0" | sed 's/^# \{0,1\}//'
}

while (($#)); do
  case "$1" in
    --name) NAME="${2:-}"; shift 2 ;;
    --dry-run) DRY_RUN=1; shift ;;
    -h|--help) usage; exit 0 ;;
    *) echo "error: unknown argument: $1" >&2; usage >&2; exit 2 ;;
  esac
done

REGION_ID="${CTYUN_WORKER_REGION_ID:-}"
PROJECT_ID="${CTYUN_WORKER_PROJECT_ID:-0}"

# --- 校验 ---
if [[ -z "$NAME" ]]; then
  echo "error: --name is required" >&2
  usage >&2
  exit 2
fi
if [[ ! "$NAME" =~ ^[a-z0-9][a-z0-9_-]{1,63}$ ]]; then
  echo "error: --name must match ^[a-z0-9][a-z0-9_-]{1,63}\$" >&2
  exit 2
fi
for cmd in ctyun-cli jq timeout; do
  command -v "$cmd" >/dev/null 2>&1 || { echo "error: missing required command: $cmd" >&2; exit 2; }
done
[[ -n "$REGION_ID" ]] || { echo "error: CTYUN_WORKER_REGION_ID is required" >&2; exit 2; }

INSTANCE_NAME="worker-${NAME}"
KEYPAIR_NAME="worker-key-${NAME}"
STATE_DIR="${CTYUN_WORKER_STATE_DIR:-/var/lib/catsco-worker/${NAME}}"

# --- 工具 ---
ctyun() {
  local raw status
  raw="$(timeout --signal=TERM --kill-after=15s 120s ctyun-cli "$@" --output json 2>&1)" || {
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

find_instance() {
  local resp name
  name="$1"
  resp="$(ctyun ecs ListEcsInstances --regionID "$REGION_ID" --projectID "$PROJECT_ID" \
    --instanceName "$name" --pageNo 1 --pageSize 10)"
  jq -r --arg n "$name" '.returnObj.results[]? | select(.instanceName == $n)' <<<"$resp" || true
}

# 可移植 UUID（clientToken）：优先内核文件，其次 uuidgen，最后时间/pid/随机兜底
gen_uuid() {
  if [[ -r /proc/sys/kernel/random/uuid ]]; then
    cat /proc/sys/kernel/random/uuid
  elif command -v uuidgen >/dev/null 2>&1; then
    uuidgen
  else
    printf 'catsco-%s-%s-%s\n' "$$" "$(date +%s)" "${RANDOM}${RANDOM}"
  fi
}

# --- 1. 删实例（不存在则跳过） ---
instance_id=""
inst="$(find_instance "$INSTANCE_NAME")"
if [[ -n "$inst" ]]; then
  instance_id="$(jq -r '.instanceID // ""' <<<"$inst")"
  if [[ $DRY_RUN -eq 1 ]]; then
    echo "{\"status\":\"dry-run\",\"instance_name\":\"$INSTANCE_NAME\",\"instance_id\":\"$instance_id\"}"
    exit 0
  fi
  # 实测（2026-08-07）：DeleteEcsInstance 需 clientToken 且不接受 --projectID
  if [[ -n "$instance_id" ]] && ! ctyun ecs DeleteEcsInstance \
      --regionID "$REGION_ID" --clientToken "$(gen_uuid)" --instanceID "$instance_id" >/dev/null 2>&1; then
    echo "error: instance delete failed (instance_id=$instance_id)" >&2
    exit 1
  fi
fi
if [[ $DRY_RUN -eq 1 ]]; then
  echo "{\"status\":\"dry-run\",\"instance_name\":\"$INSTANCE_NAME\"}"
  exit 0
fi

# --- 2. 删 key pair（幂等：不存在则跳过） ---
errors=""
kp="$(ctyun ecs GetEcsKeypairDetails --regionID "$REGION_ID" --projectID "$PROJECT_ID" \
  --keyPairName "$KEYPAIR_NAME" --pageNo 1 --pageSize 10 \
  | jq -r --arg n "$KEYPAIR_NAME" '.returnObj.results[]? | select(.keyPairName == $n) | .keyPairID' | head -n1)"
if [[ -n "$kp" ]]; then
  # 实测（2026-08-07）：DeleteEcsKeypair 不接受 --projectID，会报 unknown flag
  if ctyun ecs DeleteEcsKeypair --regionID "$REGION_ID" --keyPairName "$KEYPAIR_NAME" >/dev/null 2>&1; then
    : # ok
  else
    errors="key pair delete failed; "
  fi
fi

# --- 3. 清理本地 state（尽力，不阻塞结果） ---
# 防御：CTYUN_WORKER_STATE_DIR 来自环境变量——必须绝对路径、不含 .. 、
# 非根/非 HOME，否则拒绝（防止 rm -rf 逃逸到 / 或祖先目录）
if [[ -z "$STATE_DIR" || "$STATE_DIR" == "/" || "$STATE_DIR" == "//" || "$STATE_DIR" == "$HOME" \
  || "$STATE_DIR" != /* || "$STATE_DIR" == *..* ]]; then
  echo "warning: refusing to remove unsafe STATE_DIR='$STATE_DIR'" >&2
else
  [[ ! -d "$STATE_DIR" ]] || rm -rf "$STATE_DIR" 2>/dev/null || true
fi

if [[ -n "$errors" ]]; then
  echo "error: destroy incomplete: ${errors}" >&2
  exit 1
fi

if [[ -z "$instance_id" ]]; then
  echo "{\"status\":\"not-found\",\"instance_name\":\"$INSTANCE_NAME\"}"
else
  echo "{\"status\":\"destroyed\",\"instance_name\":\"$INSTANCE_NAME\",\"instance_id\":\"$instance_id\"}"
fi
