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
  server_build_timeout="${REMOTE_SERVER_BUILD_TIMEOUT_SECONDS:-1800}"
  echo "Building server image: ${server_image} (timeout ${server_build_timeout}s)"
  timeout "$server_build_timeout" docker build --progress=plain \
    --build-arg GOPROXY="${REMOTE_GOPROXY:-https://goproxy.cn,direct}" \
    --build-arg APK_REPOSITORY="${REMOTE_ALPINE_REPOSITORY:-https://mirrors.aliyun.com/alpine}" \
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

web_image="ghcr.io/${owner}/cats-company-web:${revision}"
pull_timeout="${REMOTE_WEB_PULL_TIMEOUT_SECONDS:-120}"
login_timeout="${REMOTE_GHCR_LOGIN_TIMEOUT_SECONDS:-20}"
web_image_mode="${REMOTE_WEB_IMAGE_MODE:-pull}"

build_web_image() {
  fallback_build_timeout="${REMOTE_WEB_BUILD_TIMEOUT_SECONDS:-900}"
  echo "Building web image locally (timeout ${fallback_build_timeout}s)."
  timeout "$fallback_build_timeout" docker build --progress=plain \
    --build-arg REACT_APP_API_BASE="${REMOTE_WEB_REACT_APP_API_BASE:-}" \
    -f deploy/Dockerfile.nginx \
    -t "$web_image" \
    .
}

if docker image inspect "$web_image" >/dev/null 2>&1; then
  echo "Web image already present: ${web_image}"
else
  case "$web_image_mode" in
    local)
      build_web_image
      ;;
    pull)
      echo "Pulling web image: ${web_image}"
      pull_ready=1
      if [ -n "${GHCR_USERNAME:-}" ] && [ -n "${GHCR_TOKEN:-}" ]; then
        if ! printf '%s\n' "$GHCR_TOKEN" | timeout "$login_timeout" docker login ghcr.io -u "$GHCR_USERNAME" --password-stdin >/dev/null; then
          echo "GHCR login failed or timed out after ${login_timeout}s."
          pull_ready=0
        fi
      fi
      if [ "$pull_ready" -ne 1 ] || ! timeout "$pull_timeout" docker pull "$web_image"; then
        echo "Web image pull failed or timed out after ${pull_timeout}s; falling back to the local build cache."
        build_web_image
      fi
      ;;
    *)
      echo "unsupported REMOTE_WEB_IMAGE_MODE: $web_image_mode" >&2
      exit 1
      ;;
  esac
fi

website_image="ghcr.io/${owner}/cats-company-website:${revision}"
website_pull_timeout="${REMOTE_WEBSITE_PULL_TIMEOUT_SECONDS:-120}"
website_image_mode="${REMOTE_WEBSITE_IMAGE_MODE:-local}"

build_website_image() {
  website_build_timeout="${REMOTE_WEBSITE_BUILD_TIMEOUT_SECONDS:-900}"
  echo "Building website image locally (timeout ${website_build_timeout}s)."
  timeout "$website_build_timeout" docker build --progress=plain \
    --build-arg VITE_APP_BASE_URL="${REMOTE_WEBSITE_APP_BASE_URL:-https://app.catsco.cc}" \
    -f deploy/Dockerfile.website \
    -t "$website_image" \
    .
}

if docker image inspect "$website_image" >/dev/null 2>&1; then
  echo "Website image already present: ${website_image}"
else
  case "$website_image_mode" in
    pull)
      echo "Pulling website image: ${website_image}"
      if ! timeout "$website_pull_timeout" docker pull "$website_image"; then
        echo "Website image pull failed or timed out after ${website_pull_timeout}s; falling back to the local build cache."
        build_website_image
      fi
      ;;
    local)
      build_website_image
      ;;
    *)
      echo "unsupported REMOTE_WEBSITE_IMAGE_MODE: $website_image_mode" >&2
      exit 1
      ;;
  esac
fi

find "$shared_source_root" -mindepth 1 -maxdepth 1 -type d -mtime +7 -exec rm -rf {} +
find "$shared_release_root" -mindepth 1 -maxdepth 1 -name 'cats-company-source-*' -type f -mtime +7 -delete
find "$root/releases" -mindepth 1 -maxdepth 1 -name 'cats-company-images-*.tar.gz' -type f -delete
