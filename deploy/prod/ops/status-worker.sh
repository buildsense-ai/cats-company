#!/usr/bin/env bash
# status-worker.sh — 批量输出 worker 实例的云侧状态（供控制面
# /api/cloud-workers 列表实时显示状态/版本/镜像）。
#
# 输出：每行 TSV
# `instanceName<TAB>instanceStatus<TAB>imageID<TAB>imageVersion<TAB>appVersion`
# instanceStatus 为天翼云 ListEcsInstances 的 instanceStatus
# （creating/running/stopped/error/...，不存在 = 无此行，控制面按 missing 处理）。
# imageVersion 由 bake 镜像列表按 imageID 关联；appVersion 来自控制面在
# tenant 状态目录持久化的已验证版本。旧实例首次读取时会通过 SSH 查询
# /opt/catsco/current/worker-release.json 并回填，之后不再重复远程探测。
#
# 依赖：ctyun-cli + jq + timeout
# 凭据：CTYUN_AK/CTYUN_SK（ctyun-cli 环境变量或 ~/.ctyun-cli.yaml）
# 云环境：CTYUN_WORKER_REGION_ID / CTYUN_WORKER_PROJECT_ID（实例所在项目）/
#         CTYUN_IMAGE_PROJECT_ID（bake 镜像所在项目，默认 0）
set -Eeuo pipefail

REGION_ID="${CTYUN_WORKER_REGION_ID:-}"
PROJECT_ID="${CTYUN_WORKER_PROJECT_ID:-0}"
IMAGE_PROJECT_ID="${CTYUN_IMAGE_PROJECT_ID:-0}"

if [[ -z "$REGION_ID" ]]; then
  echo "error: CTYUN_WORKER_REGION_ID is required" >&2
  exit 2
fi
for cmd in ctyun-cli jq timeout ssh; do
  command -v "$cmd" >/dev/null 2>&1 || { echo "error: missing required command: $cmd" >&2; exit 2; }
done

STATE_ROOT="${CTYUN_WORKER_STATE_ROOT:-/var/lib/catsco-worker}"
JUMP_IP="${CTYUN_JUMP_IP:-}"
JUMP_PORT="${CTYUN_JUMP_PORT:-22}"
JUMP_USER="${CTYUN_JUMP_USER:-root}"
JUMP_KEY="${CTYUN_JUMP_KEY:-$STATE_ROOT/jump_host_ed25519}"

read_app_version() {
  local name="$1" status="$2" ip="$3"
  local tenant="${name#worker-}" state_dir="$STATE_ROOT/${name#worker-}"
  local version_file="$state_dir/app_version" private_key="$state_dir/id_rsa"
  local version=""

  if [[ -f "$version_file" ]]; then
    version="$(tr -d '\r\n' < "$version_file" 2>/dev/null || true)"
  fi
  if [[ "$version" =~ ^[0-9A-Za-z][0-9A-Za-z._+-]{0,63}$ ]]; then
    printf '%s' "$version"
    return 0
  fi

  [[ "$status" == "running" || "$status" == "active" ]] || return 0
  [[ -n "$tenant" && -n "$ip" && -f "$private_key" ]] || return 0

  local ssh_opts=(-i "$private_key" -o BatchMode=yes -o ConnectTimeout=4 \
    -o ServerAliveInterval=3 -o ServerAliveCountMax=1 \
    -o StrictHostKeyChecking=accept-new -o UserKnownHostsFile="$state_dir/known_hosts")
  if [[ -n "$JUMP_IP" ]]; then
    ssh_opts+=(-o "ProxyCommand=ssh -i ${JUMP_KEY} -p ${JUMP_PORT} -o BatchMode=yes -o ConnectTimeout=4 -o StrictHostKeyChecking=accept-new -o UserKnownHostsFile=${state_dir}/jump_known_hosts -W %h:%p ${JUMP_USER}@${JUMP_IP}")
  fi

  version="$(timeout -s TERM -k 2 8s ssh "${ssh_opts[@]}" "root@$ip" \
    "cat /opt/catsco/current/worker-release.json 2>/dev/null" 2>/dev/null \
    | jq -r '.version // empty' 2>/dev/null || true)"
  if [[ "$version" =~ ^[0-9A-Za-z][0-9A-Za-z._+-]{0,63}$ ]]; then
    mkdir -p "$state_dir"
    printf '%s\n' "$version" > "$version_file.tmp"
    mv -f "$version_file.tmp" "$version_file"
    printf '%s' "$version"
  fi
}

# 调 ctyun-cli 并校验 statusCode（fail-closed）
ctyun() {
  local raw status
  raw="$(timeout -s TERM -k 15 90s ctyun-cli "$@" --output json 2>&1)" || {
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

# --- 1. 拉取全部 worker 实例（instanceName 以 worker- 开头） ---
# ListEcsInstances 支持分页；按名称过滤后仍可能跨页，翻页到 totalPage。
instance_rows=""
page=1
while :; do
  resp="$(ctyun ecs ListEcsInstances \
    --regionID "$REGION_ID" \
    --projectID "$PROJECT_ID" \
    --pageNo "$page" \
    --pageSize 100)"
  instance_rows="${instance_rows}$(jq -r '
    .returnObj.results[]?
    | select(.instanceName | startswith("worker-"))
    | [
        .instanceName,
        (.instanceStatus // ""),
        (.image.imageID // ""),
        (.fixedIPList[0] // .privateIP // .floatingIP // .publicIP // "")
      ]
    | @tsv
  ' <<<"$resp" 2>/dev/null | tr -d '\r' || true)"
  instance_rows+=$'\n'

  total_page="$(jq -r '.returnObj.totalPage // 1' <<<"$resp")"
  page=$((page + 1))
  if [[ $page -gt "$total_page" ]]; then
    break
  fi
done

# --- 2. bake 镜像 imageID → version 映射 ---
# list-worker-images.sh 输出 TSV：imageID<TAB>name<TAB>version<TAB>commit<TAB>createdTime<TAB>status
LIST_IMAGES_CMD="$(command -v list-worker-images.sh || true)"
[[ -n "$LIST_IMAGES_CMD" ]] || LIST_IMAGES_CMD="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/list-worker-images.sh"
RAW_IMAGES="$("$LIST_IMAGES_CMD" 2>/dev/null || true)"
TRIMMED_IMAGES="$(printf '%s' "$RAW_IMAGES" | tr -d '\r' || true)"
version_map="$(printf '%s' "$TRIMMED_IMAGES" | awk -F'\t' '{ print $1 "\t" $3 }' || true)"

# --- 3. 关联版本并输出 TSV ---
# bash 逐行处理（awk 对行尾连续空字段的行为在不同实现间不一致，
# 无 imageID 的实例需要保留尾部的空列）。
while IFS=$'\t' read -r name st img ip; do
  [[ -n "$name" ]] || continue
  ver=""
  if [[ -n "$img" ]]; then
    ver="$(awk -F'\t' -v id="$img" '$1 == id { print $2; exit }' <<<"$version_map")"
  fi
  app_ver="$(read_app_version "$name" "$st" "$ip")"
  printf '%s\t%s\t%s\t%s\t%s\n' "$name" "$st" "$img" "$ver" "$app_ver"
done <<<"$instance_rows"

exit 0
