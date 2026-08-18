#!/usr/bin/env bash
set -euo pipefail

root="${1:?stack root is required}"
revision="${2:?revision is required}"
owner="${3:?GHCR owner is required}"

shared_release_root="${CATSCO_SHARED_RELEASE_ROOT:-$root/releases}"
shared_source_root="${CATSCO_SHARED_SOURCE_ROOT:-$root/source}"
source_bundle="$shared_release_root/cats-company-source-${revision}.tar.gz"
source_root="$shared_source_root/$revision"

if [ ! -f "$source_bundle" ]; then
  echo "missing source bundle: $source_bundle" >&2
  exit 1
fi

mkdir -p "$shared_release_root" "$shared_source_root"
if [ ! -f "$source_root/.cats-company-revision" ] || [ "$(cat "$source_root/.cats-company-revision")" != "$revision" ]; then
  source_temp="$shared_source_root/.${revision}.extract.$$"
  rm -rf "$source_temp"
  mkdir -p "$source_temp"
  tar -xzf "$source_bundle" -C "$source_temp"
  printf '%s\n' "$revision" > "$source_temp/.cats-company-revision"
  rm -rf "$source_root"
  mv "$source_temp" "$source_root"
else
  echo "Source tree already present: ${source_root}"
fi
touch "$source_bundle" "$source_root"

cd "$source_root"
server_image="ghcr.io/${owner}/cats-company-server:${revision}"
if docker image inspect "$server_image" >/dev/null 2>&1; then
  echo "Server image already present: ${server_image}"
else
  server_build_timeout="${REMOTE_SERVER_BUILD_TIMEOUT_SECONDS:-900}"
  echo "Building server image: ${server_image} (timeout ${server_build_timeout}s)"
  timeout "$server_build_timeout" docker build --progress=plain \
    --build-arg GOPROXY="${REMOTE_GOPROXY:-https://goproxy.cn,direct}" \
    -f deploy/Dockerfile.server \
    -t "$server_image" \
    .
fi

dreamina_image="ghcr.io/${owner}/cats-company-dreamina-worker:${revision}"
if docker image inspect "$dreamina_image" >/dev/null 2>&1; then
  echo "Dreamina worker image already present: ${dreamina_image}"
else
  dreamina_build_timeout="${REMOTE_DREAMINA_BUILD_TIMEOUT_SECONDS:-600}"
  echo "Building Dreamina worker image: ${dreamina_image} (timeout ${dreamina_build_timeout}s)"
  timeout "$dreamina_build_timeout" docker build --progress=plain \
    -f deploy/Dockerfile.dreamina \
    -t "$dreamina_image" \
    .
fi

if [ -n "${GHCR_USERNAME:-}" ] && [ -n "${GHCR_TOKEN:-}" ]; then
  printf '%s\n' "$GHCR_TOKEN" | docker login ghcr.io -u "$GHCR_USERNAME" --password-stdin >/dev/null
fi

web_image="ghcr.io/${owner}/cats-company-web:${revision}"
pull_timeout="${REMOTE_WEB_PULL_TIMEOUT_SECONDS:-30}"

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

find "$shared_source_root" -mindepth 1 -maxdepth 1 -type d -mtime +7 -exec rm -rf {} +
find "$shared_release_root" -mindepth 1 -maxdepth 1 -name 'cats-company-source-*.tar.gz' -type f -mtime +7 -delete
find "$root/releases" -mindepth 1 -maxdepth 1 -name 'cats-company-images-*.tar.gz' -type f -delete
