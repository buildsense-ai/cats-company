#!/usr/bin/env bash
set -euo pipefail

action="${1:?action is required}"
root="${2:-/srv/catscompany-test}"
key_file="${3:-$root/run/imagegen-smoke-key}"
compose_file="$root/compose/docker-compose.yml"
env_file="$root/env/test.env"

compose() {
  if command -v docker-compose >/dev/null 2>&1; then
    docker-compose -f "$compose_file" --env-file "$env_file" "$@"
  else
    docker compose -f "$compose_file" --env-file "$env_file" "$@"
  fi
}

case "$action" in
  prepare)
    mkdir -p "$(dirname "$key_file")"
    server_id="$(compose ps -q server)"
    mysql_id="$(compose ps -q mysql)"
    if [[ -z "$server_id" || -z "$mysql_id" ]]; then
      echo "test server or mysql container is unavailable" >&2
      exit 1
    fi

    configured="$(docker inspect "$server_id" --format '{{range .Config.Env}}{{println .}}{{end}}' | awk -F= '
      $1 == "CATSCO_IMAGE_UPSTREAMS_FILE" && length($2) > 0 { pool=1 }
      $1 == "CATSCO_IMAGE_UPSTREAM_URL" && length($2) > 0 { url=1 }
      $1 == "CATSCO_IMAGE_UPSTREAM_API_KEY" && length($2) > 0 { key=1 }
      $1 == "CATSCO_IMAGE_UPSTREAM_API_KEY_FILE" && length($2) > 0 { keyfile=1 }
      END { print (pool || (url && (key || keyfile))) ? "yes" : "no" }
    ')"
    if [[ "$configured" != "yes" ]]; then
      echo "test image upstream is not configured" >&2
      exit 1
    fi

    smoke_suffix="$(date +%s)_${RANDOM}"
    smoke_user="imagegen_smoke_${smoke_suffix}"
    smoke_key="cc_smoke_$(openssl rand -hex 24)"
    docker exec -i "$mysql_id" sh -lc 'exec mysql -u"$MYSQL_USER" -p"$MYSQL_PASSWORD" openchat' <<SQL
INSERT INTO users (username, display_name, account_type, pass_hash, state)
VALUES ('${smoke_user}', 'Imagegen Smoke', 'bot', '!', 0);
SET @smoke_uid = LAST_INSERT_ID();
INSERT INTO bot_config (user_id, api_endpoint, model, enabled, api_key)
VALUES (@smoke_uid, '', 'gpt-image-2', 1, '${smoke_key}');
SQL
    umask 077
    printf '%s' "$smoke_key" > "$key_file"
    ;;
  cleanup)
    if [[ ! -f "$key_file" ]]; then
      exit 0
    fi
    mysql_id="$(compose ps -q mysql)"
    smoke_key="$(cat "$key_file")"
    if [[ -n "$mysql_id" && "$smoke_key" == cc_smoke_* ]]; then
      docker exec -i "$mysql_id" sh -lc 'exec mysql -u"$MYSQL_USER" -p"$MYSQL_PASSWORD" openchat' <<SQL
DELETE u FROM users u
JOIN bot_config b ON b.user_id = u.id
WHERE b.api_key = '${smoke_key}' AND u.username LIKE 'imagegen\\_smoke\\_%';
SQL
    fi
    rm -f "$key_file"
    ;;
  *)
    echo "unknown action: $action" >&2
    exit 2
    ;;
esac
