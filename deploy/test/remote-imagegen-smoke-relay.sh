#!/usr/bin/env bash
set -euo pipefail

action="${1:?action is required}"
root="${2:-/srv/catscompany-test}"
run_id="${3:?run id is required}"
compose_file="$root/compose/docker-compose.yml"
env_file="$root/env/test.env"
state_dir="$root/run/imagegen-smoke-relay-$run_id"
secret_name="imagegen-smoke-upstream-$run_id.key"
provider_name="imagegen-smoke-providers-$run_id.json"
secret_host_path="$root/secrets/$secret_name"
provider_host_path="$root/secrets/$provider_name"

compose() {
  if command -v docker-compose >/dev/null 2>&1; then
    docker-compose -f "$compose_file" --env-file "$env_file" "$@"
  else
    docker compose -f "$compose_file" --env-file "$env_file" "$@"
  fi
}

restart_server() {
  compose up -d --force-recreate server >/dev/null
  for _ in $(seq 1 60); do
    if curl --silent --fail http://127.0.0.1:16061/health >/dev/null 2>&1; then
      return 0
    fi
    sleep 1
  done
  echo "test gateway did not become healthy after relay switch" >&2
  return 1
}

case "$action" in
  enable)
    mkdir -p "$state_dir" "$root/secrets"
    if [[ -e "$state_dir/test.env.before" ]]; then
      echo "smoke relay state already exists for run $run_id" >&2
      exit 1
    fi
    cp "$env_file" "$state_dir/test.env.before"

    umask 077
    IFS= read -r upstream_key
    if [[ -z "$upstream_key" ]]; then
      echo "smoke relay key is empty" >&2
      exit 1
    fi
    printf '%s' "$upstream_key" > "$secret_host_path"
    unset upstream_key

    cat > "$provider_host_path" <<JSON
{
  "providers": [
    {
      "id": "catsco-live-relay",
      "generation_url": "https://app.catsco.cc/v1/images/generations",
      "edit_url": "https://app.catsco.cc/v1/images/edits",
      "model": "gpt-image-2",
      "api_key_file": "/run/catsco-secrets/$secret_name",
      "edit_transport": "json_data_url",
      "timeout_seconds": 540
    }
  ]
}
JSON
    chmod 600 "$secret_host_path" "$provider_host_path"

    python3 - "$env_file" "/run/catsco-secrets/$provider_name" <<'PY'
import sys
from pathlib import Path

path = Path(sys.argv[1])
pool_path = sys.argv[2]
updates = {
    "CATSCO_IMAGE_UPSTREAMS_FILE": pool_path,
    "CATSCO_IMAGE_UPSTREAM_URL": "",
    "CATSCO_IMAGE_UPSTREAM_API_KEY": "",
    "CATSCO_IMAGE_UPSTREAM_API_KEY_FILE": "",
    "CATSCO_IMAGE_DEFAULT_PROVIDER": "image2",
}
seen = set()
out = []
for line in path.read_text(encoding="utf-8").splitlines():
    key = line.split("=", 1)[0] if "=" in line and not line.lstrip().startswith("#") else None
    if key in updates:
        if key not in seen:
            out.append(f"{key}={updates[key]}")
            seen.add(key)
        continue
    out.append(line)
for key, value in updates.items():
    if key not in seen:
        out.append(f"{key}={value}")
path.write_text("\n".join(out) + "\n", encoding="utf-8")
PY
    restart_server
    ;;
  disable)
    if [[ ! -f "$state_dir/test.env.before" ]]; then
      exit 0
    fi
    cp "$state_dir/test.env.before" "$env_file"
    restart_server || true
    rm -f "$secret_host_path" "$provider_host_path"
    rm -rf "$state_dir"
    ;;
  *)
    echo "unknown action: $action" >&2
    exit 2
    ;;
esac
