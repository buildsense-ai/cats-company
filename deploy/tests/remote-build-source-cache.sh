#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
temp_root="$(mktemp -d)"
trap 'rm -rf "$temp_root"' EXIT

stack_root="$temp_root/stack"
cache_root="$temp_root/cache"
fixture_root="$temp_root/fixture"
fake_bin="$temp_root/bin"
docker_log="$temp_root/docker.log"
docker_images="$temp_root/docker-images"
revision="0123456789abcdef0123456789abcdef01234567"
fallback_revision="89abcdef0123456789abcdef0123456789abcdef"

mkdir -p "$stack_root/releases" "$cache_root/releases" "$cache_root/source" "$fixture_root" "$fake_bin"
printf 'fixture\n' > "$fixture_root/README.md"
tar -C "$fixture_root" -czf "$cache_root/releases/cats-company-source-${revision}.tar.gz" .
tar -C "$fixture_root" -czf "$cache_root/releases/cats-company-source-${fallback_revision}.tar.gz" .
: > "$docker_log"
: > "$docker_images"

cat > "$fake_bin/docker" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail

if [ "${1:-}" = "image" ] && [ "${2:-}" = "inspect" ]; then
  grep -Fxq "${3:-}" "$FAKE_DOCKER_IMAGES"
  exit $?
fi
if [ "${1:-}" = "build" ]; then
  image=""
  previous=""
  for argument in "$@"; do
    if [ "$previous" = "-t" ]; then
      image="$argument"
      break
    fi
    previous="$argument"
  done
  printf 'build %s %s\n' "$image" "$*" >> "$FAKE_DOCKER_LOG"
  printf '%s\n' "$image" >> "$FAKE_DOCKER_IMAGES"
  exit 0
fi
if [ "${1:-}" = "pull" ]; then
  printf 'pull %s\n' "${2:-}" >> "$FAKE_DOCKER_LOG"
  exit 1
fi
if [ "${1:-}" = "login" ]; then
  exit 0
fi
printf 'unexpected docker command: %s\n' "$*" >&2
exit 1
EOF
chmod +x "$fake_bin/docker"

run_build() {
  local target_revision="${1:?revision is required}"
  local web_mode="${2:-pull}"
  PATH="$fake_bin:$PATH" \
  FAKE_DOCKER_LOG="$docker_log" \
  FAKE_DOCKER_IMAGES="$docker_images" \
  CATSCO_SHARED_RELEASE_ROOT="$cache_root/releases" \
  CATSCO_SHARED_SOURCE_ROOT="$cache_root/source" \
  REMOTE_WEB_IMAGE_MODE="$web_mode" \
    bash "$repo_root/deploy/remote-build-source.sh" "$stack_root" "$target_revision" buildsense-ai
}

first_output="$(run_build "$revision" local 2>&1)"
printf 'preserve\n' > "$cache_root/source/$revision/reuse-marker"
second_output="$(run_build "$revision" local 2>&1)"
fallback_output="$(run_build "$fallback_revision" pull 2>&1)"

[ "$(grep -c '^build ' "$docker_log")" -eq 8 ]
[ "$(grep -c '^pull ' "$docker_log")" -eq 1 ]
[ -f "$cache_root/source/$revision/reuse-marker" ]
grep -q 'Building web image locally' <<<"$first_output"
grep -q -- '--build-arg APK_REPOSITORY=https://mirrors.aliyun.com/alpine' "$docker_log"
grep -q 'timed out after 120s' <<<"$fallback_output"
grep -q 'Source tree already present' <<<"$second_output"
grep -q 'Server image already present' <<<"$second_output"
grep -q 'Dreamina worker image already present' <<<"$second_output"
grep -q 'Web image already present' <<<"$second_output"

echo "remote-build-source cache tests passed"
