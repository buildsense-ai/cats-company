#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
tmpdir="$(mktemp -d)"
trap 'rm -rf "$tmpdir"' EXIT

base_reference='georgjung/nginx-brotli:latest@sha256:488e48d7773deef7f696a25362da3043e3aabb447c6f28548eda7391a27c7fc9'
base_digest='sha256:488e48d7773deef7f696a25362da3043e3aabb447c6f28548eda7391a27c7fc9'

fake_bin="$tmpdir/bin"
docker_log="$tmpdir/docker.log"
docker_images="$tmpdir/docker-images"
mkdir -p "$fake_bin"
: > "$docker_log"
: > "$docker_images"

# The seed step has to be exercised without a daemon, so this stub only records
# pulls and tags. FAKE_PULL_OK names the one source whose pull succeeds; a tag
# makes both the tagged reference and its digest-qualified form inspectable,
# which mirrors how the deploy host resolves the base after a mirror pull.
cat > "$fake_bin/docker" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail

if [ "${1:-}" = "image" ] && [ "${2:-}" = "inspect" ]; then
  if [ "${3:-}" = "--format" ]; then
    ref="${5:-}"
  else
    ref="${3:-}"
  fi
  if grep -Fxq "$ref" "$FAKE_DOCKER_IMAGES"; then
    if [ "${3:-}" = "--format" ]; then
      echo "sha256:0000000000000000000000000000000000000000000000000000000000000000"
    fi
    exit 0
  fi
  exit 1
fi
if [ "${1:-}" = "pull" ]; then
  ref="${2:-}"
  printf 'pull %s\n' "$ref" >> "$FAKE_DOCKER_LOG"
  case "$ref" in
    *"${FAKE_PULL_OK:?}"*)
      printf '%s\n' "$ref" >> "$FAKE_DOCKER_IMAGES"
      exit 0
      ;;
  esac
  exit 1
fi
if [ "${1:-}" = "tag" ]; then
  printf 'tag %s %s\n' "${2:-}" "${3:-}" >> "$FAKE_DOCKER_LOG"
  printf '%s\n' "${3:-}" >> "$FAKE_DOCKER_IMAGES"
  printf '%s@%s\n' "${3:-}" "$FAKE_TAG_DIGEST" >> "$FAKE_DOCKER_IMAGES"
  exit 0
fi
printf 'unexpected docker command: %s\n' "$*" >&2
exit 1
EOF
chmod +x "$fake_bin/docker"

run_ensure() {
  PATH="$fake_bin:$PATH" \
  FAKE_DOCKER_LOG="$docker_log" \
  FAKE_DOCKER_IMAGES="$docker_images" \
  FAKE_PULL_OK="$1" \
  FAKE_TAG_DIGEST="$base_digest" \
  REMOTE_WEB_BASE_MIRRORS="$2" \
    bash "$repo_root/deploy/shared/ensure-build-cache.sh" "$3"
}

# A base that is already present is left alone: no pull is even attempted.
cache_root="$tmpdir/cache"
printf '%s\n' "$base_reference" >> "$docker_images"
run_ensure nosuch '' "$cache_root"

test -d "$cache_root/releases"
test -d "$cache_root/source"
test "$(stat -c '%a' "$cache_root")" = "700"
test "$(grep -c '^pull ' "$docker_log" || true)" = "0"

# A missing base is seeded from the first source whose pull works; the registry
# reference is tried before the mirrors.
: > "$docker_log"
: > "$docker_images"
seed_output="$(run_ensure 1panel docker.1panel.live "$cache_root" 2>&1)"
grep -q "Seeding Web build base from ${base_reference}" <<<"$seed_output"
grep -q "Seeding Web build base from docker.1panel.live/${base_reference}" <<<"$seed_output"
grep -q "Web build base seeded from docker.1panel.live/${base_reference}" <<<"$seed_output"
test "$(head -n 1 "$docker_log")" = "pull ${base_reference}"
grep -q "^pull docker.1panel.live/${base_reference}$" "$docker_log"
grep -q "^tag .* georgjung/nginx-brotli:latest$" "$docker_log"
grep -Fxq "$base_reference" "$docker_images"

# Every source failing is a warning, not a failure: the build's own fallback
# still works, it just grows the layer chain.
: > "$docker_log"
: > "$docker_images"
missing_output="$(run_ensure nosuch docker.1panel.live "$cache_root" 2>&1)"
grep -q "WARNING: the pinned Web build base ${base_reference} is missing" <<<"$missing_output"

# A cache root that needs chmod is still repaired.
chmod 755 "$cache_root"
run_ensure nosuch '' "$cache_root" >/dev/null 2>&1
test "$(stat -c '%a' "$cache_root")" = "700"

echo "ensure-build-cache tests passed"
