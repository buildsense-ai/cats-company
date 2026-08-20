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
