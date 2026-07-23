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
server_build_timeout="${REMOTE_SERVER_BUILD_TIMEOUT_SECONDS:-900}"
echo "Building server image: ghcr.io/${owner}/cats-company-server:${revision} (timeout ${server_build_timeout}s)"
timeout "$server_build_timeout" docker build --progress=plain \
  --build-arg GOPROXY="${REMOTE_GOPROXY:-https://goproxy.cn,direct}" \
  -f deploy/Dockerfile.server \
  -t "ghcr.io/${owner}/cats-company-server:${revision}" \
  .

dreamina_build_timeout="${REMOTE_DREAMINA_BUILD_TIMEOUT_SECONDS:-600}"
echo "Building Dreamina worker image: ghcr.io/${owner}/cats-company-dreamina-worker:${revision} (timeout ${dreamina_build_timeout}s)"
timeout "$dreamina_build_timeout" docker build --progress=plain \
  -f deploy/Dockerfile.dreamina \
  -t "ghcr.io/${owner}/cats-company-dreamina-worker:${revision}" \
  .

if [ -n "${GHCR_USERNAME:-}" ] && [ -n "${GHCR_TOKEN:-}" ]; then
  printf '%s\n' "$GHCR_TOKEN" | docker login ghcr.io -u "$GHCR_USERNAME" --password-stdin >/dev/null
fi

web_image="ghcr.io/${owner}/cats-company-web:${revision}"
pull_timeout="${REMOTE_WEB_PULL_TIMEOUT_SECONDS:-300}"

if docker image inspect "$web_image" >/dev/null 2>&1; then
  echo "Web image already present: ${web_image}"
else
  echo "Pulling web image: ${web_image}"
  if ! timeout "$pull_timeout" docker pull "$web_image"; then
    fallback_build_timeout="${REMOTE_WEB_BUILD_TIMEOUT_SECONDS:-900}"
    echo "Web image pull failed or timed out after ${pull_timeout}s; building locally from source (timeout ${fallback_build_timeout}s)."
    timeout "$fallback_build_timeout" docker build --progress=plain \
      --build-arg REACT_APP_API_BASE="${REMOTE_WEB_REACT_APP_API_BASE:-}" \
      -f deploy/Dockerfile.nginx \
      -t "$web_image" \
      .
  fi
fi

find "$root/source" -mindepth 1 -maxdepth 1 -type d -mtime +7 -exec rm -rf {} +
find "$root/releases" -mindepth 1 -maxdepth 1 -name 'cats-company-source-*.tar.gz' -type f -mtime +7 -delete
find "$root/releases" -mindepth 1 -maxdepth 1 -name 'cats-company-images-*.tar.gz' -type f -delete
