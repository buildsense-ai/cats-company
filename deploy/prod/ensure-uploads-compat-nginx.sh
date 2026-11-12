#!/usr/bin/env bash
set -Eeuo pipefail

if [ "$(id -u)" -ne 0 ]; then
  if ! command -v sudo >/dev/null 2>&1; then
    echo "updating uploads routing requires root or passwordless sudo" >&2
    exit 1
  fi
  exec sudo -n -- "$0" "$@"
fi

if [ "$#" -gt 0 ]; then
  echo "usage: $0" >&2
  exit 1
fi

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
if [ -f "$script_dir/tencent/nginx/uploads-zos.conf" ]; then
  source_snippet="$script_dir/tencent/nginx/uploads-zos.conf"
  route_helper="$script_dir/update-nginx-uploads-route.py"
else
  source_snippet="$script_dir/../tencent/nginx/uploads-zos.conf"
  route_helper="$script_dir/update-nginx-uploads-route.py"
fi
target="/etc/nginx/snippets/uploads-zos.conf"
app_vhost="/etc/nginx/sites-available/catscompany-app"
app_cn_vhost="/etc/nginx/sites-available/catscompany-app-cn"

if [ ! -f "$source_snippet" ] || [ ! -f "$route_helper" ]; then
  echo "missing uploads snippet or route migration helper" >&2
  exit 1
fi
for vhost in "$app_vhost" "$app_cn_vhost"; do
  if [ ! -f "$vhost" ]; then
    echo "missing expected app vhost: $vhost" >&2
    exit 1
  fi
done
if [ ! -d "$(dirname "$target")" ]; then
  echo "missing Nginx snippets directory: $(dirname "$target")" >&2
  exit 1
fi

backup_suffix="$(date +%Y%m%d%H%M%S)"
snippet_backup="${target}.catsco-attachment-compat.bak.${backup_suffix}"
cn_backup="${app_cn_vhost}.catsco-attachment-compat.bak.${backup_suffix}"
temporary="$(mktemp "$(dirname "$target")/.uploads-zos.conf.XXXXXX")"
mutated=0
had_target=0

cleanup() {
  rm -f "$temporary"
}
restore_previous() {
  if [ "$had_target" -eq 1 ]; then
    cp -a "$snippet_backup" "$target"
  else
    rm -f "$target"
  fi
  cp -a "$cn_backup" "$app_cn_vhost"
  if nginx -t >/dev/null 2>&1; then
    if command -v systemctl >/dev/null 2>&1; then
      systemctl reload nginx || true
    else
      nginx -s reload || true
    fi
  fi
}
on_exit() {
  local status=$?
  if [ "$status" -ne 0 ] && [ "$mutated" -eq 1 ]; then
    echo "uploads routing update failed; restoring the previous snippet and .cn vhost" >&2
    restore_previous
  fi
  cleanup
  exit "$status"
}
trap on_exit EXIT

if [ -f "$target" ]; then
  had_target=1
  cp -a "$target" "$snippet_backup"
  owner="$(stat -c '%u' "$target")"
  group="$(stat -c '%g' "$target")"
  mode="$(stat -c '%a' "$target")"
else
  owner=0
  group=0
  mode=644
fi
cp -a "$app_cn_vhost" "$cn_backup"
mutated=1

# The .cc vhost already includes the snippet; the .cn vhost may still have its
# legacy inline location. The helper accepts exactly one of those states.
python3 "$route_helper" "$app_vhost"
python3 "$route_helper" "$app_cn_vhost"

snippet_changed=1
if [ -f "$target" ] && cmp -s "$source_snippet" "$target"; then
  snippet_changed=0
fi
cn_changed=0
if ! cmp -s "$cn_backup" "$app_cn_vhost"; then
  cn_changed=1
fi
if [ "$snippet_changed" -eq 0 ] && [ "$cn_changed" -eq 0 ]; then
  echo "uploads compatibility routing already installed"
  exit 0
fi

mutated=1
if [ "$snippet_changed" -eq 1 ]; then
  install -o "$owner" -g "$group" -m "$mode" "$source_snippet" "$temporary"
  mv -f "$temporary" "$target"
fi

if ! nginx -t; then
  echo "new uploads routing failed nginx validation" >&2
  exit 1
fi
if command -v systemctl >/dev/null 2>&1; then
  systemctl reload nginx
else
  nginx -s reload
fi

mutated=0
if [ "$had_target" -eq 1 ]; then
  echo "temporary authenticated/legacy uploads routing installed; backups: $snippet_backup $cn_backup"
else
  echo "temporary authenticated/legacy uploads routing installed; backup: $cn_backup"
fi
