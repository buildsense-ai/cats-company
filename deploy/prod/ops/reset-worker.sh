#!/usr/bin/env bash
# reset-worker.sh — 云托管虚拟员工重置/重装（B4-1d）
#
# 丢弃数据语义：先销毁现有实例（destroy-worker.sh），再从指定/最新
# 镜像重建并重新供给（provision-worker.sh）。worker 运行时数据
# （/srv/catsco-agent）随实例销毁而清空；bot 记录在服务器侧保留。
#
# 注入凭证：优先命令行参数，缺省回退 provision 写入
# $STATE_DIR/inject.env 的身份快照（同一 tenant 重建身份不变）。
#
# 用法：
#   reset-worker.sh --name <tenant> [--image-id <id>] \
#     [--login-token <jwt>] [--api-key <key>] [--bot-uid <uid>] \
#     [--user-uid <uid>] [--user-name <n>] [--user-display <d>] [--dry-run]
#
# 依赖：同 provision/destroy（ctyun-cli + jq + ssh + ssh-keygen + timeout）
# 云环境：CTYUN_WORKER_REGION_ID / _PROJECT_ID / _AZ_NAME / _FLAVOR_ID /
#         _VPC_ID / _SUBNET_ID / _SECURITY_GROUP_ID
set -Eeuo pipefail

NAME=""
IMAGE_ID=""
LOGIN_TOKEN=""
BOT_API_KEY=""
BOT_UID=""
USER_UID=""
USER_NAME=""
USER_DISPLAY=""
DRY_RUN=0

usage() {
  sed -n '2,19p' "$0" | sed 's/^# \{0,1\}//'
}

while (($#)); do
  case "$1" in
    --name) NAME="${2:-}"; shift 2 ;;
    --image-id) IMAGE_ID="${2:-}"; shift 2 ;;
    --login-token) LOGIN_TOKEN="${2:-}"; shift 2 ;;
    --api-key) BOT_API_KEY="${2:-}"; shift 2 ;;
    --bot-uid) BOT_UID="${2:-}"; shift 2 ;;
    --user-uid) USER_UID="${2:-}"; shift 2 ;;
    --user-name) USER_NAME="${2:-}"; shift 2 ;;
    --user-display) USER_DISPLAY="${2:-}"; shift 2 ;;
    --dry-run) DRY_RUN=1; shift ;;
    -h|--help) usage; exit 0 ;;
    *) echo "error: unknown argument: $1" >&2; usage >&2; exit 2 ;;
  esac
done

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

OPS_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
STATE_DIR="${CTYUN_WORKER_STATE_DIR:-/var/lib/catsco-worker/${NAME}}"

# --- 身份快照回退（命令行优先） ---
if [[ -f "$STATE_DIR/inject.env" ]]; then
  [[ -n "$LOGIN_TOKEN" ]] || LOGIN_TOKEN="$(sed -n 's/^CATSCO_USER_TOKEN=//p' "$STATE_DIR/inject.env" | tail -n1)"
  [[ -n "$BOT_API_KEY" ]] || BOT_API_KEY="$(sed -n 's/^CATSCO_API_KEY=//p' "$STATE_DIR/inject.env" | tail -n1)"
  [[ -n "$BOT_UID" ]] || BOT_UID="$(sed -n 's/^CATSCO_BOT_UID=//p' "$STATE_DIR/inject.env" | tail -n1)"
  [[ -n "$USER_UID" ]] || USER_UID="$(sed -n 's/^CATSCO_USER_UID=//p' "$STATE_DIR/inject.env" | tail -n1)"
  [[ -n "$USER_NAME" ]] || USER_NAME="$(sed -n 's/^CATSCO_USER_NAME=//p' "$STATE_DIR/inject.env" | tail -n1)"
  [[ -n "$USER_DISPLAY" ]] || USER_DISPLAY="$(sed -n 's/^CATSCO_USER_DISPLAY_NAME=//p' "$STATE_DIR/inject.env" | tail -n1)"
fi

# 重供给需要完整身份（bot 连接凭证 + 创建者登录凭证）
if [[ -z "$BOT_API_KEY" || -z "$LOGIN_TOKEN" ]]; then
  echo "error: --api-key and --login-token are required (no inject.env snapshot found)" >&2
  exit 2
fi

# --- 1. 销毁现有实例（幂等；dry-run 透传） ---
if [[ $DRY_RUN -eq 1 ]]; then
  "$OPS_DIR/destroy-worker.sh" --name "$NAME" --dry-run
else
  "$OPS_DIR/destroy-worker.sh" --name "$NAME"
fi

# --- 2. 从指定/最新镜像重建并重新供给 ---
prov_args=(--name "$NAME" --login-token "$LOGIN_TOKEN" --api-key "$BOT_API_KEY")
[[ -n "$IMAGE_ID" ]] && prov_args+=(--image-id "$IMAGE_ID")
[[ -n "$BOT_UID" ]] && prov_args+=(--bot-uid "$BOT_UID")
[[ -n "$USER_UID" ]] && prov_args+=(--user-uid "$USER_UID")
[[ -n "$USER_NAME" ]] && prov_args+=(--user-name "$USER_NAME")
[[ -n "$USER_DISPLAY" ]] && prov_args+=(--user-display "$USER_DISPLAY")
[[ $DRY_RUN -eq 1 ]] && prov_args+=(--dry-run)

"$OPS_DIR/provision-worker.sh" "${prov_args[@]}"
