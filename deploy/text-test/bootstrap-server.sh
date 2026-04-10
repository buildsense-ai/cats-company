#!/usr/bin/env bash
set -euo pipefail

root="${1:-/root/text/catscompany-docker-test}"
compose_bin="/root/text/bin/docker-compose"

mkdir -p \
  "$root/releases" \
  "$root/compose" \
  "$root/env" \
  "$root/data/mysql" \
  "$root/data/uploads" \
  "$root/logs" \
  "/root/text/bin"

if [ ! -x "$compose_bin" ]; then
  curl -L --fail \
    https://github.com/docker/compose/releases/latest/download/docker-compose-linux-x86_64 \
    -o "$compose_bin"
  chmod +x "$compose_bin"
fi

if [ -f "$root/env/text-test.env.example" ] && [ ! -f "$root/env/text-test.env" ]; then
  cp "$root/env/text-test.env.example" "$root/env/text-test.env"
fi

echo "Bootstrap ready:"
echo "  root: $root"
echo "  compose: $compose_bin"
echo "  env: $root/env/text-test.env"
