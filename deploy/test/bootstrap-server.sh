#!/usr/bin/env bash
set -euo pipefail

root="${1:-/srv/catscompany-test}"
compose_bin="/usr/local/bin/docker-compose"

mkdir -p \
  "$root/releases" \
  "$root/compose" \
  "$root/env" \
  "$root/data/mysql" \
  "$root/data/uploads" \
  "$root/logs"

if [ ! -x "$compose_bin" ]; then
  curl -L --fail \
    https://github.com/docker/compose/releases/latest/download/docker-compose-linux-x86_64 \
    -o "$compose_bin"
  chmod +x "$compose_bin"
fi

if [ -f "$root/env/env.test.example" ] && [ ! -f "$root/env/test.env" ]; then
  cp "$root/env/env.test.example" "$root/env/test.env"
fi

echo "Bootstrap ready:"
echo "  root: $root"
echo "  compose: $compose_bin"
echo "  env: $root/env/test.env"
