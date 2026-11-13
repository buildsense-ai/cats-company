#!/usr/bin/env bash
# Run on Linux with Bash and the actual host utilities, including BusyBox in CI.
set -Eeuo pipefail
source "$(cd "$(dirname "${BASH_SOURCE[0]}")/../prod/ops" && pwd)/worker-artifact-cache.sh"
root=$(mktemp -d)
trap 'rm -rf "$root"' EXIT
mkdir -p "$root/cache" "$root/outside" "$root/cache/bot-a"
printf 'preserve tenant key\n' > "$root/cache/bot-a/id_rsa"
for version in 1.6.1 1.6.2 1.6.3 1.6.4 1.6.5 1.6.6; do
  mkdir "$root/cache/$version"
  touch "$root/cache/$version/catsco-worker-$version.tar.gz"
done
ln -s "$root/outside" "$root/cache/1.0.0"
mkdir "$root/cache/invalid name"
exec 7>"$root/cache/.prune.lock"
flock -s 7
worker_prune_artifact_cache "$root/cache" 1.6.6 1
test -d "$root/cache/1.6.2"
flock -u 7
touch "$root/cache/1.6.2/.inuse-test"
worker_prune_artifact_cache "$root/cache" 1.6.1 3
for version in 1.6.1 1.6.2 1.6.4 1.6.5 1.6.6; do test -d "$root/cache/$version"; done
test ! -e "$root/cache/1.6.3"
rm "$root/cache/1.6.2/.inuse-test"
worker_prune_artifact_cache "$root/cache" 1.6.1 3
test ! -e "$root/cache/1.6.2"
test -L "$root/cache/1.0.0"
test -d "$root/outside"
test -d "$root/cache/invalid name"
test -f "$root/cache/bot-a/id_rsa"
mkdir "$root/cache/1.6.7"
touch "$root/cache/1.6.7/catsco-worker-1.6.7.tar.gz"
worker_prune_artifact_cache "$root/cache" 1.6.7 0
test -d "$root/cache/1.6.4"
rm() { return 1; }
if worker_prune_artifact_cache "$root/cache" 1.6.7 1; then
  echo 'error: prune failure was hidden' >&2
  exit 1
fi
unset -f rm
printf 'worker artifact cache: retention, locks, pins, tenant state, symlinks, disabled cleanup, and failure visibility passed\n'
