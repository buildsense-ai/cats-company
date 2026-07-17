#!/usr/bin/env bash
set -euo pipefail

if [ "$(id -u)" -ne 0 ]; then
  if ! command -v sudo >/dev/null 2>&1; then
    echo "updating the host Nginx config requires root or passwordless sudo" >&2
    exit 1
  fi
  exec sudo -n -- "$0" "$@"
fi

config_path="${1:-/etc/nginx/sites-available/catscompany-app}"
timeout_seconds="${2:-580}"
script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
renderer="$script_dir/update-nginx-v1-timeout.py"

if [[ ! "$timeout_seconds" =~ ^[1-9][0-9]*$ ]]; then
  echo "image Nginx timeout must be a positive number of seconds" >&2
  exit 1
fi
if [ ! -f "$config_path" ]; then
  echo "missing host Nginx config: $config_path" >&2
  exit 1
fi
if [ ! -f "$renderer" ]; then
  echo "missing Nginx timeout renderer: $renderer" >&2
  exit 1
fi

config_dir="$(dirname "$config_path")"
rendered="$(mktemp "$config_dir/.catsco-nginx-rendered.XXXXXX")"
backup="${config_path}.catsco-image-timeout.bak"
cleanup() {
  rm -f "$rendered"
}
trap cleanup EXIT

python3 "$renderer" \
  --input "$config_path" \
  --output "$rendered" \
  --timeout "$timeout_seconds"

if cmp -s "$config_path" "$rendered"; then
  echo "host Nginx /v1/ timeout already ${timeout_seconds}s"
  exit 0
fi

cp -a "$config_path" "$backup"
mode="$(stat -c '%a' "$config_path")"
owner="$(stat -c '%u' "$config_path")"
group="$(stat -c '%g' "$config_path")"
install -o "$owner" -g "$group" -m "$mode" "$rendered" "$config_path"

reload_nginx() {
  if command -v systemctl >/dev/null 2>&1; then
    systemctl reload nginx
  else
    nginx -s reload
  fi
}

restore_previous() {
  cp -a "$backup" "$config_path"
  nginx -t
  reload_nginx
}

if ! nginx -t; then
  echo "new host Nginx config is invalid; restoring previous config" >&2
  restore_previous
  exit 1
fi
if ! reload_nginx; then
  echo "host Nginx reload failed; restoring previous config" >&2
  restore_previous
  exit 1
fi

echo "host Nginx /v1/ timeout updated to ${timeout_seconds}s"
