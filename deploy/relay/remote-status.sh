#!/usr/bin/env bash
set -euo pipefail

root="${1:-/srv/cats-bifrost}"
service="${RELAY_ADAPTER_SERVICE:-cats-openai-adapter.service}"
health_url="${RELAY_ADAPTER_HEALTH_URL:-http://127.0.0.1:18091/health}"

if [ -f "$root/CURRENT_ADAPTER_REVISION" ]; then
  echo "adapter revision: $(cat "$root/CURRENT_ADAPTER_REVISION")"
else
  echo "adapter revision: unmanaged baseline"
fi
sha256sum "$root/adapter/openai_adapter.py"
sudo -n systemctl is-active "$service"
curl -fsS -m 5 "$health_url"
echo
