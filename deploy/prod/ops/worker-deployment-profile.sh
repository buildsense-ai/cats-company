#!/usr/bin/env bash
# Sourced by lifecycle scripts. JSON is data, never shell-sourced or evaluated.
worker_profile_load() {
  local tenant="$1" persist="${2:-0}" state_dir raw key value
  [[ -n "$tenant" ]] || return 0 # caller retains its original usage error
  [[ "$tenant" =~ ^[a-z0-9][a-z0-9_-]{1,63}$ ]] || { echo 'error: invalid deployment tenant' >&2; return 2; }
  state_dir="${CTYUN_WORKER_STATE_DIR:-/var/lib/catsco-worker/$tenant}"
  [[ -z "${CTYUN_WORKER_STATE_ROOT:-}" ]] || state_dir="${CTYUN_WORKER_STATE_ROOT%/}/$tenant"
  raw="${CATSCO_WORKER_DEPLOYMENT_JSON:-}"
  if [[ -z "$raw" && -f "$state_dir/deployment.json" ]]; then
    [[ ! -L "$state_dir/deployment.json" ]] || return 2
    raw="$(cat "$state_dir/deployment.json")"
  fi
  [[ -n "$raw" ]] || return 0 # legacy NAT worker
  export CATSCO_WORKER_DEFAULT_REGION_ID="${CATSCO_WORKER_DEFAULT_REGION_ID:-${CTYUN_WORKER_REGION_ID:-}}"
  export CATSCO_WORKER_DEFAULT_PROJECT_ID="${CATSCO_WORKER_DEFAULT_PROJECT_ID:-${CTYUN_WORKER_PROJECT_ID:-0}}"
  export CATSCO_ARTIFACT_GATEWAY_SSH_IP="${CATSCO_ARTIFACT_GATEWAY_SSH_IP:-${CTYUN_JUMP_IP:-}}"
  export CATSCO_ARTIFACT_GATEWAY_SSH_PORT="${CATSCO_ARTIFACT_GATEWAY_SSH_PORT:-${CTYUN_JUMP_PORT:-22}}"
  export CATSCO_ARTIFACT_GATEWAY_SSH_USER="${CATSCO_ARTIFACT_GATEWAY_SSH_USER:-${CTYUN_JUMP_USER:-root}}"
  export CATSCO_ARTIFACT_GATEWAY_SSH_KEY="${CATSCO_ARTIFACT_GATEWAY_SSH_KEY:-${CTYUN_JUMP_KEY:-/var/lib/catsco-worker/jump_host_ed25519}}"
  jq -e '
    (.profile == "private_nat" or .profile == "public_ip") and (.env | type == "object") and
    (.env | to_entries | all( .key | IN(
      "CTYUN_WORKER_REGION_ID", "CTYUN_WORKER_PROJECT_ID", "CTYUN_IMAGE_PROJECT_ID",
      "CTYUN_WORKER_AZ_NAME", "CTYUN_WORKER_FLAVOR_ID", "CTYUN_WORKER_VPC_ID",
      "CTYUN_WORKER_SUBNET_ID", "CTYUN_WORKER_SECURITY_GROUP_ID", "CTYUN_WORKER_EXT_IP",
      "CTYUN_WORKER_BILLING_MODE", "CTYUN_WORKER_CYCLE_COUNT",
      "CTYUN_JUMP_IP", "CTYUN_JUMP_PORT", "CTYUN_JUMP_USER", "CTYUN_JUMP_KEY",
      "CATSCO_WORKER_HTTP_BASE_URL", "CATSCO_WORKER_SERVER_URL"))) and
    (.env | to_entries | all(.value | type == "string" and (test("[\u0000\r\n]") | not))) and
    (.env.CTYUN_WORKER_REGION_ID | type == "string" and length > 0)
  ' <<<"$raw" >/dev/null || { echo 'error: invalid worker deployment snapshot' >&2; return 2; }
  while IFS= read -r -d '' key && IFS= read -r -d '' value; do
    export "$key=$value"
  done < <(jq -j '.env | to_entries[] | .key, "\u0000", .value, "\u0000"' <<<"$raw")
  export CATSCO_WORKER_DEPLOYMENT_JSON="$raw"
  if [[ "$(jq -r .profile <<<"$raw")" == public_ip ]]; then
    export CTYUN_WORKER_EXT_IP=1 CTYUN_JUMP_IP='' CTYUN_JUMP_KEY=''
  else
    export CTYUN_WORKER_EXT_IP=0
  fi
  if [[ "$persist" == 1 ]]; then
    (umask 077; mkdir -p "$state_dir"; printf '%s\n' "$raw" > "$state_dir/deployment.json.tmp")
    mv -f "$state_dir/deployment.json.tmp" "$state_dir/deployment.json"
  fi
}

worker_connection_ip() {
  # Public machines must wait for their EIP rather than trying an unroutable
  # private address. Preserve the historical private-mode fallback.
  jq -r --arg public "${CTYUN_WORKER_EXT_IP:-0}" '
    if $public == "1" then (.floatingIP // .publicIP // "")
    else (.fixedIPList[0] // .privateIP // .floatingIP // .publicIP // "") end
  '
}
