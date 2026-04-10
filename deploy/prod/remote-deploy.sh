#!/usr/bin/env bash
set -euo pipefail

root="${1:-/srv/catscompany-prod}"
revision="${2:-}"
compose_bin="/usr/local/bin/docker-compose"
compose_dir="$root/compose"
env_dir="$root/env"
env_file="$env_dir/prod.env"
compose_file="$compose_dir/docker-compose.yml"
health_api="${PROD_HEALTH_API:-http://127.0.0.1:26061/health}"
health_web="${PROD_HEALTH_WEB:-http://127.0.0.1:28080/health}"

if [ -z "$revision" ]; then
  echo "usage: $0 <stack-root> <revision>" >&2
  exit 1
fi

mkdir -p \
  "$root/releases" \
  "$compose_dir" \
  "$env_dir" \
  "$root/data/uploads" \
  "$root/logs" \
  "/usr/local/bin"

if [ ! -x "$compose_bin" ]; then
  curl -L --fail \
    https://github.com/docker/compose/releases/latest/download/docker-compose-linux-x86_64 \
    -o "$compose_bin"
  chmod +x "$compose_bin"
fi

if [ ! -f "$compose_file" ]; then
  echo "missing compose file: $compose_file" >&2
  exit 1
fi

if [ ! -f "$env_file" ]; then
  if [ -f "$env_dir/env.prod.example" ]; then
    cp "$env_dir/env.prod.example" "$env_file"
    echo "created template env file at $env_file" >&2
    echo "fill real secrets, then rerun deploy" >&2
  else
    echo "missing env file: $env_file" >&2
  fi
  exit 1
fi

python3 - <<PY
from pathlib import Path

p = Path(r"$env_file")
text = p.read_text(encoding="utf-8", errors="replace").replace("\ufeff", "")

updates = {
    "GHCR_REGISTRY": "ghcr.io",
    "GHCR_OWNER": "${GHCR_OWNER:-}",
    "IMAGE_TAG": "$revision",
}

lines = []
seen = set()
for raw_line in text.splitlines():
    line = raw_line
    if "=" in line and not line.lstrip().startswith("#"):
        key, _, value = line.partition("=")
        if key in updates and updates[key]:
            line = f"{key}={updates[key]}"
            seen.add(key)
    lines.append(line)

for key, value in updates.items():
    if value and key not in seen:
        lines.append(f"{key}={value}")

p.write_text("\n".join(lines) + "\n", encoding="utf-8")
PY

if [ -n "${GHCR_USERNAME:-}" ] && [ -n "${GHCR_TOKEN:-}" ]; then
  printf '%s\n' "$GHCR_TOKEN" | docker login ghcr.io -u "$GHCR_USERNAME" --password-stdin >/dev/null
fi

if [ -f "$root/CURRENT_REVISION" ]; then
  cp "$root/CURRENT_REVISION" "$root/PREVIOUS_REVISION"
fi

cd "$compose_dir"
"$compose_bin" -f "$compose_file" --env-file "$env_file" pull server web
"$compose_bin" -f "$compose_file" --env-file "$env_file" up -d
"$compose_bin" -f "$compose_file" --env-file "$env_file" ps

printf '%s\n' "$revision" > "$root/CURRENT_REVISION"

curl -fsS "$health_api" >/dev/null
curl -fsS "$health_web" >/dev/null

echo "deployed revision $revision to $root"
