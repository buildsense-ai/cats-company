#!/usr/bin/env bash
set -euo pipefail

root="${1:-/srv/catscompany-prod}"
revision="${2:-}"
compose_dir="$root/compose"
env_dir="$root/env"
env_file="$env_dir/prod.env"
compose_file="$compose_dir/docker-compose.yml"
health_api="${PROD_HEALTH_API:-http://127.0.0.1:26061/health}"
health_web="${PROD_HEALTH_WEB:-http://127.0.0.1:28080/health}"

compose() {
  if command -v docker-compose >/dev/null 2>&1; then
    docker-compose "$@"
  else
    docker compose "$@"
  fi
}

wait_for_health() {
  local name="$1"
  local url="$2"
  local attempts="${3:-30}"
  local delay="${4:-2}"

  for attempt in $(seq 1 "$attempts"); do
    if curl -fsS -m 10 "$url" >/dev/null; then
      echo "$name health ok"
      return 0
    fi

    echo "waiting for $name health ($attempt/$attempts): $url"
    sleep "$delay"
  done

  echo "$name health check failed after $attempts attempts: $url" >&2
  curl -sS -m 10 "$url" >&2 || true
  echo >&2
  return 1
}

if [ -z "$revision" ]; then
  echo "usage: $0 <stack-root> <revision>" >&2
  exit 1
fi

mkdir -p \
  "$root/releases" \
  "$compose_dir" \
  "$env_dir" \
  "$root/data/uploads" \
  "$root/logs"

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
    "GHCR_REGISTRY": "${GHCR_REGISTRY:-ghcr.io}",
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

allow_shared_db_user="$(sed -n 's/^ALLOW_SHARED_DB_USER=//p' "$env_file" | tail -n 1)"
db_driver="$(sed -n 's/^OC_DB_DRIVER=//p' "$env_file" | tail -n 1)"
db_dsn="$(sed -n 's/^OC_DB_DSN=//p' "$env_file" | tail -n 1)"
db_user="${db_dsn%%:*}"

if [ "${db_driver:-mysql}" = "mysql" ] && [ "${allow_shared_db_user:-0}" != "1" ] && [ "$db_user" = "openchat" ]; then
  cat >&2 <<EOF
refusing deploy: OC_DB_DSN is using the shared MySQL DB user "openchat"
while ALLOW_SHARED_DB_USER is not enabled.

Use a dedicated database user for production containers when possible.

If sharing this user is intentional, set:
ALLOW_SHARED_DB_USER=1
EOF
  exit 1
fi

if [ -n "${GHCR_USERNAME:-}" ] && [ -n "${GHCR_TOKEN:-}" ]; then
  printf '%s\n' "$GHCR_TOKEN" | docker login ghcr.io -u "$GHCR_USERNAME" --password-stdin >/dev/null
fi

if [ -f "$root/CURRENT_REVISION" ]; then
  cp "$root/CURRENT_REVISION" "$root/PREVIOUS_REVISION"
fi

cd "$compose_dir"
if [ "${SKIP_IMAGE_PULL:-0}" != "1" ]; then
  compose -f "$compose_file" --env-file "$env_file" pull server web
fi
compose -f "$compose_file" --env-file "$env_file" up -d
compose -f "$compose_file" --env-file "$env_file" ps

printf '%s\n' "$revision" > "$root/CURRENT_REVISION"

wait_for_health "api" "$health_api"
wait_for_health "web" "$health_web"

echo "deployed revision $revision to $root"
