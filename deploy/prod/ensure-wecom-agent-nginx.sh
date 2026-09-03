#!/usr/bin/env bash
set -euo pipefail

root="${1:?stack root is required}"
source_config="$root/compose/catsco-wecom.conf"
target_config="/etc/nginx/sites-available/catsco-wecom"
enabled_link="/etc/nginx/sites-enabled/catsco-wecom"
certificate="/etc/letsencrypt/live/wecom.catsco.cn/fullchain.pem"
upstream_health="${WECOM_AGENT_HEALTH_URL:-http://10.254.0.2:12345/health}"

if [ ! -s "$source_config" ]; then
  echo "WeCom nginx source config is missing: $source_config" >&2
  exit 1
fi

if ! sudo test -s "$certificate"; then
  echo "wecom.catsco.cn certificate is not installed; keeping current nginx config"
  exit 0
fi

if ! curl -fsS -m 10 "$upstream_health" >/dev/null; then
  echo "WeCom middleware health check failed; keeping current nginx config" >&2
  exit 1
fi

backup_config="${target_config}.codex-backup.$$"
had_target=0
cleanup() {
  sudo rm -f "$backup_config"
}
trap cleanup EXIT

if [ -e "$target_config" ] || [ -L "$target_config" ]; then
  sudo cp -p "$target_config" "$backup_config"
  had_target=1
fi

sudo install -o root -g root -m 0644 "$source_config" "$target_config"
sudo ln -sfn "$target_config" "$enabled_link"
if ! sudo nginx -t -c /etc/nginx/nginx.conf; then
  sudo rm -f "$enabled_link"
  if [ "$had_target" -eq 1 ]; then
    sudo install -o root -g root -m 0644 "$backup_config" "$target_config"
    sudo ln -sfn "$target_config" "$enabled_link"
  else
    sudo rm -f "$target_config"
  fi
  sudo nginx -t -c /etc/nginx/nginx.conf >/dev/null || true
  echo "candidate WeCom nginx config failed validation; restored current config" >&2
  exit 1
fi

sudo systemctl reload nginx
echo "WeCom nginx routing enabled via $target_config"
