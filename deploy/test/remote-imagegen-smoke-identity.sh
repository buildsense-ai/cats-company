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

container_env() {
  local container_id="$1"
  local name="$2"
  docker inspect "$container_id" --format '{{range .Config.Env}}{{println .}}{{end}}' |
    sed -n "s/^${name}=//p" |
    head -n 1
}

psql_compatible_dsn() {
  python3 - "$1" <<'PY'
import sys
from urllib.parse import parse_qsl, urlencode, urlsplit, urlunsplit

parts = urlsplit(sys.argv[1])
query = []
for key, value in parse_qsl(parts.query, keep_blank_values=True):
    if key == "search_path":
        query.append(("options", f"-csearch_path={value}"))
    else:
        query.append((key, value))
print(urlunsplit((parts.scheme, parts.netloc, parts.path, urlencode(query), parts.fragment)))
PY
}

create_identity() {
  local server_id="$1"
  local smoke_user="$2"
  local random_suffix="$3"
  local mysql_id
  mysql_id="$(compose ps -q mysql)"
  if [[ -n "$mysql_id" ]]; then
    docker exec -i "$mysql_id" sh -lc 'exec mysql -N -B -u"$MYSQL_USER" -p"$MYSQL_PASSWORD" openchat' <<SQL
INSERT INTO users (username, display_name, account_type, pass_hash, state)
VALUES ('${smoke_user}', 'Imagegen Smoke', 'bot', '!', 0);
SET @smoke_uid = LAST_INSERT_ID();
SET @smoke_key = CONCAT('cc_', LOWER(HEX(@smoke_uid)), '_', '${random_suffix}');
INSERT INTO bot_config (user_id, api_endpoint, model, enabled, api_key)
VALUES (@smoke_uid, '', 'gpt-image-2', 1, @smoke_key);
SELECT @smoke_key;
SQL
    return
  fi

  local driver dsn network
  driver="$(container_env "$server_id" OC_DB_DRIVER)"
  dsn="$(container_env "$server_id" OC_DB_DSN)"
  network="$(docker inspect "$server_id" --format '{{range $name, $_ := .NetworkSettings.Networks}}{{println $name}}{{end}}' | head -n 1)"
  if [[ "$driver" != "postgres" || -z "$dsn" || -z "$network" ]]; then
    echo "test database is neither the local mysql service nor configured postgres" >&2
    exit 1
  fi
  dsn="$(psql_compatible_dsn "$dsn")"
  docker run --rm -i --network "$network" postgres:16-alpine \
    psql "$dsn" -qAt -v ON_ERROR_STOP=1 -v smoke_user="$smoke_user" -v random_suffix="$random_suffix" <<'SQL'
WITH created_user AS (
  INSERT INTO users (username, display_name, account_type, pass_hash, state)
  VALUES (:'smoke_user', 'Imagegen Smoke', 'bot', decode('', 'hex'), 0)
  RETURNING id
), created_bot AS (
  INSERT INTO bot_config (user_id, api_endpoint, model, enabled, api_key)
  SELECT id, '', 'gpt-image-2', true,
         'cc_' || to_hex(id) || '_' || :'random_suffix'
  FROM created_user
  RETURNING api_key
)
SELECT api_key FROM created_bot;
SQL
}

delete_identity() {
  local server_id="$1"
  local smoke_key="$2"
  local mysql_id
  mysql_id="$(compose ps -q mysql)"
  if [[ -n "$mysql_id" ]]; then
    docker exec -i "$mysql_id" sh -lc 'exec mysql -u"$MYSQL_USER" -p"$MYSQL_PASSWORD" openchat' <<SQL
DELETE u FROM users u
JOIN bot_config b ON b.user_id = u.id
WHERE b.api_key = '${smoke_key}' AND u.username LIKE 'imagegen\\_smoke\\_%';
SQL
    return
  fi

  local driver dsn network
  driver="$(container_env "$server_id" OC_DB_DRIVER)"
  dsn="$(container_env "$server_id" OC_DB_DSN)"
  network="$(docker inspect "$server_id" --format '{{range $name, $_ := .NetworkSettings.Networks}}{{println $name}}{{end}}' | head -n 1)"
  if [[ "$driver" == "postgres" && -n "$dsn" && -n "$network" ]]; then
    dsn="$(psql_compatible_dsn "$dsn")"
    docker run --rm -i --network "$network" postgres:16-alpine \
      psql "$dsn" -v ON_ERROR_STOP=1 -v smoke_key="$smoke_key" <<'SQL'
DELETE FROM users u
USING bot_config b
WHERE b.user_id = u.id
  AND b.api_key = :'smoke_key'
  AND u.username LIKE 'imagegen\_smoke\_%' ESCAPE '\';
SQL
  fi
}

case "$action" in
  prepare)
    mkdir -p "$(dirname "$key_file")"
    server_id="$(compose ps -q server)"
    if [[ -z "$server_id" ]]; then
      echo "test server container is unavailable" >&2
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
    random_suffix="$(openssl rand -hex 32)"
    smoke_key="$(create_identity "$server_id" "$smoke_user" "$random_suffix")"
    if [[ "$smoke_key" != cc_*_* ]]; then
      echo "failed to create a parseable smoke bot key" >&2
      exit 1
    fi
    umask 077
    printf '%s' "$smoke_key" > "$key_file"
    ;;
  cleanup)
    if [[ ! -f "$key_file" ]]; then
      exit 0
    fi
    server_id="$(compose ps -q server)"
    smoke_key="$(cat "$key_file")"
    if [[ -n "$server_id" && "$smoke_key" == cc_smoke_* ]]; then
      delete_identity "$server_id" "$smoke_key"
    fi
    rm -f "$key_file"
    ;;
  *)
    echo "unknown action: $action" >&2
    exit 2
    ;;
esac
