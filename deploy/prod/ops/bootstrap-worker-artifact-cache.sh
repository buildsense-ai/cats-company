#!/usr/bin/env bash
# Run on the existing SSH jump host. This creates a separate private listener;
# it does not change the artifact gateway or its public routes.
set -Eeuo pipefail
ip="${CATSCO_WORKER_CACHE_LISTEN_IP:?set the jump host private IPv4 address}"
port="${CATSCO_WORKER_CACHE_PORT:-19992}"
root="${CATSCO_WORKER_ARTIFACT_MIRROR_DIR:-/var/cache/catsco-worker-artifacts}"
cidrs="${CATSCO_WORKER_CACHE_ALLOW_CIDRS:?set comma-separated worker subnet CIDRs}"
[[ "$ip" =~ ^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$ &&
   ( "$ip" == 10.* || "$ip" == 192.168.* || "$ip" =~ ^172\.(1[6-9]|2[0-9]|3[01])\. ) ]] || {
  echo 'error: cache listener must use a private IPv4 address' >&2; exit 2;
}
[[ "$port" =~ ^[1-9][0-9]{0,4}$ ]] && ((port <= 65535)) || exit 2
[[ "$root" =~ ^/[A-Za-z0-9_./-]+$ && "$root" != / && "$root" != *..* ]] || exit 2
IFS=, read -ra allowed <<<"$cidrs"
((${#allowed[@]})) || exit 2
for cidr in "${allowed[@]}"; do
  [[ "$cidr" =~ ^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+/([0-9]|[12][0-9]|3[0-2])$ ]] || exit 2
done
for cmd in nginx flock curl sha256sum; do
  command -v "$cmd" >/dev/null || { echo "error: cache host requires $cmd" >&2; exit 2; }
done
install -d -m 0755 "$root"
config=/etc/nginx/conf.d/20-catsco-worker-cache.conf
backup=$(mktemp /tmp/catsco-worker-cache-config.XXXXXX)
existed=0
if [[ -f "$config" ]]; then cp "$config" "$backup"; existed=1; fi
rollback() {
  if ((existed)); then cp "$backup" "$config"; else rm -f "$config"; fi
}
trap 'rm -f "$backup"' EXIT
{
  printf 'server {\n    listen %s:%s;\n    server_name _;\n    root %s;\n' "$ip" "$port" "$root"
  printf '    autoindex off;\n    sendfile on;\n    gzip off;\n'
  for cidr in "${allowed[@]}"; do printf '    allow %s;\n' "$cidr"; done
  printf '    deny all;\n'
  cat <<'NGINX'
    location ~ "^/[0-9A-Za-z][0-9A-Za-z._+-]{0,63}/[0-9a-f]{64}/[0-9A-Za-z._+-]+\.tar\.gz$" {
        try_files $uri =404;
    }
    location / { return 404; }
}
NGINX
} > "$config"
if ! nginx -t; then rollback; exit 1; fi
if ! systemctl reload nginx; then rollback; nginx -t && systemctl reload nginx; exit 1; fi
printf 'Worker cache ready at http://%s:%s (allow: %s)\n' "$ip" "$port" "$cidrs"
