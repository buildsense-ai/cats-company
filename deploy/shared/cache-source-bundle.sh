#!/usr/bin/env bash
set -euo pipefail

action="${1:?usage: cache-source-bundle.sh <verify|prepare|pack|seal> <cache-root> <revision> [source-tree]}"
cache_root="${2:?cache root is required}"
revision="${3:?revision is required}"

if [[ ! "$revision" =~ ^[0-9a-f]{40,64}$ ]]; then
  echo "invalid source revision: $revision" >&2
  exit 1
fi

release_root="$cache_root/releases"
source_cache_root="$cache_root/source"
bundle_name="cats-company-source-${revision}.tar.gz"
bundle="$release_root/$bundle_name"
checksum="$bundle.sha256"
revision_file="$bundle.revision"

verify_bundle() {
  [ -s "$bundle" ] || return 1
  [ -s "$checksum" ] || return 1
  [ "$(cat "$revision_file" 2>/dev/null || true)" = "$revision" ] || return 1
  (cd "$release_root" && sha256sum --check --status "$(basename "$checksum")")
}

seal_bundle() {
  tar -tzf "$bundle" >/dev/null

  checksum_temp="${checksum}.tmp.$$"
  revision_temp="${revision_file}.tmp.$$"
  hash="$(sha256sum "$bundle" | awk '{print $1}')"
  printf '%s  %s\n' "$hash" "$bundle_name" > "$checksum_temp"
  printf '%s\n' "$revision" > "$revision_temp"
  mv "$checksum_temp" "$checksum"
  mv "$revision_temp" "$revision_file"
}

mkdir -p "$release_root" "$source_cache_root"

case "$action" in
  verify)
    verify_bundle
    ;;
  prepare)
    mirror="$cache_root/source-mirror"
    mkdir -p "$mirror"
    if [ -z "$(find "$mirror" -mindepth 1 -maxdepth 1 -print -quit)" ]; then
      latest_source="$(find "$source_cache_root" -mindepth 1 -maxdepth 1 -type d ! -name '.*' -printf '%T@ %p\n' \
        | sort -n | tail -n 1 | cut -d' ' -f2-)"
      if [ -n "$latest_source" ] && [ -d "$latest_source" ]; then
        cp -a "$latest_source/." "$mirror/"
        rm -f "$mirror/.cats-company-revision"
        echo "Prepared source mirror from: $latest_source"
      else
        echo "No previous source tree available; source mirror starts empty."
      fi
    else
      echo "Source mirror already prepared: $mirror"
    fi
    ;;
  pack)
    source_tree="${4:?source tree is required for pack}"
    if verify_bundle; then
      echo "Source bundle already cached: $bundle"
      exit 0
    fi
    [ -d "$source_tree" ] || {
      echo "source tree does not exist: $source_tree" >&2
      exit 1
    }

    bundle_temp="${bundle}.upload.$$"
    rm -f "$bundle_temp"
    tar -C "$source_tree" -czf "$bundle_temp" .
    mv "$bundle_temp" "$bundle"
    seal_bundle
    echo "Packed source bundle: $bundle"
    ;;
  seal)
    [ -s "$bundle" ] || {
      echo "source bundle does not exist: $bundle" >&2
      exit 1
    }
    seal_bundle
    echo "Sealed source bundle: $bundle"
    ;;
  *)
    echo "unsupported action: $action" >&2
    exit 1
    ;;
esac
