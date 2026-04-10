#!/usr/bin/env bash
set -euo pipefail

root="${1:-/srv/catscompany-prod}"
compose_bin="/usr/local/bin/docker-compose"

mkdir -p \
  "$root/releases" \
  "$root/compose" \
  "$root/env" \
  "$root/data/uploads" \
  "$root/logs"

if [ ! -x "$compose_bin" ]; then
  curl -L --fail \
    https://github.com/docker/compose/releases/latest/download/docker-compose-linux-x86_64 \
    -o "$compose_bin"
  chmod +x "$compose_bin"
fi

if [ -f "$root/env/env.prod.example" ] && [ ! -f "$root/env/prod.env" ]; then
  cp "$root/env/env.prod.example" "$root/env/prod.env"
fi

echo "Bootstrap ready:"
echo "  root: $root"
echo "  compose: $compose_bin"
echo "  env: $root/env/prod.env"
