#!/usr/bin/env bash
set -euo pipefail

root="${1:-/srv/cats-bifrost}"
revision="${2:-}"
service="${RELAY_ADAPTER_SERVICE:-cats-openai-adapter.service}"
health_url="${RELAY_ADAPTER_HEALTH_URL:-http://127.0.0.1:18091/health}"
source_file="$root/releases/openai_adapter-${revision}.py"
target_dir="$root/adapter"
target_file="$target_dir/openai_adapter.py"
rollback_file="$root/releases/openai_adapter-rollback-${revision}.py"

if [[ ! "$revision" =~ ^[0-9a-f]{40}$ ]]; then
  echo "usage: $0 <relay-root> <revision>" >&2
  echo "revision must be a 40-character lowercase Git SHA" >&2
  exit 1
fi
if [ ! -f "$source_file" ]; then
  echo "missing release source: $source_file" >&2
  exit 1
fi

python3 -m py_compile "$source_file"
mkdir -p "$target_dir" "$root/releases"

previous_revision="unmanaged-baseline"
if [ -s "$root/CURRENT_ADAPTER_REVISION" ]; then
  previous_revision="$(cat "$root/CURRENT_ADAPTER_REVISION")"
fi

if [ -f "$target_file" ]; then
  cp -p "$target_file" "$rollback_file"
else
  rm -f "$rollback_file"
fi

restart_service() {
  sudo -n systemctl restart "$service"
}

wait_for_health() {
  for attempt in $(seq 1 15); do
    if curl -fsS -m 5 "$health_url" >/dev/null; then
      return 0
    fi
    echo "waiting for relay adapter health ($attempt/15): $health_url"
    sleep 1
  done
  return 1
}

install -m 0644 "$source_file" "$target_dir/.openai_adapter.py.${revision}.new"
mv -f "$target_dir/.openai_adapter.py.${revision}.new" "$target_file"

if restart_service && wait_for_health; then
  printf '%s\n' "$revision" > "$root/CURRENT_ADAPTER_REVISION"
  printf '%s\n' "$rollback_file" > "$root/ROLLBACK_ADAPTER_SOURCE"
  printf '%s\n' "$previous_revision" > "$root/ROLLBACK_ADAPTER_REVISION"
  echo "deployed relay adapter revision $revision"
  exit 0
fi

echo "relay adapter health failed; restoring previous source" >&2
if [ -f "$rollback_file" ]; then
  install -m 0644 "$rollback_file" "$target_dir/.openai_adapter.py.rollback.new"
  mv -f "$target_dir/.openai_adapter.py.rollback.new" "$target_file"
  restart_service || true
  wait_for_health || true
fi
exit 1
