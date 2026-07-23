#!/usr/bin/env bash
set -euo pipefail

root="${1:-/srv/catscompany-prod}"
compose_file="$root/compose/docker-compose.yml"
env_file="$root/env/prod.env"

compose() {
  if command -v docker-compose >/dev/null 2>&1; then
    docker-compose "$@"
  else
    docker compose "$@"
  fi
}

if [ ! -f "$root/PREVIOUS_REVISION" ]; then
  echo "missing previous revision file: $root/PREVIOUS_REVISION" >&2
  exit 1
fi

previous_revision="$(cat "$root/PREVIOUS_REVISION")"
current_worker_revision="$(sed -n 's/^DREAMINA_IMAGE_TAG=//p' "$env_file" | tail -n 1)"
registry="$(sed -n 's/^GHCR_REGISTRY=//p' "$env_file" | tail -n 1)"
owner="$(sed -n 's/^GHCR_OWNER=//p' "$env_file" | tail -n 1)"
registry="${registry:-ghcr.io}"
worker_revision="${current_worker_revision:-$previous_revision}"
if docker image inspect "${registry}/${owner}/cats-company-dreamina-worker:${previous_revision}" >/dev/null 2>&1; then
  worker_revision="$previous_revision"
fi

python3 - <<PY
from pathlib import Path

p = Path(r"$env_file")
text = p.read_text(encoding="utf-8", errors="replace").replace("\ufeff", "")

updates = {
    "IMAGE_TAG": "$previous_revision",
    "DREAMINA_IMAGE_TAG": "$worker_revision",
}
lines = []
seen = set()
for raw_line in text.splitlines():
    if "=" in raw_line and not raw_line.lstrip().startswith("#"):
        key, _, _ = raw_line.partition("=")
        if key in updates:
            lines.append(f"{key}={updates[key]}")
            seen.add(key)
            continue
    lines.append(raw_line)

for key, value in updates.items():
    if key not in seen:
        lines.append(f"{key}={value}")

p.write_text("\n".join(lines) + "\n", encoding="utf-8")
PY

cd "$root/compose"
if [ "${SKIP_IMAGE_PULL:-0}" != "1" ]; then
  compose -f "$compose_file" --env-file "$env_file" pull server web
fi
compose -f "$compose_file" --env-file "$env_file" up -d
compose -f "$compose_file" --env-file "$env_file" ps
printf '%s\n' "$previous_revision" > "$root/CURRENT_REVISION"

echo "rolled back production stack to revision $previous_revision"
