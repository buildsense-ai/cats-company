#!/usr/bin/env bash
# status-worker.sh — 批量输出 worker 实例的云侧状态（供控制面
# /api/cloud-workers 列表实时显示状态/版本/镜像）。
#
# 输出：每行 TSV  `instanceName<TAB>instanceStatus<TAB>imageID<TAB>version`
# instanceStatus 为天翼云 ListEcsInstances 的 instanceStatus
# （creating/running/stopped/error/...，不存在 = 无此行，控制面按 missing 处理）。
# version 由 bake 镜像列表（list-worker-images.sh TSV 第 1/3 列）按 imageID 关联。
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
for cmd in ctyun-cli jq timeout; do
  command -v "$cmd" >/dev/null 2>&1 || { echo "error: missing required command: $cmd" >&2; exit 2; }
done

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
    | [ .instanceName, (.instanceStatus // ""), (.image.imageID // ""), "" ]
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
while IFS=$'\t' read -r name st img; do
  [[ -n "$name" ]] || continue
  ver=""
  if [[ -n "$img" ]]; then
    ver="$(awk -F'\t' -v id="$img" '$1 == id { print $2; exit }' <<<"$version_map")"
  fi
  printf '%s\t%s\t%s\t%s\n' "$name" "$st" "$img" "$ver"
done <<<"$instance_rows"

exit 0
