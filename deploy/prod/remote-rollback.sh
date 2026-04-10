#!/usr/bin/env bash
set -euo pipefail

root="${1:-/srv/catscompany-prod}"
compose_bin="/usr/local/bin/docker-compose"
compose_file="$root/compose/docker-compose.yml"
env_file="$root/env/prod.env"

if [ ! -f "$root/PREVIOUS_REVISION" ]; then
  echo "missing previous revision file: $root/PREVIOUS_REVISION" >&2
  exit 1
fi

previous_revision="$(cat "$root/PREVIOUS_REVISION")"

python3 - <<PY
from pathlib import Path

p = Path(r"$env_file")
text = p.read_text(encoding="utf-8", errors="replace").replace("\ufeff", "")

lines = []
replaced = False
for raw_line in text.splitlines():
    if raw_line.startswith("IMAGE_TAG="):
        lines.append("IMAGE_TAG=$previous_revision")
        replaced = True
    else:
        lines.append(raw_line)

if not replaced:
    lines.append("IMAGE_TAG=$previous_revision")

p.write_text("\n".join(lines) + "\n", encoding="utf-8")
PY

cd "$root/compose"
"$compose_bin" -f "$compose_file" --env-file "$env_file" pull server web
"$compose_bin" -f "$compose_file" --env-file "$env_file" up -d
"$compose_bin" -f "$compose_file" --env-file "$env_file" ps
printf '%s\n' "$previous_revision" > "$root/CURRENT_REVISION"

echo "rolled back production stack to revision $previous_revision"
