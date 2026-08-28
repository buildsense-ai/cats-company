#!/usr/bin/env bash
# destroy-worker.sh — 云托管虚拟员工销毁（B4-1c）
#
# 按实例名 worker-<tenant> 找到云实例并永久删除，同时清理 key pair
# worker-key-<tenant> 与本地 state 目录。包月实例会先退订，等待进入
# unsubscribed，再调用 DestroyEcsInstance。fail-closed：删除失败聚合报告。
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
if [[ -n "${CTYUN_WORKER_STATE_ROOT:-}" ]]; then
  STATE_DIR="${CTYUN_WORKER_STATE_ROOT%/}/${NAME}"
else
  STATE_DIR="${CTYUN_WORKER_STATE_DIR:-/var/lib/catsco-worker/${NAME}}"
fi
OPS_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ARTIFACT_ROUTE_SCRIPT="${CATSCO_ARTIFACT_GATEWAY_ROUTE_SCRIPT:-$OPS_DIR/artifact-gateway-route.sh}"
BOT_UID="$(sed -n 's/^CATSCO_BOT_UID=//p' "$STATE_DIR/inject.env" 2>/dev/null | tail -n1 || true)"

# --- 工具 ---
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

find_instance() {
  local resp name
  name="$1"
  resp="$(ctyun ecs ListEcsInstances --regionID "$REGION_ID" --projectID "$PROJECT_ID" \
    --instanceName "$name" --pageNo 1 --pageSize 10)"
  jq -r --arg n "$name" '.returnObj.results[]? | select(.instanceName == $n)' <<<"$resp" || true
}

stop_instance() {
  local inst_json="$1" instance_id current status attempt
  instance_id="$(jq -r '.instanceID // ""' <<<"$inst_json")"
  [[ -n "$instance_id" ]] || return 1
  status="$(jq -r '.instanceStatus // .state // ""' <<<"$inst_json" | tr '[:upper:]' '[:lower:]')"
  case "$status" in
    stopped|shutoff|expired|freezing|frozen|unsubscribed|error|bootdiskexpired|nobootdisk) return 0 ;;
  esac
  ctyun ecs StopEcsInstance --regionID "$REGION_ID" --instanceID "$instance_id" --force false >/dev/null 2>&1 \
    || { echo "error: instance stop failed (instance_id=$instance_id)" >&2; return 1; }
  for attempt in $(seq 1 30); do
    sleep 2
    current="$(find_instance "$INSTANCE_NAME")"
    [[ -n "$current" ]] || return 0
    status="$(jq -r '.instanceStatus // .state // ""' <<<"$current" | tr '[:upper:]' '[:lower:]')"
    case "$status" in
      stopped|shutoff|expired|freezing|frozen|unsubscribed|error|bootdiskexpired|nobootdisk) return 0 ;;
    esac
  done
  echo "error: timed out waiting for instance stop (instance_id=$instance_id)" >&2
  return 1
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
instance_status=""
inst="$(find_instance "$INSTANCE_NAME")"
if [[ -n "$inst" ]]; then
  instance_id="$(jq -r '.instanceID // ""' <<<"$inst")"
  if [[ $DRY_RUN -eq 1 ]]; then
    echo "{\"status\":\"dry-run\",\"instance_name\":\"$INSTANCE_NAME\",\"instance_id\":\"$instance_id\"}"
    exit 0
  fi
  # 包月实例先退订，再永久销毁；按量实例走 DeleteEcsInstance。
  # 这些 API 都需 clientToken 且不接受 --projectID。
  if [[ -n "$instance_id" ]]; then
    instance_status="$(jq -r '.instanceStatus // .state // ""' <<<"$inst" | tr '[:upper:]' '[:lower:]')"
    if [[ -n "$(jq -r '.expiredTime // ""' <<<"$inst")" \
      || "$instance_status" =~ ^(expired|freezing|frozen|unsubscribed|bootdiskexpired)$ ]]; then
      stop_instance "$inst" || exit 1
      if [[ "$instance_status" != "unsubscribed" ]]; then
        ctyun ecs UnsubscribeEcsInstance \
          --regionID "$REGION_ID" --clientToken "$(gen_uuid)" --instanceID "$instance_id" >/dev/null 2>&1 \
          || { echo "error: instance unsubscribe failed (instance_id=$instance_id)" >&2; exit 1; }
      fi

      destroy_ready=0
      already_removed=0
      for _ in $(seq 1 60); do
        current="$(find_instance "$INSTANCE_NAME")"
        if [[ -z "$current" ]]; then
          already_removed=1
          break
        fi
        current_status="$(jq -r '.instanceStatus // .state // ""' <<<"$current" | tr '[:upper:]' '[:lower:]')"
        case "$current_status" in
          unsubscribed) destroy_ready=1; break ;;
          released|deleted) already_removed=1; break ;;
        esac
        sleep 2
      done
      if [[ "$already_removed" -eq 0 && "$destroy_ready" -eq 0 ]]; then
        echo "error: timed out waiting for instance unsubscribe (instance_id=$instance_id)" >&2
        exit 1
      fi
      if [[ "$destroy_ready" -eq 1 ]]; then
        ctyun ecs DestroyEcsInstance \
          --regionID "$REGION_ID" --clientToken "$(gen_uuid)" --instanceID "$instance_id" >/dev/null 2>&1 \
          || { echo "error: instance permanent destroy failed (instance_id=$instance_id)" >&2; exit 1; }
        removed=0
        for _ in $(seq 1 60); do
          current="$(find_instance "$INSTANCE_NAME")"
          if [[ -z "$current" ]]; then
            removed=1
            break
          fi
          current_status="$(jq -r '.instanceStatus // .state // ""' <<<"$current" | tr '[:upper:]' '[:lower:]')"
          case "$current_status" in
            released|deleted) removed=1; break ;;
          esac
          sleep 2
        done
        if [[ "$removed" -eq 0 ]]; then
          echo "error: timed out waiting for permanent instance destroy (instance_id=$instance_id)" >&2
          exit 1
        fi
      fi
    else
      ctyun ecs DeleteEcsInstance \
        --regionID "$REGION_ID" --clientToken "$(gen_uuid)" --instanceID "$instance_id" >/dev/null 2>&1 \
        || { echo "error: instance delete failed (instance_id=$instance_id)" >&2; exit 1; }
    fi
  fi
fi
if [[ $DRY_RUN -eq 1 ]]; then
  echo "{\"status\":\"dry-run\",\"instance_name\":\"$INSTANCE_NAME\"}"
  exit 0
fi

# Artifact routing is auxiliary. A stale route should be removed, but a
# gateway outage must not turn a completed instance deletion into a failed
# worker destroy operation.
if [[ "${CATSCO_ARTIFACT_GATEWAY_ENABLED:-0}" == "1" && "$BOT_UID" =~ ^[1-9][0-9]{0,18}$ ]]; then
  if ! "$ARTIFACT_ROUTE_SCRIPT" remove "$BOT_UID" >/dev/null; then
    echo "warning: Artifact route cleanup failed for Agent $BOT_UID" >&2
  fi
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
