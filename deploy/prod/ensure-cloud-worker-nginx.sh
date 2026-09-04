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
renderer="$script_dir/update-nginx-cloud-worker-route.py"
if [ ! -f "$renderer" ]; then
  echo "missing cloud-worker Nginx renderer: $renderer" >&2
  exit 1
fi
if [ "$#" -eq 0 ]; then
  echo "at least one config path:server-name pair is required" >&2
  exit 1
fi

declare -a config_paths=()
declare -a rendered_paths=()
declare -a changed_paths=()
declare -a backup_paths=()
mutated=0

cleanup() {
  for path in "${rendered_paths[@]}"; do
    rm -f "$path"
  done
}

reload_nginx() {
  if command -v systemctl >/dev/null 2>&1; then
    systemctl reload nginx
  else
    nginx -s reload
  fi
}

restore_previous() {
  local index
  for index in "${!changed_paths[@]}"; do
    if [ -f "${backup_paths[$index]}" ]; then
      cp -a "${backup_paths[$index]}" "${changed_paths[$index]}"
    fi
  done
  if nginx -t >/dev/null 2>&1; then
    reload_nginx || true
  fi
}

on_exit() {
  local status=$?
  if [ "$status" -ne 0 ] && [ "$mutated" -eq 1 ]; then
    echo "cloud-worker Nginx update failed; restoring all changed vhosts" >&2
    restore_previous
  fi
  cleanup
  exit "$status"
}
trap on_exit EXIT

# Render every target before changing any file. An ambiguous/malformed second
# vhost must never leave the first vhost half-updated.
for spec in "$@"; do
  config_path="${spec%%:*}"
  server_name="${spec#*:}"
  if [ "$config_path" = "$server_name" ] || [ -z "$config_path" ] || [ -z "$server_name" ]; then
    echo "config argument must be path:server-name (got $spec)" >&2
    exit 1
  fi
  for existing_path in "${config_paths[@]}"; do
    if [ "$existing_path" = "$config_path" ]; then
      echo "duplicate host Nginx config argument: $config_path" >&2
      exit 1
    fi
  done
  if [ ! -f "$config_path" ]; then
    echo "missing host Nginx config: $config_path" >&2
    exit 1
  fi
  rendered="$config_path.rendered"
  # Register the temporary path before rendering so a Python parse error also
  # gets cleaned up by the EXIT trap.
  rendered_paths+=("$rendered")
  python3 "$renderer" --input "$config_path" --output "$rendered" --server-name "$server_name"
  config_paths+=("$config_path")
  if ! cmp -s "$config_path" "$rendered"; then
    changed_paths+=("$config_path")
    backup_paths+=("$config_path.catsco-cloud-worker.bak")
  fi
done

if [ "${#changed_paths[@]}" -eq 0 ]; then
  echo "cloud-worker Nginx route already configured"
  exit 0
fi

# Take all backups before the first install so rollback is complete even if a
# later install or Nginx validation fails.
for index in "${!changed_paths[@]}"; do
  cp -a "${changed_paths[$index]}" "${backup_paths[$index]}"
done
mutated=1

for index in "${!config_paths[@]}"; do
  config_path="${config_paths[$index]}"
  rendered="${rendered_paths[$index]}"
  # A rendered file exists for every target; only replace files that changed.
  changed=0
  for changed_path in "${changed_paths[@]}"; do
    if [ "$changed_path" = "$config_path" ]; then
      changed=1
      break
    fi
  done
  if [ "$changed" -eq 0 ]; then
    continue
  fi
  mode="$(stat -c '%a' "$config_path")"
  owner="$(stat -c '%u' "$config_path")"
  group="$(stat -c '%g' "$config_path")"
  install -o "$owner" -g "$group" -m "$mode" "$rendered" "$config_path"
done

if ! nginx -t; then
  echo "new cloud-worker Nginx config is invalid; restoring previous config" >&2
  exit 1
fi
if ! reload_nginx; then
  echo "host Nginx reload failed; restoring previous config" >&2
  exit 1
fi

mutated=0
echo "cloud-worker Nginx route configured for ${#changed_paths[@]} vhost(s)"
