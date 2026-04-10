#!/usr/bin/env bash
set -euo pipefail

root="${1:-/srv/catscompany-test}"
compose_bin="/usr/local/bin/docker-compose"
compose_file="$root/compose/docker-compose.yml"
env_file="$root/env/test.env"

echo "stack root: $root"
if [ -f "$root/CURRENT_REVISION" ]; then
  echo "current revision: $(cat "$root/CURRENT_REVISION")"
fi

if [ -x "$compose_bin" ] && [ -f "$compose_file" ] && [ -f "$env_file" ]; then
  "$compose_bin" -f "$compose_file" --env-file "$env_file" ps
else
  echo "compose/env not ready"
fi

echo "--- api health ---"
curl -sS -m 10 http://127.0.0.1:16061/health || true
echo
echo "--- web health ---"
curl -sS -m 10 http://127.0.0.1:18080/health || true
echo
