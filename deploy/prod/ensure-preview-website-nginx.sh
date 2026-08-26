#!/usr/bin/env bash
set -euo pipefail

root="${1:?stack root is required}"
source_config="$root/compose/catsco-preview.conf"
target_config="/etc/nginx/sites-available/catsco-preview"
enabled_link="/etc/nginx/sites-enabled/catsco-preview"
certificate="${PREVIEW_WEBSITE_CERTIFICATE:-/etc/letsencrypt/live/preview.catsco.cc/fullchain.pem}"
website_health="${PROD_HEALTH_WEBSITE:-http://127.0.0.1:28081/health}"

if [ ! -s "$source_config" ]; then
  echo "preview website nginx source config is missing: $source_config" >&2
  exit 1
fi
if [ ! -s "$certificate" ]; then
  echo "preview website certificate is not installed; keeping current nginx config" >&2
  exit 0
fi
if ! curl -fsS -m 10 "$website_health" >/dev/null; then
  echo "preview website health check failed; keeping current nginx config" >&2
  exit 0
fi

backup_config="${target_config}.codex-backup.$$"
had_target=0
cleanup() { sudo rm -f "$backup_config"; }
trap cleanup EXIT

if [ -e "$target_config" ] || [ -L "$target_config" ]; then
  sudo cp -p "$target_config" "$backup_config"
  had_target=1
fi
sudo install -o root -g root -m 644 "$source_config" "$target_config"
sudo ln -sfn "$target_config" "$enabled_link"
if ! sudo nginx -t -c /etc/nginx/nginx.conf; then
  if [ "$had_target" -eq 1 ]; then
    sudo install -o root -g root -m 644 "$backup_config" "$target_config"
  else
    sudo rm -f "$target_config" "$enabled_link"
  fi
  sudo nginx -t -c /etc/nginx/nginx.conf >/dev/null || true
  echo "candidate preview nginx config failed validation; restored current config" >&2
  exit 1
fi
sudo systemctl reload nginx
echo "preview website nginx routing enabled via $target_config"
