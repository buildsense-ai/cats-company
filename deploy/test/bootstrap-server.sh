#!/usr/bin/env bash
set -euo pipefail

root="${1:-/srv/catscompany-test}"

mkdir -p \
  "$root/releases" \
  "$root/compose" \
  "$root/env" \
  "$root/data/mysql" \
  "$root/data/uploads" \
  "$root/logs"

if command -v docker-compose >/dev/null 2>&1; then
  docker-compose version >/dev/null
elif command -v docker >/dev/null 2>&1 && docker compose version >/dev/null 2>&1; then
  :
else
  echo "docker compose is required. Install Docker Compose before deployment." >&2
  exit 1
fi

if [ -f "$root/env/env.test.example" ] && [ ! -f "$root/env/test.env" ]; then
  cp "$root/env/env.test.example" "$root/env/test.env"
fi

echo "Bootstrap ready:"
echo "  root: $root"
echo "  env: $root/env/test.env"
