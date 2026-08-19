#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
tmpdir="$(mktemp -d)"
trap 'rm -rf "$tmpdir"' EXIT

cache_root="$tmpdir/cache"
bash "$repo_root/deploy/shared/ensure-build-cache.sh" "$cache_root"

test -d "$cache_root/releases"
test -d "$cache_root/source"
test "$(stat -c '%a' "$cache_root")" = "700"

chmod 755 "$cache_root"
bash "$repo_root/deploy/shared/ensure-build-cache.sh" "$cache_root"
test "$(stat -c '%a' "$cache_root")" = "700"

echo "ensure-build-cache tests passed"
