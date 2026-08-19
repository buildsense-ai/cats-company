#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
script="$repo_root/deploy/shared/cache-source-bundle.sh"
temp_root="$(mktemp -d)"
trap 'rm -rf "$temp_root"' EXIT

cache_root="$temp_root/cache"
source_tree="$temp_root/source"
first_revision="0123456789abcdef0123456789abcdef01234567"
second_revision="89abcdef0123456789abcdef0123456789abcdef"

mkdir -p "$source_tree"
printf 'first\n' > "$source_tree/value.txt"
mkdir -p "$cache_root/source/previous"
printf 'previous\n' > "$cache_root/source/previous/unchanged.txt"
printf 'old-revision\n' > "$cache_root/source/previous/.cats-company-revision"

bash "$script" prepare "$cache_root" "$first_revision"
[ -f "$cache_root/source-mirror/unchanged.txt" ]
[ ! -f "$cache_root/source-mirror/.cats-company-revision" ]

bash "$script" pack "$cache_root" "$first_revision" "$source_tree"
bash "$script" verify "$cache_root" "$first_revision"

first_bundle="$cache_root/releases/cats-company-source-${first_revision}.tar.gz"
first_hash="$(sha256sum "$first_bundle" | awk '{print $1}')"
printf 'changed\n' > "$source_tree/value.txt"
bash "$script" pack "$cache_root" "$first_revision" "$source_tree"
[ "$(sha256sum "$first_bundle" | awk '{print $1}')" = "$first_hash" ]

bash "$script" pack "$cache_root" "$second_revision" "$source_tree"
bash "$script" verify "$cache_root" "$second_revision"

second_bundle="$cache_root/releases/cats-company-source-${second_revision}.tar.gz"
printf 'corrupt\n' >> "$second_bundle"
if bash "$script" verify "$cache_root" "$second_revision"; then
  echo "corrupted bundle unexpectedly passed verification" >&2
  exit 1
fi

if bash "$script" verify "$cache_root" invalid-revision 2>/dev/null; then
  echo "invalid revision unexpectedly passed verification" >&2
  exit 1
fi

echo "cache-source-bundle tests passed"
