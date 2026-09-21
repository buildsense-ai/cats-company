#!/usr/bin/env bash
set -euo pipefail

root="${1:?usage: ensure-build-cache.sh <cache-root>}"

if [ ! -d "$root" ]; then
  if ! install -d -m 700 "$root" 2>/dev/null; then
    if ! command -v sudo >/dev/null 2>&1 || ! sudo -n true 2>/dev/null; then
      echo "Cannot create deploy cache $root; passwordless sudo is required." >&2
      exit 1
    fi
    sudo -n install -d -o "$(id -u)" -g "$(id -g)" -m 700 "$root"
  fi
elif [ ! -w "$root" ]; then
  if ! command -v sudo >/dev/null 2>&1 || ! sudo -n true 2>/dev/null; then
    echo "Deploy cache $root is not writable; passwordless sudo is required." >&2
    exit 1
  fi
  sudo -n chown "$(id -u):$(id -g)" "$root"
fi

chmod 700 "$root"
mkdir -p "$root/releases" "$root/source"

# The Web image is built from a pinned nginx-brotli base. When that base is
# missing locally, remote-build-source.sh falls back to building on the previous
# revision's Web image, which stacks a few layers on it on every deploy: the
# chain grows revision after revision and eventually reaches the overlay
# mount-options limit, where every Web build fails with "mount options is too
# long" (2026-09-20: 490 layers on the shared build host). Keep the base present
# so the fallback stays a fallback; re-seed it from a registry mirror when a
# cleanup removed it, instead of leaving the chain to grow again.
web_base_reference="georgjung/nginx-brotli:latest@sha256:488e48d7773deef7f696a25362da3043e3aabb447c6f28548eda7391a27c7fc9"

ensure_web_base_image() {
  command -v docker >/dev/null 2>&1 || return 0
  docker image inspect "$web_base_reference" >/dev/null 2>&1 && return 0

  # The tagable name half of the reference: the tag step and the warning below
  # must never drift from the reference the verification (and the build) uses.
  local tag_reference="${web_base_reference%%@*}"

  # Sources are tried in order: the registry reference first, then one
  # <mirror>/<reference> per mirror. REMOTE_WEB_BASE_MIRRORS overrides the list;
  # an empty value leaves the registry as the only source.
  local candidates="$web_base_reference"
  local mirror
  for mirror in ${REMOTE_WEB_BASE_MIRRORS-docker.1panel.live docker.1ms.run}; do
    mirror="${mirror%/}"
    [ -n "$mirror" ] || continue
    candidates="$candidates $mirror/$web_base_reference"
  done

  # Kept well under the deploying step's 8-minute budget even if every source
  # hangs: seeding must never be what times out a deploy.
  local pull_timeout="${REMOTE_WEB_BASE_PULL_TIMEOUT_SECONDS:-120}"
  local candidate image_id
  for candidate in $candidates; do
    echo "Seeding Web build base from ${candidate}..." >&2
    timeout "$pull_timeout" docker pull "$candidate" >/dev/null 2>&1 || continue
    image_id="$(docker image inspect --format '{{.Id}}' "$candidate" 2>/dev/null || true)"
    [ -n "$image_id" ] || continue
    docker tag "$image_id" "$tag_reference" >/dev/null 2>&1 || continue
    if docker image inspect "$web_base_reference" >/dev/null 2>&1; then
      echo "Web build base seeded from ${candidate}." >&2
      return 0
    fi
  done

  echo "WARNING: the pinned Web build base ${web_base_reference} is missing and could not be seeded." >&2
  echo "The next Web build falls back to the previous revision's image and grows the layer chain." >&2
  echo "Seed it manually or point REMOTE_WEB_BASE_IMAGE at a local base to stop that." >&2
  echo "Manual fix: docker pull <registry>/${web_base_reference}, then docker tag <image-id> ${tag_reference}." >&2
  return 0
}

ensure_web_base_image
