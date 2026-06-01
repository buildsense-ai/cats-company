#!/usr/bin/env bash
set -euo pipefail

root="${1:?stack root is required}"
revision="${2:?revision is required}"
owner="${3:?GHCR owner is required}"

source_bundle="$root/releases/cats-company-source-${revision}.tar.gz"
source_root="$root/source/$revision"

if [ ! -f "$source_bundle" ]; then
  echo "missing source bundle: $source_bundle" >&2
  exit 1
fi

rm -rf "$source_root"
mkdir -p "$source_root"
tar -xzf "$source_bundle" -C "$source_root"

cd "$source_root"
docker build \
  --build-arg GOPROXY="${REMOTE_GOPROXY:-https://goproxy.cn,direct}" \
  -f deploy/Dockerfile.server \
  -t "ghcr.io/${owner}/cats-company-server:${revision}" \
  .

if [ -n "${GHCR_USERNAME:-}" ] && [ -n "${GHCR_TOKEN:-}" ]; then
  printf '%s\n' "$GHCR_TOKEN" | docker login ghcr.io -u "$GHCR_USERNAME" --password-stdin >/dev/null
fi

web_image="ghcr.io/${owner}/cats-company-web:${revision}"
pull_timeout="${REMOTE_WEB_PULL_TIMEOUT_SECONDS:-900}"
pull_heartbeat="${REMOTE_WEB_PULL_HEARTBEAT_SECONDS:-20}"

echo "Pulling web image: ${web_image}"
timeout "$pull_timeout" docker pull "$web_image" &
pull_pid=$!
elapsed=0
while kill -0 "$pull_pid" 2>/dev/null; do
  sleep "$pull_heartbeat"
  elapsed=$((elapsed + pull_heartbeat))
  if kill -0 "$pull_pid" 2>/dev/null; then
    echo "Still pulling web image after ${elapsed}s..."
  fi
done
wait "$pull_pid"

find "$root/source" -mindepth 1 -maxdepth 1 -type d -mtime +7 -exec rm -rf {} +
find "$root/releases" -mindepth 1 -maxdepth 1 -name 'cats-company-source-*.tar.gz' -type f -mtime +7 -delete
find "$root/releases" -mindepth 1 -maxdepth 1 -name 'cats-company-images-*.tar.gz' -type f -delete
