#!/usr/bin/env bash
set -Eeuo pipefail

if [ "$(id -u)" -ne 0 ]; then
  if ! command -v sudo >/dev/null 2>&1; then
    echo "updating the host Nginx config requires root or passwordless sudo" >&2
    exit 1
  fi
  exec sudo -n -- "$0" "$@"
fi

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
renderer="$script_dir/update-nginx-shimo-login.py"
config_path="${1:-/etc/nginx/sites-available/catscompany-app}"
env_file="${2:-/srv/catscompany-prod/env/prod.env}"
worker_port="${PROD_SHIMO_WORKER_HOST_PORT:-}"

# This script runs as root (via sudo above), so the production env file can be
# read here without weakening its owner-only permissions.  A missing variable
# is valid and retains the compose default.
if [ -z "$worker_port" ] && [ -r "$env_file" ]; then
  worker_port="$(sed -n 's/^PROD_SHIMO_WORKER_HOST_PORT=//p' "$env_file" | tail -n 1)"
fi
worker_port="${worker_port:-26070}"

if [ ! -f "$renderer" ]; then
  echo "missing Shimo Nginx renderer: $renderer" >&2
  exit 1
fi
if [ ! -s "$config_path" ]; then
  echo "missing host Nginx config: $config_path" >&2
  exit 1
fi

config_dir="$(dirname "$config_path")"
rendered="$config_path.rendered"
backup="$config_path.catsco-shimo.bak"
trap 'rm -f "$rendered"' EXIT

python3 "$renderer" --input "$config_path" --output "$rendered" --worker-port "$worker_port"
if cmp -s "$config_path" "$rendered"; then
  echo "Shimo Nginx routes already configured"
  exit 0
fi

cp -a "$config_path" "$backup"
mode="$(stat -c '%a' "$config_path")"
owner="$(stat -c '%u' "$config_path")"
group="$(stat -c '%g' "$config_path")"
install -o "$owner" -g "$group" -m "$mode" "$rendered" "$config_path"

restore() {
  install -o "$owner" -g "$group" -m "$mode" "$backup" "$config_path"
  nginx -t >/dev/null 2>&1 || true
  rm -f "$backup"
}
if ! nginx -t; then
  echo "new Shimo Nginx config is invalid; restoring previous config" >&2
  restore
  exit 1
fi
if ! (systemctl reload nginx 2>/dev/null || nginx -s reload); then
  echo "host Nginx reload failed; restoring previous config" >&2
  restore
  exit 1
fi
rm -f "$backup"
echo "Shimo login and grant routes configured in $config_path"
