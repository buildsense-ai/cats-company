#!/usr/bin/env bash
set -euo pipefail

root="${1:-/srv/catscompany-test}"
key_file="${2:-$root/run/imagegen-smoke-key}"
run_dir="${3:?run directory is required}"
skill_dir="$root/skills/imagegen"
deps_dir="$root/run/imagegen-python-libs"
pip_cache_dir="$root/run/imagegen-pip-cache"

capture_server_log() {
  local server_id
  server_id="$(docker ps --filter label=com.docker.compose.project=catscompany_test --filter label=com.docker.compose.service=server --format '{{.ID}}' | head -n 1)"
  if [[ -n "$server_id" ]]; then
    docker logs --since 30m "$server_id" > "$run_dir/server.log" 2>&1 || true
  fi
}
trap capture_server_log EXIT

if [[ ! -s "$key_file" ]]; then
  echo "imagegen smoke identity is unavailable" >&2
  exit 1
fi
if [[ ! -f "$skill_dir/scripts/invoke_imagegen.py" ]]; then
  echo "imagegen Skill is unavailable" >&2
  exit 1
fi

mkdir -p "$run_dir/generate" "$run_dir/edit" "$deps_dir" "$pip_cache_dir"
cp "$root/compose/imagegen-smoke-generate.json" "$run_dir/generate-request.json"

preflight_status="$(curl --silent --output "$run_dir/auth-preflight.json" --write-out '%{http_code}' \
  -H "Authorization: Bearer $(cat "$key_file")" \
  -H 'Content-Type: application/json' \
  --data '{}' \
  http://127.0.0.1:16061/v1/images/generations)"
if [[ "$preflight_status" != "400" ]]; then
  echo "imagegen Bearer authentication preflight returned HTTP $preflight_status" >&2
  exit 1
fi

docker run --rm --network host \
  -v "$skill_dir:/opt/imagegen:ro" \
  -v "$deps_dir:/opt/imagegen-deps" \
  -v "$pip_cache_dir:/root/.cache/pip" \
  -v "$key_file:/run/imagegen-key:ro" \
  -v "$run_dir:/work" \
  -w /work \
  python:3.12-slim sh -ceu '
    if [ ! -d /opt/imagegen-deps/openai ]; then
      python -m pip install --disable-pip-version-check --quiet \
        --target /opt/imagegen-deps -r /opt/imagegen/requirements.txt
    fi
    export PYTHONPATH=/opt/imagegen-deps
    export CATSCO_IMAGE_API_BASE=http://127.0.0.1:16061/v1
    export CATSCO_API_KEY="$(cat /run/imagegen-key)"
    export IMAGE_GEN_MODEL=gpt-image-2
    export IMAGE_GEN_DEFAULT_SIZE=1024x1024
    export IMAGE_GEN_DEFAULT_QUALITY=medium
    python /opt/imagegen/scripts/invoke_imagegen.py \
      --request /work/generate-request.json \
      --out-dir /work/generate
    python - <<"PY"
import json
from pathlib import Path

request = {
    "prompt": (
        "Keep the mug, camera angle, composition, lighting, shadows, "
        "travertine surface and warm-white background unchanged. "
        "Change only the mug color from forest green to cobalt blue. "
        "No text, no logo."
    ),
    "referenced_image_paths": ["/work/generate/image.png"],
}
Path("/work/edit-request.json").write_text(
    json.dumps(request, ensure_ascii=False, indent=2), encoding="utf-8"
)
PY
    python /opt/imagegen/scripts/invoke_imagegen.py \
      --request /work/edit-request.json \
      --out-dir /work/edit
  '
