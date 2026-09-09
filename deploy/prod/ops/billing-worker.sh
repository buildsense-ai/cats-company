#!/usr/bin/env bash
# Per-tenant trial billing operations. Conversion is never retried blindly:
# the provider has no client token for ConvertEcsToCycle.
set -euo pipefail
NAME='' ACTION=''
while (($#)); do
  case "$1" in
    --name) NAME="${2:-}"; shift 2;;
    --action) ACTION="${2:-}"; shift 2;;
    *) echo 'error: unsupported billing argument' >&2; exit 2;;
  esac
done
[[ "$NAME" =~ ^[a-z0-9][a-z0-9_-]{1,63}$ && "$ACTION" =~ ^(suspend|start|convert|cancel)$ ]] || { echo 'error: valid --name and --action required' >&2; exit 2; }
OPS_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$OPS_DIR/worker-deployment-profile.sh"
worker_profile_load "$NAME"
REGION="${CTYUN_WORKER_REGION_ID:?missing worker region}"
PROJECT="${CTYUN_WORKER_PROJECT_ID:-0}"
STATE_DIR="${CTYUN_WORKER_STATE_DIR:-/var/lib/catsco-worker/$NAME}"
[[ -z "${CTYUN_WORKER_STATE_ROOT:-}" ]] || STATE_DIR="${CTYUN_WORKER_STATE_ROOT%/}/$NAME"
REQUEST_MARKER="$STATE_DIR/conversion-requested"
ctyun() {
  local raw
  raw="$(timeout -s TERM -k 10 120s ctyun-cli "$@" --output json)" || { echo 'error: provider transport failed; reconcile before retry' >&2; return 1; }
  if [[ "$(jq -r '.statusCode' <<<"$raw")" != 800 ]]; then
    echo "error: provider $(jq -r '.errorCode // "unknown"' <<<"$raw"): $(jq -r '.message // "request failed"' <<<"$raw")" >&2
    return 3
  fi
  printf '%s' "$raw"
}
raw="$(ctyun ecs ListEcsInstances --regionID "$REGION" --projectID "$PROJECT" --instanceName "worker-$NAME" --pageNo 1 --pageSize 100)"
matches="$(jq --arg name "worker-$NAME" '[.returnObj.results[]? | select(.instanceName==$name)]' <<<"$raw")"
[[ "$(jq length <<<"$matches")" == 1 ]] || { echo 'error: exact managed instance not found' >&2; exit 1; }
ID="$(jq -r '.[0].instanceID' <<<"$matches")"
details() { ctyun ecs GetEcsInstanceDetails --regionID "$REGION" --instanceID "$ID" | jq -c '.returnObj'; }
instance="$(details)"
state() { jq -r '.instanceStatus // ""' <<<"$instance"; }
on_demand() { [[ "$(jq -r '.onDemand | tostring' <<<"$instance")" == true ]]; }
wait_running() {
  for _ in $(seq 1 60); do
    instance="$(details)"
    [[ "$(state)" == running || "$(state)" == active ]] && return 0
    sleep 2
  done
  echo 'error: timed out waiting for running' >&2; return 1
}
start_instance() {
  case "$(state)" in
    running|active) return 0;;
    shelve|shelved|stopped|shutoff) ctyun ecs StartEcsInstance --regionID "$REGION" --instanceID "$ID" >/dev/null;;
    *) echo 'error: instance cannot be started from current state' >&2; return 1;;
  esac
  wait_running
}
case "$ACTION" in
  cancel)
    on_demand && [[ ! -e "$REQUEST_MARKER" ]] || { echo 'error: conversion may have reached provider; cancellation requires reconciliation' >&2; exit 1; }
    jq -nc '{status:"conversion_cancelled"}'
    ;;
  suspend)
    on_demand || { echo 'error: refusing trial suspension of a monthly instance' >&2; exit 1; }
    if [[ "$(state)" != shelve && "$(state)" != shelved ]]; then
      # Ordinary stopped instances still incur compute charges. The provider
      # requires starting them before switching into its saving-stop mode.
      start_instance
      ctyun ecs ShelveEcsInstance --regionID "$REGION" --instanceID "$ID" >/dev/null
      for _ in $(seq 1 60); do
        instance="$(details)"
        [[ "$(state)" == shelve || "$(state)" == shelved ]] && break
        sleep 2
      done
    fi
    [[ "$(state)" == shelve || "$(state)" == shelved ]] || { echo 'error: saving stop not confirmed' >&2; exit 1; }
    jq -nc --arg id "$ID" '{status:"shelve",instance_id:$id,compute_suspended:true,residual_resources_billable:true}'
    ;;
  start)
    start_instance
    jq -nc --arg id "$ID" '{status:"running",instance_id:$id}'
    ;;
  convert)
    if on_demand; then
      [[ ! -e "$REQUEST_MARKER" ]] || { echo 'error: conversion result unknown; reconcile provider order before retry' >&2; exit 1; }
      start_instance
      (umask 077; mkdir -p "$STATE_DIR"; set -o noclobber; printf '%s\n' "$ID" > "$REQUEST_MARKER")
      result=0
      ctyun ecs ConvertEcsToCycle --regionID "$REGION" --instanceIDList "$ID" --cycleType MONTH --cycleCount 1 >/dev/null || result=$?
      if ((result != 0)); then
        # A provider rejection is definite; transport failures remain unknown.
        [[ "$result" != 3 ]] || rm -f "$REQUEST_MARKER"
        exit 1
      fi
      for _ in $(seq 1 60); do
        instance="$(details)"
        [[ "$(jq -r '.onDemand | tostring' <<<"$instance")" == false && -n "$(jq -r '.expiredTime // ""' <<<"$instance")" ]] && break
        sleep 2
      done
    fi
    [[ "$(jq -r '.onDemand | tostring' <<<"$instance")" == false && -n "$(jq -r '.expiredTime // ""' <<<"$instance")" ]] || { echo 'error: monthly conversion not confirmed' >&2; exit 1; }
    ctyun ecs UpdateEcsAutoRenewConfig --regionID "$REGION" --instanceIDList "$ID" --autoRenewStatus 0 >/dev/null
    renew="$(ctyun ecs GetEcsAutoRenewConfig --regionID "$REGION" --instanceID "$ID")"
    [[ "$(jq -r '.returnObj.autoRenewStatus' <<<"$renew")" == 0 ]] || { echo 'error: automatic renewal disable not confirmed' >&2; exit 1; }
    start_instance
    if [[ -n "${CATSCO_WORKER_DEPLOYMENT_JSON:-}" ]]; then
      export CATSCO_WORKER_DEPLOYMENT_JSON="$(jq -c '.env.CTYUN_WORKER_BILLING_MODE="month"' <<<"$CATSCO_WORKER_DEPLOYMENT_JSON")"
      worker_profile_load "$NAME" 1
    fi
    rm -f "$REQUEST_MARKER"
    jq -nc --arg id "$ID" --arg expiry "$(jq -r .expiredTime <<<"$instance")" '{status:"monthly",instance_id:$id,expires_at:$expiry,auto_renew_disabled:true}'
    ;;
esac
