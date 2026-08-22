#!/usr/bin/env bash
# reset-worker.sh — 云托管虚拟员工重置/重装（B4-1d）
#
# 丢弃数据语义：先销毁现有实例（destroy-worker.sh），再从指定/最新
# 镜像重建并重新供给（provision-worker.sh）。worker 运行时数据
# （/srv/catsco-agent）随实例销毁而清空；bot 记录在服务器侧保留。
#
# 注入凭证：优先 0600 credential file，其次命令行参数，最后回退 provision 写入
# $STATE_DIR/inject.env 的身份快照（同一 tenant 重建身份不变）。
#
# 用法：
#   reset-worker.sh --name <tenant> [--version <v> | --image-id <id>] \
#     [--credential-file <0600-file>] [--login-token <jwt>] [--api-key <key>] [--bot-uid <uid>] \
#     [--user-uid <uid>] [--user-name <n>] [--user-display <d>] [--dry-run]
#
# --version 指定 bake 镜像版本（从 list-worker-images.sh 解析对应 image id），
# 缺省使用最新镜像。
#
# 依赖：同 provision/destroy（ctyun-cli + jq + ssh + ssh-keygen + timeout）
# 云环境：CTYUN_WORKER_REGION_ID / _PROJECT_ID / _AZ_NAME / _FLAVOR_ID /
#         _VPC_ID / _SUBNET_ID / _SECURITY_GROUP_ID
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
  sed -n '2,19p' "$0" | sed 's/^# \{0,1\}//'
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
if [[ -n "${CTYUN_WORKER_STATE_ROOT:-}" ]]; then
  STATE_DIR="${CTYUN_WORKER_STATE_ROOT%/}/${NAME}"
else
  STATE_DIR="${CTYUN_WORKER_STATE_DIR:-/var/lib/catsco-worker/${NAME}}"
fi

# --- 版本 → 镜像映射（可选；显式 --image-id 优先） ---
if [[ -n "$VERSION" ]]; then
  if [[ ! "$VERSION" =~ ^[A-Za-z0-9._-]+$ ]]; then
    echo "error: --version must match ^[A-Za-z0-9._-]+\$" >&2
    exit 2
  fi
  if [[ -z "$IMAGE_ID" ]]; then
    # 从 bake 通道镜像列表里找该版本的 imageID（list-worker-images.sh TSV：
    # imageID<TAB>name<TAB>version<TAB>commit<TAB>createdTime<TAB>status）。
    # PATH 优先（测试注入 fake），生产回退同目录脚本。
    LIST_IMAGES_CMD="$(command -v list-worker-images.sh || true)"
    [[ -n "$LIST_IMAGES_CMD" ]] || LIST_IMAGES_CMD="$OPS_DIR/list-worker-images.sh"
    IMAGE_ID="$("$LIST_IMAGES_CMD" | awk -F'\t' -v v="$VERSION" '$3 == v { print $1; exit }')"
    if [[ -z "$IMAGE_ID" ]]; then
      echo "error: no image found for version: $VERSION" >&2
      exit 2
    fi
  fi
fi

# --- 身份快照回退（命令行优先） ---
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
PROV_CREDENTIAL_FILE="$CREDENTIAL_FILE"
if [[ -z "$PROV_CREDENTIAL_FILE" ]]; then
  PROV_CREDENTIAL_FILE="$(mktemp "${TMPDIR:-/tmp}/catsco-worker-credentials.XXXXXX")"
  chmod 600 "$PROV_CREDENTIAL_FILE"
  printf '%s\n%s\n' "$LOGIN_TOKEN" "$BOT_API_KEY" > "$PROV_CREDENTIAL_FILE"
  trap 'rm -f -- "$PROV_CREDENTIAL_FILE"' EXIT
fi
prov_args=(--name "$NAME" --credential-file "$PROV_CREDENTIAL_FILE")
[[ -n "$IMAGE_ID" ]] && prov_args+=(--image-id "$IMAGE_ID")
[[ -n "$BOT_UID" ]] && prov_args+=(--bot-uid "$BOT_UID")
[[ -n "$USER_UID" ]] && prov_args+=(--user-uid "$USER_UID")
[[ -n "$USER_NAME" ]] && prov_args+=(--user-name "$USER_NAME")
[[ -n "$USER_DISPLAY" ]] && prov_args+=(--user-display "$USER_DISPLAY")
  [[ -n "$BODY_ID" ]] && prov_args+=(--body-id "$BODY_ID")
  [[ -n "$INSTALLATION_ID" ]] && prov_args+=(--installation-id "$INSTALLATION_ID")
  [[ $DRY_RUN -eq 1 ]] && prov_args+=(--dry-run)
"$OPS_DIR/provision-worker.sh" "${prov_args[@]}"
