#!/usr/bin/env bash
set -euo pipefail

if [ $# -ne 2 ]; then
  echo "usage: $0 <stack-root> <revision>" >&2
  exit 1
fi

root="$1"
revision="$2"
archive="$root/releases/cats-company-$revision.tar.gz"
release_dir="$root/app/releases/$revision"
current_dir="$root/app/current"
compose_dir="$root/compose"
env_dir="$root/env"
compose_bin="/root/text/bin/docker-compose"
env_file="$env_dir/text-test.env"
compose_file="$compose_dir/docker-compose.yml"
health_api="${TEXT_TEST_HEALTH_API:-http://127.0.0.1:16061/health}"
health_web="${TEXT_TEST_HEALTH_WEB:-http://127.0.0.1:18080/health}"

mkdir -p \
  "$root/releases" \
  "$root/app/releases" \
  "$compose_dir" \
  "$env_dir" \
  "$root/data/mysql" \
  "$root/data/uploads" \
  "$root/logs" \
  "/root/text/bin"

if [ ! -f "$archive" ]; then
  echo "missing release archive: $archive" >&2
  exit 1
fi

if [ ! -x "$compose_bin" ]; then
  curl -L --fail \
    https://github.com/docker/compose/releases/latest/download/docker-compose-linux-x86_64 \
    -o "$compose_bin"
  chmod +x "$compose_bin"
fi

rm -rf "$release_dir"
mkdir -p "$release_dir"
tar -xzf "$archive" -C "$release_dir"
ln -sfn "$release_dir" "$current_dir"
printf '%s\n' "$revision" > "$root/CURRENT_REVISION"

ln -sfn "$current_dir/deploy/text-test/docker-compose.yml" "$compose_file"
ln -sfn "$current_dir/deploy/text-test/text-test.env.example" "$env_dir/text-test.env.example"

if [ ! -f "$env_file" ]; then
  cp "$env_dir/text-test.env.example" "$env_file"
  echo "created template env file at $env_file" >&2
  echo "fill real secrets, then rerun deploy" >&2
  exit 1
fi

python3 - <<PY
from pathlib import Path
p = Path(r"$env_file")
text = p.read_text(encoding="utf-8", errors="replace").replace("\ufeff", "")
p.write_text(text, encoding="utf-8")
PY

cd "$compose_dir"
"$compose_bin" -f "$compose_file" --env-file "$env_file" up -d --build
"$compose_bin" -f "$compose_file" --env-file "$env_file" ps

curl -fsS "$health_api" >/dev/null
curl -fsS "$health_web" >/dev/null

echo "deployed revision $revision to $root"
