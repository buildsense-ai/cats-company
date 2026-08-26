#!/usr/bin/env bash
set -euo pipefail

root="${1:?stack root is required}"
source_config="$root/compose/catsco-public.conf"
target_config="/etc/nginx/sites-available/catsco-public"
enabled_link="/etc/nginx/sites-enabled/catsco-public"
app_config="/etc/nginx/sites-available/catscompany-app"
certificate="/etc/letsencrypt/live/catsco.cc/fullchain.pem"
website_health="${PROD_HEALTH_WEBSITE:-http://127.0.0.1:28081/health}"

if [ ! -s "$source_config" ]; then
  echo "public website nginx source config is missing: $source_config" >&2
  exit 1
fi

if [ ! -s "$certificate" ]; then
  echo "public website certificate is not installed; keeping current nginx config" >&2
  exit 0
fi

if ! curl -fsS -m 10 "$website_health" >/dev/null; then
  echo "public website health check failed; keeping current nginx config" >&2
  exit 0
fi

if [ ! -s "$app_config" ]; then
  echo "app nginx config is missing; keeping current nginx config: $app_config" >&2
  exit 1
fi

work_dir="$(mktemp -d)"
public_candidate="$work_dir/catsco-public"
app_candidate="$work_dir/catscompany-app"
sudo cp -p "$source_config" "$public_candidate"
sudo cp -p "$app_config" "$app_candidate"

# Preserve the production app config and only point its root HTTP redirect at
# the temporary preview. Root HTTPS remains in an independent config file.
if ! sudo python3 - "$app_candidate" <<'PY'
from pathlib import Path
import sys

path = Path(sys.argv[1])
data = path.read_bytes()
patterns = [
    (b"server_name catsco.cc www.catsco.cc;\n    return 301 https://app.catsco.cc$request_uri;",
     b"server_name catsco.cc www.catsco.cc;\n    return 301 https://preview.catsco.cc$request_uri;"),
    (b"server_name catsco.cc www.catsco.cc;\r\n    return 301 https://app.catsco.cc$request_uri;",
     b"server_name catsco.cc www.catsco.cc;\r\n    return 301 https://preview.catsco.cc$request_uri;"),
    (b"server_name catsco.cc www.catsco.cc;\n    return 301 https://$host$request_uri;",
     b"server_name catsco.cc www.catsco.cc;\n    return 301 https://preview.catsco.cc$request_uri;"),
    (b"server_name catsco.cc www.catsco.cc;\r\n    return 301 https://$host$request_uri;",
     b"server_name catsco.cc www.catsco.cc;\r\n    return 301 https://preview.catsco.cc$request_uri;"),
]
for old, new in patterns:
    count = data.count(old)
    if count:
        if count != 1:
            raise SystemExit(f"expected one root HTTP redirect, found {count}")
        data = data.replace(old, new)
        path.write_bytes(data)
        break
else:
    if (b"server_name catsco.cc www.catsco.cc;\n    return 301 https://preview.catsco.cc$request_uri;" not in data
            and b"server_name catsco.cc www.catsco.cc;\r\n    return 301 https://preview.catsco.cc$request_uri;" not in data):
        raise SystemExit("root HTTP redirect was not found in app nginx config")
PY
then
  rm -rf "$work_dir"
  exit 1
fi

backup_config="${target_config}.codex-backup.$$"
backup_app_config="${app_config}.codex-backup.$$"
backup_enabled_link="${enabled_link}.codex-backup.$$"
had_target=0
had_enabled=0
old_enabled_target=""
cleanup() {
  rm -rf "$work_dir"
  sudo rm -f "$backup_config" "$backup_app_config" "$backup_enabled_link"
}
trap cleanup EXIT

if [ -e "$target_config" ] || [ -L "$target_config" ]; then
  sudo cp -p "$target_config" "$backup_config"
  had_target=1
fi
sudo cp -p "$app_config" "$backup_app_config"
if [ -L "$enabled_link" ]; then
  old_enabled_target="$(readlink "$enabled_link")"
  had_enabled=1
elif [ -e "$enabled_link" ]; then
  sudo cp -a "$enabled_link" "$backup_enabled_link"
  had_enabled=1
fi

sudo install -o root -g root -m 644 "$public_candidate" "$target_config"
sudo install -o root -g root -m 644 "$app_candidate" "$app_config"
sudo ln -sfn "$target_config" "$enabled_link"
if ! sudo nginx -t -c /etc/nginx/nginx.conf; then
  if [ "$had_target" -eq 1 ]; then
    sudo install -o root -g root -m 644 "$backup_config" "$target_config"
  else
    sudo rm -f "$target_config"
  fi
  sudo install -o root -g root -m 644 "$backup_app_config" "$app_config"
  sudo rm -f "$enabled_link"
  if [ "$had_enabled" -eq 1 ]; then
    if [ -n "$old_enabled_target" ]; then
      sudo ln -s "$old_enabled_target" "$enabled_link"
    else
      sudo cp -a "$backup_enabled_link" "$enabled_link"
    fi
  fi
  sudo nginx -t -c /etc/nginx/nginx.conf >/dev/null || true
  echo "candidate public nginx config failed validation; restored current config" >&2
  exit 1
fi
sudo systemctl reload nginx
echo "public website nginx routing enabled via $target_config"
