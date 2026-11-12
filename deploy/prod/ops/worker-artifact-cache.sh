#!/usr/bin/env bash
# Sourced by deploy-worker-version.sh. Every transfer is checked against the
# private manifest; object-store credentials remain on the control server.
worker_prune_artifact_cache() (
  local root="$1" current="$2" keep="$3" entry version sorted i artifact pin active now modified
  local versions=()
  [[ "$keep" =~ ^[1-9][0-9]*$ && -d "$root" && ! -L "$root" ]] || return 0
  # Updates hold shared locks locally. Pin creation on the mirror uses this
  # same exclusive lock so pruning cannot race a new worker download.
  exec 9>"$root/.prune.lock" || return 1
  flock -xn 9 || return 0
  now=$(date +%s)
  for entry in "$root"/*; do
    [[ -d "$entry" && ! -L "$entry" ]] || continue
    version="${entry##*/}"
    [[ "$version" =~ ^[0-9A-Za-z][0-9A-Za-z._+-]{0,63}$ ]] || continue
    # Never treat tenant state directories as disposable cache versions.
    artifact=
    for artifact in "$entry"/catsco-worker-*.tar.gz "$entry"/*/catsco-worker-*.tar.gz; do
      [[ -f "$artifact" && ! -L "$artifact" ]] && break
    done
    [[ -f "$artifact" && ! -L "$artifact" ]] || continue
    active=0
    for pin in "$entry"/.inuse-*; do
      [[ -f "$pin" ]] || continue
      modified=$(stat -c %Y "$pin") || return 1
      if ((now-modified < 86400)); then active=1; break; fi
    done
    ((active == 0)) || continue
    versions+=("$version")
  done
  ((${#versions[@]} > keep)) || return 0
  # BusyBox find lacks -printf; its sort supports -V.
  sorted="$(printf '%s\n' "${versions[@]}" | sort -V)" || {
    echo 'warning: could not sort worker artifact cache versions' >&2
    return 1
  }
  mapfile -t versions <<<"$sorted"
  for ((i=0; i<${#versions[@]}-keep; i++)); do
    version="${versions[i]}"
    [[ "$version" != "$current" ]] || continue
    if ! rm -rf -- "${root:?}/$version"; then
      echo "warning: could not prune worker artifact cache version=$version" >&2
      return 1
    fi
  done
)

worker_copy_artifact_from_mirror() (
  local base="${CATSCO_WORKER_ARTIFACT_MIRROR_URL:-}"
  local root="${CATSCO_WORKER_ARTIFACT_MIRROR_DIR:-/var/cache/catsco-worker-artifacts}"
  local relative remote_file upload cached_sha url signed_url pin filled=0
  [[ -n "$base" && -n "$JUMP_IP" ]] || return 1
  if [[ ! "$base" =~ ^https?://[A-Za-z0-9._:-]+/?$ ||
        ! "$root" =~ ^/[A-Za-z0-9_./-]+$ || "$root" == / || "$root" == *..* ]]; then
    echo 'warning: invalid worker artifact mirror configuration; using SCP' >&2
    return 1
  fi
  local mirror_opts=(-i "$JUMP_KEY" -p "$JUMP_PORT" -o BatchMode=yes -o ConnectTimeout=10
    -o ServerAliveInterval=15 -o ServerAliveCountMax=3
    -o StrictHostKeyChecking=accept-new -o UserKnownHostsFile="$STATE_DIR/jump_known_hosts")
  relative="$VERSION/${EXPECTED_SHA,,}/$ARTIFACT_FILE"
  remote_file="${root%/}/$relative"
  upload="${remote_file}.part-${NAME}-$$"
  pin="${root%/}/$VERSION/.inuse-${NAME}-$$"
  trap 'timeout -s TERM -k 5 15s ssh "${mirror_opts[@]}" "$JUMP_USER@$JUMP_IP" "rm -f '\''$pin'\'' '\''$upload'\''" </dev/null || true' EXIT
  cached_sha="$(timeout -s TERM -k 5 20s ssh -n "${mirror_opts[@]}" "$JUMP_USER@$JUMP_IP" \
    "exec 9>'$root/.prune.lock'; flock -x 9 && install -d -m 0755 '${remote_file%/*}' && touch '$pin' && flock -u 9 && { if test -r '$remote_file'; then sha256sum '$remote_file'; fi; }")" || {
    echo 'warning: private artifact cache probe/pin failed; using SCP' >&2
    return 1
  }
  if [[ "${cached_sha%% *}" != "${EXPECTED_SHA,,}" ]]; then
    echo 'worker-update: filling private artifact cache' >&2
    # Delegate one GET for ten minutes over SSH stdin. Neither the signed URL
    # nor permanent credentials enter argv, logs, or files on the jump host.
    if signed_url="$(timeout -s TERM -k 5 20s tos-fetch \
         --endpoint "$ARTIFACT_ENDPOINT" --region "$ARTIFACT_REGION" --bucket "$ARTIFACT_BUCKET" \
         --presign-key "$ARTIFACT_PREFIX/$VERSION/$ARTIFACT_FILE")" &&
       [[ "$signed_url" == https://* && "$signed_url" != *[$'\r\n"\\']* ]] &&
       printf 'url = "%s"\n' "$signed_url" | timeout -s TERM -k 10 140s ssh "${mirror_opts[@]}" "$JUMP_USER@$JUMP_IP" \
         "curl --fail --silent --show-error --connect-timeout 10 --max-time 120 --config - --output '$upload' --write-out 'worker-update: cache-fill bytes=%{size_download} seconds=%{time_total} bytes_per_second=%{speed_download}\\n' >&2"; then
      filled=1
    else
      echo 'warning: direct private artifact cache fill failed; trying SCP fill' >&2
    fi
    local mirror_scp_opts=(-i "$JUMP_KEY" -P "$JUMP_PORT" "${mirror_opts[@]:4}")
    if ((filled == 0)); then
      timeout -s TERM -k 15 180s scp "${mirror_scp_opts[@]}" "$ARTIFACT" "$JUMP_USER@$JUMP_IP:$upload" || {
        echo 'warning: private artifact cache SCP fill failed; using worker SCP' >&2
        return 1
      }
    fi
    timeout -s TERM -k 5 30s ssh -n "${mirror_opts[@]}" "$JUMP_USER@$JUMP_IP" \
      "printf '%s  %s\\n' '${EXPECTED_SHA,,}' '$upload' | sha256sum -c - >&2 && chmod 0644 '$upload' && mv -f '$upload' '$remote_file'" || {
      echo 'warning: private artifact cache publish/checksum failed; using SCP' >&2
      return 1
    }
  else
    echo 'worker-update: private artifact cache hit' >&2
  fi
  url="${base%/}/$relative"
  if ! timeout -s TERM -k 10 140s ssh -n "${ssh_opts[@]}" "root@$INSTANCE_IP" \
      "curl --fail --silent --show-error --connect-timeout 5 --max-time 120 --write-out 'worker-update: private-download bytes=%{size_download} seconds=%{time_total} bytes_per_second=%{speed_download}\\n' --output '${REMOTE_PREFIX}.tar.gz' '$url' >&2 && printf '%s  %s\\n' '${EXPECTED_SHA,,}' '${REMOTE_PREFIX}.tar.gz' | sha256sum -c - >&2"; then
    echo 'warning: private artifact download or checksum failed; using SCP' >&2
    return 1
  fi
  timeout -s TERM -k 5 15s ssh -n "${mirror_opts[@]}" "$JUMP_USER@$JUMP_IP" "rm -f '$pin'" || true
  {
    declare -f worker_prune_artifact_cache
    printf '\nworker_prune_artifact_cache %q %q %q\n' "$root" "$VERSION" "$ARTIFACT_CACHE_KEEP"
  } | timeout -s TERM -k 5 20s ssh "${mirror_opts[@]}" "$JUMP_USER@$JUMP_IP" bash -s ||
      echo 'warning: could not prune private worker artifact cache' >&2
)
