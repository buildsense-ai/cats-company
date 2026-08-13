#!/usr/bin/env bash
# rollback-worker.sh — 云托管虚拟员工版本回滚（B4-1e）
#
# 保留 worker 数据（/srv/catsco-agent 不动），SSH 到实例把
# /opt/catsco/current 软链切换到指定历史 release 版本并重启
# catsco-agent.service。--version 缺省时回滚到实例内最新 release 版本。
#
# 用法：
#   rollback-worker.sh --name <tenant> [--version <v>] [--dry-run]
#
# 保留数据：不删除/不重建实例，不触碰 /srv/catsco-agent。
#
# 依赖：ctyun-cli + jq + ssh + timeout
# 云环境：CTYUN_WORKER_REGION_ID / _PROJECT_ID
# 本地：CTYUN_WORKER_STATE_DIR（私钥/known_hosts，provision 时生成）
set -Eeuo pipefail

NAME=""
VERSION=""
DRY_RUN=0

usage() {
  sed -n '2,16p' "$0" | sed 's/^# \{0,1\}//'
}

while (($#)); do
  case "$1" in
    --name) NAME="${2:-}"; shift 2 ;;
    --version) VERSION="${2:-}"; shift 2 ;;
    --dry-run) DRY_RUN=1; shift ;;
    -h|--help) usage; exit 0 ;;
    *) echo "error: unknown argument: $1" >&2; usage >&2; exit 2 ;;
  esac
done

REGION_ID="${CTYUN_WORKER_REGION_ID:-}"
PROJECT_ID="${CTYUN_WORKER_PROJECT_ID:-0}"
STATE_DIR="${CTYUN_WORKER_STATE_DIR:-/var/lib/catsco-worker/${NAME}}"
# SSH 跳板（ProxyJump）：SSH config 别名或 user@host；空 = 直连公网 IP
JUMP_HOST="${CTYUN_JUMP_HOST:-}"

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
# version 只允许安全字符（会拼进远程 glob，防注入）
if [[ -n "$VERSION" && ! "$VERSION" =~ ^[A-Za-z0-9._-]+$ ]]; then
  echo "error: --version must match ^[A-Za-z0-9._-]+\$" >&2
  exit 2
fi
for cmd in ctyun-cli jq ssh timeout; do
  command -v "$cmd" >/dev/null 2>&1 || { echo "error: missing required command: $cmd" >&2; exit 2; }
done
[[ -n "$REGION_ID" ]] || { echo "error: CTYUN_WORKER_REGION_ID is required" >&2; exit 2; }

INSTANCE_NAME="worker-${NAME}"

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

# --- 1. 找实例 + IP ---
inst="$(find_instance "$INSTANCE_NAME")"
[[ -n "$inst" ]] || { echo "error: instance worker-${NAME} not found" >&2; exit 1; }
# 内网模式：fixedIPList[0] 是 VPC 内网 IP；公网模式回退 floatingIP
INSTANCE_IP="$(jq -r '(.fixedIPList[0] // .privateIP // .floatingIP // .publicIP // "")' <<<"$inst")"
[[ -n "$INSTANCE_IP" ]] || { echo "error: instance has no IP" >&2; exit 1; }
PRIVATE_KEY="$STATE_DIR/id_rsa"
[[ -f "$PRIVATE_KEY" ]] || { echo "error: private key not found at $PRIVATE_KEY (was the worker provisioned here?)" >&2; exit 1; }

ssh_opts=(-i "$PRIVATE_KEY" -o BatchMode=yes -o ConnectTimeout=10 \
  -o ServerAliveInterval=15 -o ServerAliveCountMax=3 \
  -o StrictHostKeyChecking=accept-new -o UserKnownHostsFile="$STATE_DIR/known_hosts")
[[ -n "$JUMP_HOST" ]] && ssh_opts+=(-J "$JUMP_HOST")
ssh_run() {
  timeout -s TERM -k 15 60s ssh "${ssh_opts[@]}" "$@"
}

# --- 2. 无 --version：回滚到最新 release 版本 ---
# （list 语义不再单独输出：空版本 = 按实例内已部署版本排序取最新并切换）
if [[ -z "$VERSION" ]]; then
  target="$(ssh_run "root@$INSTANCE_IP" "ls -1d /opt/catsco/releases/*/ 2>/dev/null | xargs -n1 basename | sort -V | tail -n1" 2>/dev/null || true)"
  [[ -n "$target" ]] || { echo "error: no releases found on instance" >&2; exit 1; }

  if [[ $DRY_RUN -eq 1 ]]; then
    echo "{\"status\":\"dry-run\",\"instance_name\":\"$INSTANCE_NAME\",\"version\":\"$target\"}"
    exit 0
  fi

  ssh_run "root@$INSTANCE_IP" "ln -sfn /opt/catsco/releases/${target} /opt/catsco/current && systemctl restart catsco-agent.service && sleep 3 && systemctl is-active catsco-agent.service" >/dev/null 2>&1 \
    || { echo "error: rollback to $target failed" >&2; exit 1; }

  echo "{\"status\":\"rolled-back\",\"instance_name\":\"$INSTANCE_NAME\",\"version\":\"$target\"}"
  exit 0
fi

# --- 3. 切换 current 软链到目标版本并重启 service ---
# 前缀匹配 <version>-<sha> 的 release 目录（glob 受上面正则约束）
target="$(ssh_run "root@$INSTANCE_IP" "ls -1d /opt/catsco/releases/${VERSION}*/ 2>/dev/null | head -n1 | xargs -n1 basename" 2>/dev/null || true)"
[[ -n "$target" ]] || { echo "error: release $VERSION not found on instance" >&2; exit 1; }

if [[ $DRY_RUN -eq 1 ]]; then
  echo "{\"status\":\"dry-run\",\"instance_name\":\"$INSTANCE_NAME\",\"version\":\"$target\"}"
  exit 0
fi

ssh_run "root@$INSTANCE_IP" "ln -sfn /opt/catsco/releases/${target} /opt/catsco/current && systemctl restart catsco-agent.service && sleep 3 && systemctl is-active catsco-agent.service" >/dev/null 2>&1 \
  || { echo "error: rollback to $target failed" >&2; exit 1; }

echo "{\"status\":\"rolled-back\",\"instance_name\":\"$INSTANCE_NAME\",\"version\":\"$target\"}"
