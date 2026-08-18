#!/usr/bin/env bash
# deploy-worker-version.sh - install one published CatsCo application release
# on an existing cloud worker while preserving /srv/catsco-agent.
set -Eeuo pipefail

NAME=""
VERSION=""
DRY_RUN=0

while (($#)); do
  case "$1" in
    --name) NAME="${2:-}"; shift 2 ;;
    --version) VERSION="${2:-}"; shift 2 ;;
    --dry-run) DRY_RUN=1; shift ;;
    -h|--help)
      echo "usage: deploy-worker-version.sh --name <tenant> --version <version> [--dry-run]"
      exit 0
      ;;
    *) echo "error: unknown argument: $1" >&2; exit 2 ;;
  esac
done

[[ "$NAME" =~ ^[a-z0-9][a-z0-9_-]{1,63}$ ]] || { echo "error: invalid --name" >&2; exit 2; }
if [[ -n "$VERSION" && ! "$VERSION" =~ ^v?[0-9A-Za-z][0-9A-Za-z._+-]{0,63}$ ]]; then
  echo "error: invalid --version" >&2
  exit 2
fi
VERSION="${VERSION#v}"

for cmd in ctyun-cli jq tos-fetch sha256sum tar ssh scp timeout awk cut sort; do
  command -v "$cmd" >/dev/null 2>&1 || { echo "error: missing required command: $cmd" >&2; exit 2; }
done

REGION_ID="${CTYUN_WORKER_REGION_ID:-}"
PROJECT_ID="${CTYUN_WORKER_PROJECT_ID:-0}"
STATE_ROOT="${CTYUN_WORKER_STATE_ROOT:-}"
if [[ -n "$STATE_ROOT" ]]; then
  STATE_DIR="$STATE_ROOT/$NAME"
else
  STATE_DIR="${CTYUN_WORKER_STATE_DIR:-/var/lib/catsco-worker/$NAME}"
  STATE_ROOT="$(dirname "$STATE_DIR")"
fi
ARTIFACT_BUCKET="${CATSCO_WORKER_ARTIFACT_BUCKET:-catsco-worker-release}"
ARTIFACT_PREFIX="${CATSCO_WORKER_ARTIFACT_PREFIX:-update/worker}"
ARTIFACT_PREFIX="${ARTIFACT_PREFIX#/}"
ARTIFACT_PREFIX="${ARTIFACT_PREFIX%/}"
ARTIFACT_REGION="${CATSCO_WORKER_ARTIFACT_REGION:-cn-guangzhou}"
ARTIFACT_ENDPOINT="${CATSCO_WORKER_ARTIFACT_ENDPOINT:-https://tos-cn-guangzhou.volces.com}"
ARTIFACT_CACHE_ROOT="${CATSCO_WORKER_ARTIFACT_CACHE_DIR:-$STATE_ROOT/.artifacts}"
OPS_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
LIST_IMAGES_CMD="${CATSCO_WORKER_IMAGES_SCRIPT:-$OPS_DIR/list-worker-images.sh}"

JUMP_IP="${CTYUN_JUMP_IP:-}"
JUMP_PORT="${CTYUN_JUMP_PORT:-22}"
JUMP_USER="${CTYUN_JUMP_USER:-root}"
JUMP_KEY="${CTYUN_JUMP_KEY:-/var/lib/catsco-worker/jump_host_ed25519}"

[[ -n "$REGION_ID" ]] || { echo "error: CTYUN_WORKER_REGION_ID is required" >&2; exit 2; }
[[ -x "$LIST_IMAGES_CMD" ]] || { echo "error: image list script is unavailable" >&2; exit 2; }

ctyun() {
  local raw status
  raw="$(timeout -s TERM -k 15 120s ctyun-cli "$@" --output json 2>&1)" || {
    echo "error: ctyun-cli failed: $*" >&2
    return 1
  }
  status="$(jq -r '.statusCode // empty' <<<"$raw")"
  [[ "$status" == "800" ]] || {
    echo "error: Tianyi Cloud API failed" >&2
    return 1
  }
  printf '%s' "$raw"
}

find_instance() {
  local resp
  resp="$(ctyun ecs ListEcsInstances --regionID "$REGION_ID" --projectID "$PROJECT_ID" \
    --instanceName "worker-$NAME" --pageNo 1 --pageSize 10)"
  jq -r --arg n "worker-$NAME" '.returnObj.results[]? | select(.instanceName == $n)' <<<"$resp" || true
}

image_rows="$($LIST_IMAGES_CMD)"
if [[ -z "$VERSION" ]]; then
  image_row="$(printf '%s\n' "$image_rows" | sort -t $'\t' -k5,5nr | head -n1)"
  VERSION="$(cut -f3 <<<"$image_row")"
  VERSION="${VERSION#v}"
else
  image_row="$(awk -F '\t' -v wanted="$VERSION" '
    {
      candidate=$3
      sub(/^v/, "", candidate)
      if (candidate == wanted) { print; exit }
    }
  ' <<<"$image_rows")"
fi
[[ -n "$image_row" ]] || { echo "error: no worker image found for version $VERSION" >&2; exit 1; }
[[ "$VERSION" =~ ^[0-9A-Za-z][0-9A-Za-z._+-]{0,63}$ ]] || { echo "error: selected image version is invalid" >&2; exit 1; }
COMMIT="$(cut -f4 <<<"$image_row")"
[[ "$COMMIT" =~ ^[0-9a-fA-F]{40}$ ]] || { echo "error: image commit is invalid for version $VERSION" >&2; exit 1; }

inst="$(find_instance)"
[[ -n "$inst" ]] || { echo "error: instance worker-$NAME not found" >&2; exit 1; }
INSTANCE_IP="$(jq -r '(.fixedIPList[0] // .privateIP // .floatingIP // .publicIP // "")' <<<"$inst")"
[[ -n "$INSTANCE_IP" ]] || { echo "error: instance has no IP" >&2; exit 1; }

if [[ $DRY_RUN -eq 1 ]]; then
  jq -nc --arg name "worker-$NAME" --arg version "$VERSION" --arg commit "$COMMIT" \
    '{status:"dry-run",instance_name:$name,version:$version,commit:$commit}'
  exit 0
fi

mkdir -p "$STATE_DIR"
PRIVATE_KEY="$STATE_DIR/id_rsa"
if [[ ! -f "$PRIVATE_KEY" && -f "$STATE_ROOT/id_rsa" ]]; then
  cp "$STATE_ROOT/id_rsa" "$PRIVATE_KEY"
  chmod 600 "$PRIVATE_KEY"
fi
[[ -f "$PRIVATE_KEY" ]] || { echo "error: worker private key is unavailable" >&2; exit 1; }

ssh_opts=(-i "$PRIVATE_KEY" -o BatchMode=yes -o ConnectTimeout=10 \
  -o ServerAliveInterval=15 -o ServerAliveCountMax=3 \
  -o StrictHostKeyChecking=accept-new -o UserKnownHostsFile="$STATE_DIR/known_hosts")
if [[ -n "$JUMP_IP" ]]; then
  ssh_opts+=(-o "ProxyCommand=ssh -i ${JUMP_KEY} -p ${JUMP_PORT} -o BatchMode=yes -o ConnectTimeout=10 -o StrictHostKeyChecking=accept-new -o UserKnownHostsFile=${STATE_DIR}/jump_known_hosts -W %h:%p ${JUMP_USER}@${JUMP_IP}")
fi

# Lazy reuse: image builds and previous installs leave immutable releases on
# the worker. Re-selecting one switches the symlink locally without downloading
# or unpacking the artifact again.
RELEASE_ID="${VERSION}-${COMMIT:0:8}"
RELEASE_ROOT="/opt/catsco/releases/$RELEASE_ID"
if timeout -s TERM -k 10 30s ssh "${ssh_opts[@]}" "root@$INSTANCE_IP" \
  "current=\$(readlink -f /opt/catsco/current 2>/dev/null || true); test \"\$current\" = '$RELEASE_ROOT' && systemctl is-active --quiet catsco-agent.service"; then
  jq -nc --arg name "worker-$NAME" --arg version "$VERSION" --arg commit "$COMMIT" \
    '{status:"already-current",instance_name:$name,version:$version,commit:$commit}'
  exit 0
fi
if timeout -s TERM -k 10 30s ssh "${ssh_opts[@]}" "root@$INSTANCE_IP" \
  "test -f '$RELEASE_ROOT/worker-release.json' && jq -e '.version == \"$VERSION\" and .commit == \"$COMMIT\"' '$RELEASE_ROOT/worker-release.json' >/dev/null"; then
  timeout -s TERM -k 20 90s ssh "${ssh_opts[@]}" "root@$INSTANCE_IP" \
    "old=\$(readlink -f /opt/catsco/current 2>/dev/null || true); mkdir -p /var/lib/catsco; if test \"\$old\" != '$RELEASE_ROOT'; then printf '%s\\n' \"\$old\" > /var/lib/catsco/previous-release; fi; ln -sfn '$RELEASE_ROOT' /opt/catsco/current; systemctl restart catsco-agent.service; sleep 5; systemctl is-active catsco-agent.service" >/dev/null
  jq -nc --arg name "worker-$NAME" --arg version "$VERSION" --arg commit "$COMMIT" \
    '{status:"reused-local-release",instance_name:$name,version:$version,commit:$commit}'
  exit 0
fi

TEMP_DIR="$(mktemp -d "${TMPDIR:-/tmp}/catsco-worker-version.XXXXXX")"
REMOTE_PREFIX="/tmp/catsco-version-${NAME}-$$"
cleanup() {
  rm -rf "$TEMP_DIR"
  if [[ -n "${INSTANCE_IP:-}" && -f "${PRIVATE_KEY:-/nonexistent}" ]]; then
    [[ ${#ssh_opts[@]} -gt 0 ]] && timeout -s TERM -k 5 20s ssh "${ssh_opts[@]}" "root@$INSTANCE_IP" \
      "rm -f '${REMOTE_PREFIX}.tar.gz' '${REMOTE_PREFIX}.sh'" >/dev/null 2>&1 || true
  fi
}
trap cleanup EXIT

MANIFEST="$TEMP_DIR/manifest.json"
UPDATER="$TEMP_DIR/update-worker-artifact.sh"
download_private_object() {
  local key="$1" destination="$2" timeout_seconds="$3"
  timeout -s TERM -k 15 "${timeout_seconds}s" tos-fetch \
    --endpoint "$ARTIFACT_ENDPOINT" \
    --region "$ARTIFACT_REGION" \
    --bucket "$ARTIFACT_BUCKET" \
    --key "$key" \
    --output "$destination"
}

MANIFEST_KEY="$ARTIFACT_PREFIX/$VERSION/manifest.json"
download_private_object "$MANIFEST_KEY" "$MANIFEST" 180

MANIFEST_VERSION="$(jq -r '.version // ""' "$MANIFEST")"
MANIFEST_COMMIT="$(jq -r '.commit // ""' "$MANIFEST")"
EXPECTED_SHA="$(jq -r '.sha256 // ""' "$MANIFEST")"
ARTIFACT_FILE="$(jq -r '.artifactFile // ""' "$MANIFEST")"
[[ "$MANIFEST_VERSION" == "$VERSION" && "$MANIFEST_COMMIT" == "$COMMIT" ]] || {
  echo "error: published artifact identity does not match image metadata" >&2
  exit 1
}
[[ "$EXPECTED_SHA" =~ ^[0-9a-fA-F]{64}$ ]] || { echo "error: published artifact sha256 is invalid" >&2; exit 1; }
[[ "$ARTIFACT_FILE" =~ ^[0-9A-Za-z._+-]+\.tar\.gz$ ]] || { echo "error: published artifact file is invalid" >&2; exit 1; }

ARTIFACT_CACHE_DIR="$ARTIFACT_CACHE_ROOT/$VERSION"
ARTIFACT="$ARTIFACT_CACHE_DIR/$ARTIFACT_FILE"
mkdir -p "$ARTIFACT_CACHE_DIR"
ACTUAL_SHA=""
[[ -f "$ARTIFACT" ]] && ACTUAL_SHA="$(sha256sum "$ARTIFACT" | awk '{print $1}')"
if [[ "${ACTUAL_SHA,,}" != "${EXPECTED_SHA,,}" ]]; then
  DOWNLOAD="$TEMP_DIR/$ARTIFACT_FILE.download"
  download_private_object "$ARTIFACT_PREFIX/$VERSION/$ARTIFACT_FILE" "$DOWNLOAD" 900
  ACTUAL_SHA="$(sha256sum "$DOWNLOAD" | awk '{print $1}')"
  [[ "${ACTUAL_SHA,,}" == "${EXPECTED_SHA,,}" ]] || { echo "error: artifact checksum mismatch" >&2; exit 1; }
  mv -f "$DOWNLOAD" "$ARTIFACT"
fi
tar -xOf "$ARTIFACT" app/scripts/update-worker-artifact.sh > "$UPDATER"
chmod 700 "$UPDATER"

timeout -s TERM -k 15 300s scp "${ssh_opts[@]}" "$ARTIFACT" "root@$INSTANCE_IP:${REMOTE_PREFIX}.tar.gz"
timeout -s TERM -k 15 60s scp "${ssh_opts[@]}" "$UPDATER" "root@$INSTANCE_IP:${REMOTE_PREFIX}.sh"
timeout -s TERM -k 30 300s ssh "${ssh_opts[@]}" "root@$INSTANCE_IP" \
  "bash '${REMOTE_PREFIX}.sh' --artifact '${REMOTE_PREFIX}.tar.gz' --sha256 '$EXPECTED_SHA' --version '$VERSION' --commit '$COMMIT'"

jq -nc --arg name "worker-$NAME" --arg version "$VERSION" --arg commit "$COMMIT" \
  '{status:"updated",instance_name:$name,version:$version,commit:$commit}'
