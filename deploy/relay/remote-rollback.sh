#!/usr/bin/env bash
set -euo pipefail

root="${1:-/srv/cats-bifrost}"
service="${RELAY_ADAPTER_SERVICE:-cats-openai-adapter.service}"
health_url="${RELAY_ADAPTER_HEALTH_URL:-http://127.0.0.1:18091/health}"
target_file="$root/adapter/openai_adapter.py"
rollback_pointer="$root/ROLLBACK_ADAPTER_SOURCE"
rollback_revision_pointer="$root/ROLLBACK_ADAPTER_REVISION"

if [ ! -s "$rollback_pointer" ]; then
  echo "missing rollback pointer: $rollback_pointer" >&2
  exit 1
fi
if [ ! -s "$rollback_revision_pointer" ]; then
  echo "missing rollback revision: $rollback_revision_pointer" >&2
  exit 1
fi
rollback_file="$(cat "$rollback_pointer")"
rollback_revision="$(cat "$rollback_revision_pointer")"
if [ ! -f "$rollback_file" ]; then
  echo "missing rollback source: $rollback_file" >&2
  exit 1
fi

python3 -m py_compile "$rollback_file"
install -m 0644 "$rollback_file" "$root/adapter/.openai_adapter.py.rollback.new"
mv -f "$root/adapter/.openai_adapter.py.rollback.new" "$target_file"
sudo -n systemctl restart "$service"
curl -fsS --retry 15 --retry-delay 1 --retry-connrefused -m 5 "$health_url" >/dev/null
printf '%s\n' "$rollback_revision" > "$root/CURRENT_ADAPTER_REVISION"
echo "rolled back relay adapter to revision $rollback_revision from $rollback_file"
