#!/usr/bin/env bash
# list-worker-images.sh — 列出 bake 通道的 worker 私有镜像（供云控制面
# /api/cloud-workers/meta 展示 + 回滚选择）。
#
# 输出：每行 TSV  `imageID<TAB>name<TAB>version<TAB>commit<TAB>createdTime<TAB>status`
# 只列名称以 catsco-worker- 开头且带 bake label 的私有镜像（与 XiaoBa-CLI
# Manage-WorkerImages.ps1 同语义，bash 版跑在 Linux server）。
#
# 依赖：ctyun-cli + jq + GNU timeout（服务端镜像已装）
# 凭据：CTYUN_AK/CTYUN_SK（ctyun-cli 环境变量或 ~/.ctyun-cli.yaml），不进仓库
set -Eeuo pipefail

REGION_ID="${CTYUN_WORKER_REGION_ID:-}"
PROJECT_ID="${CTYUN_WORKER_PROJECT_ID:-0}"

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
  raw="$(timeout --signal=TERM --kill-after=15s 90s ctyun-cli "$@" --output json 2>&1)" || {
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

page=1
found=0
while :; do
  resp="$(ctyun ims ListImage \
    --regionID "$REGION_ID" \
    --projectID "$PROJECT_ID" \
    --imageVisibilityCode 0 \
    --pageNo "$page" \
    --pageSize 200)"

  # 过滤 bake 通道镜像并输出 TSV 行（createdTime 数字毫秒，供外部排序）
  jq -r '
    .returnObj.images[]?
    | select(.imageName | startswith("catsco-worker-"))
    | select(((.labels // []) | map(select(.labelKey == "bake"))) | length > 0)
    | [
        .imageID // "",
        .imageName // "",
        ((.labels // []) | map(select(.labelKey == "version"))[0].labelValue // ""),
        ((.labels // []) | map(select(.labelKey == "commit"))[0].labelValue // ""),
        (.createdTime // 0),
        .imageStatus // ""
      ]
    | @tsv
  ' <<<"$resp" || true

  total_page="$(jq -r '.returnObj.totalPage // 1' <<<"$resp")"
  page=$((page + 1))
  if [[ $page -gt "$total_page" ]]; then
    break
  fi
done

exit 0
