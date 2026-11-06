#!/usr/bin/env bash
# status-worker.sh — 批量输出 worker 实例的云侧状态（供控制面
# /api/cloud-workers 列表实时显示状态/版本/镜像）。
#
# 输出：每行 TSV
# `instanceName<TAB>instanceStatus<TAB>imageID<TAB>imageVersion<TAB>appVersion<TAB>privateIP<TAB>publicIP`
# instanceStatus 为天翼云 ListEcsInstances 的 instanceStatus
# （creating/running/stopped/error/...，不存在 = 无此行，控制面按 missing 处理）。
# imageVersion 由 bake 镜像列表按 imageID 关联；appVersion 来自控制面在
# tenant 状态目录持久化的已验证版本。旧实例首次读取时会通过 SSH 查询
# /opt/catsco/current/worker-release.json 并回填，之后不再重复远程探测。
#
# 依赖：ctyun-cli + jq + timeout
# 凭据：CTYUN_AK/CTYUN_SK（ctyun-cli 环境变量或 ~/.ctyun-cli.yaml）
# 云环境：CTYUN_WORKER_REGION_ID / CTYUN_WORKER_PROJECT_ID（实例所在项目）/
#         CTYUN_IMAGE_PROJECT_ID（bake 镜像所在项目，必须显式配置）
#
# 多地域：一个部署可能横跨两个地域（早期实例建在 CTYUN_WORKER_REGION_ID，后来的
# 迁移实例把目标地域写进了自己的 deployment.json）。只查环境变量那一个地域，会把
# 另一批在线实例全部报告为缺席，而控制面把"没有这一行"当作 missing。
# 所以查询集合 = 环境变量地域 ∪ 每个租户 snapshot 里记录的地域。
set -Eeuo pipefail

REGION_ID="${CTYUN_WORKER_REGION_ID:-}"
PROJECT_ID="${CTYUN_WORKER_PROJECT_ID:-0}"
IMAGE_PROJECT_ID="${CTYUN_IMAGE_PROJECT_ID:-}"

if [[ -z "$REGION_ID" ]]; then
  echo "error: CTYUN_WORKER_REGION_ID is required" >&2
  exit 2
fi
if [[ -z "$IMAGE_PROJECT_ID" ]]; then
  echo "error: CTYUN_IMAGE_PROJECT_ID is required (bake image project)" >&2
  exit 2
fi
for cmd in ctyun-cli jq timeout ssh; do
  command -v "$cmd" >/dev/null 2>&1 || { echo "error: missing required command: $cmd" >&2; exit 2; }
done

# The control-plane snapshot request has a two-minute context. Keep a strict
# script-wide budget below that so provider pagination, image listing, and SSH
# enrichment cannot accumulate into a request timeout.
STATUS_BUDGET_SECONDS="${CATSCO_WORKER_STATUS_BUDGET_SECONDS:-110}"
[[ "$STATUS_BUDGET_SECONDS" =~ ^[0-9]+$ && "$STATUS_BUDGET_SECONDS" -ge 1 ]] || {
  echo "error: CATSCO_WORKER_STATUS_BUDGET_SECONDS must be a positive integer" >&2
  exit 2
}
STATUS_DEADLINE=$(( $(date +%s) + STATUS_BUDGET_SECONDS ))

remaining_budget() {
  local left
  left=$((STATUS_DEADLINE - $(date +%s)))
  (( left > 0 )) || return 1
  printf '%s' "$left"
}

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

  local ssh_timeout remaining
  remaining="$(remaining_budget || true)"
  [[ -n "$remaining" ]] || return 0
  ssh_timeout=$(( remaining < 8 ? remaining : 8 ))
  version="$(timeout -s TERM -k 2 "${ssh_timeout}s" ssh "${ssh_opts[@]}" "root@$ip" \
    "cat /opt/catsco/current/worker-release.json 2>/dev/null" 2>/dev/null \
    | jq -r '.version // empty' 2>/dev/null || true)"
  if [[ "$version" =~ ^[0-9A-Za-z][0-9A-Za-z._+-]{0,63}$ ]]; then
    mkdir -p "$state_dir"
    printf '%s\n' "$version" > "$version_file.tmp"
    mv -f "$version_file.tmp" "$version_file"
    printf '%s' "$version"
  fi
}

# 调 ctyun-cli 并校验 statusCode（fail-closed）。状态刷新是后台 best-effort
# 操作，单次 provider 调用快速失败，避免分页或厂商卡顿拖垮控制面快照。
ctyun() {
  local raw status remaining call_timeout
  remaining="$(remaining_budget || true)"
  [[ -n "$remaining" ]] || { echo "error: status snapshot budget exhausted" >&2; return 1; }
  call_timeout=$(( remaining < 45 ? remaining : 45 ))
  raw="$(timeout -s TERM -k 10 "${call_timeout}s" ctyun-cli "$@" --output json 2>&1)" || {
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

# --- 0. 需要查询的 (region, project) 集合 ---
# 默认地域来自环境变量；每个租户的 deployment.json 记录着它**创建时**的地域
# （迁移过的实例只在这里留痕）。两者取并集，两批机器都能查到。
#
# 只信任形状合法的值：region/project 都必须是 [A-Za-z0-9_-]（天翼云的 projectID
# 是十六进制串，不能按数字校验，否则会被 "修正" 成 0 从而查错项目）。
# 软链接一律跳过：deployment.json 是安全边界上的输入，不让它指向任意文件。
declare -a REGION_PAIRS=()
_seen_region_pairs=" "
add_region_pair() {
  local r="${1:-}" p="${2:-0}"
  [[ -n "$r" ]] || return 0
  [[ "$r" =~ ^[A-Za-z0-9_-]+$ ]] || return 0
  [[ "$p" =~ ^[A-Za-z0-9_-]+$ ]] || p=0
  case "$_seen_region_pairs" in *" $r:$p "*) return 0 ;; esac
  _seen_region_pairs="$_seen_region_pairs$r:$p "
  REGION_PAIRS+=("$r:$p")
}

add_region_pair "$REGION_ID" "$PROJECT_ID"
while IFS=$'\t' read -r _snap_region _snap_project; do
  add_region_pair "$_snap_region" "$_snap_project"
done < <(
  for _f in "$STATE_ROOT"/*/deployment.json; do
    [[ -f "$_f" && ! -L "$_f" ]] || continue
    jq -r '[.env.CTYUN_WORKER_REGION_ID // "", .env.CTYUN_WORKER_PROJECT_ID // "0"] | @tsv' \
      "$_f" 2>/dev/null || true
  done | sort -u
)

if (( ${#REGION_PAIRS[@]} == 0 )); then
  echo "error: CTYUN_WORKER_REGION_ID is required" >&2
  exit 2
fi

# --- 1. 拉取全部 worker 实例（instanceName 以 worker- 开头） ---
# ListEcsInstances 支持分页；按名称过滤后仍可能跨页，翻页到 totalPage。
#
# 查询的地域来自 step 0：环境变量 + 每个租户 deployment.json。任一地域查询失败
# 都整体失败（fail-closed）——控制面把缺席当成 missing，容忍部分失败会让一次
# 地域抖动把在线机器标成不存在，进而触发错误的容量判断。
instance_rows=""
for _pair in "${REGION_PAIRS[@]}"; do
  query_region="${_pair%%:*}"
  query_project="${_pair##*:}"
  page=1
  max_pages=20
  while :; do
    resp="$(ctyun ecs ListEcsInstances \
      --regionID "$query_region" \
      --projectID "$query_project" \
      --pageNo "$page" \
      --pageSize 100)"
    instance_rows="${instance_rows}$(jq -r '
      .returnObj.results[]?
      | select(.instanceName | startswith("worker-"))
      | [
          .instanceName,
          (.instanceStatus // ""),
          (.image.imageID // ""),
          (.fixedIPList[0] // .privateIP // ""),
          (.floatingIP // .publicIP // "")
        ]
      | @tsv
    ' <<<"$resp" 2>/dev/null | tr -d '\r' || true)"
    instance_rows+=$'\n'

    total_page="$(jq -r '.returnObj.totalPage // 1' <<<"$resp")"
    page=$((page + 1))
    if [[ $page -gt $((max_pages + 1)) ]]; then
      echo "error: instance pagination exceeded $max_pages pages (region $query_region)" >&2
      exit 1
    fi
    if [[ $page -gt "$total_page" ]]; then
      break
    fi
  done
done

# --- 2. bake 镜像 imageID → version 映射 ---
# list-worker-images.sh 输出 TSV：imageID<TAB>name<TAB>version<TAB>commit<TAB>createdTime<TAB>status
# 每个地域各查一次：实例可能建在镜像所在地域之外，imageID 只有在同一地域的
# 镜像列表里才查得到，否则版本列会整片变空。
LIST_IMAGES_CMD="$(command -v list-worker-images.sh || true)"
[[ -n "$LIST_IMAGES_CMD" ]] || LIST_IMAGES_CMD="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/list-worker-images.sh"
version_map=""
for _pair in "${REGION_PAIRS[@]}"; do
  remaining="$(remaining_budget || true)"
  [[ -n "$remaining" ]] || break
  RAW_IMAGES="$(CTYUN_WORKER_REGION_ID="${_pair%%:*}" CTYUN_WORKER_PROJECT_ID="${_pair##*:}" \
    timeout -s TERM -k 5 "${remaining}s" "$LIST_IMAGES_CMD" 2>/dev/null || true)"
  TRIMMED_IMAGES="$(printf '%s' "$RAW_IMAGES" | tr -d '\r' || true)"
  version_map="${version_map}$(printf '%s' "$TRIMMED_IMAGES" | awk -F'\t' 'NF { print $1 "\t" $3 }' || true)"$'\n'
done

# --- 3. 关联版本并输出 TSV ---
# bash 逐行处理（awk 对行尾连续空字段的行为在不同实现间不一致，
# 无 imageID 的实例需要保留尾部的空列）。
while IFS=$'\t' read -r name st img private_ip public_ip; do
  [[ -n "$name" ]] || continue
  ver=""
  if [[ -n "$img" ]]; then
    ver="$(awk -F'\t' -v id="$img" '$1 == id { print $2; exit }' <<<"$version_map")"
  fi
  connect_ip="$private_ip"
  [[ "${CTYUN_WORKER_EXT_IP:-0}" != 1 ]] || connect_ip="$public_ip"
  app_ver="$(read_app_version "$name" "$st" "$connect_ip")"
  printf '%s\t%s\t%s\t%s\t%s\t%s\t%s\n' "$name" "$st" "$img" "$ver" "$app_ver" "$private_ip" "$public_ip"
done <<<"$instance_rows"

exit 0
