#!/usr/bin/env bash
# renew-worker.sh — extend or resume one monthly cloud worker after a paid
# CatsCo plan renewal.
#
# Active and provider-expired/freezing instances use ResubscribeEcsInstance.
# An unsubscribed instance has already left the renewable lifecycle in the
# current worker region and is rejected explicitly. This script never creates
# a replacement instance and always disables provider-side automatic renewal.
set -Eeuo pipefail

NAME=""
CYCLE_COUNT="${CTYUN_WORKER_CYCLE_COUNT:-1}"
DRY_RUN=0

usage() {
  sed -n '2,13p' "$0" | sed 's/^# \{0,1\}//'
}

while (($#)); do
  case "$1" in
    --name) NAME="${2:-}"; shift 2 ;;
    --cycle-count) CYCLE_COUNT="${2:-}"; shift 2 ;;
    --dry-run) DRY_RUN=1; shift ;;
    -h|--help) usage; exit 0 ;;
    *) echo "error: unknown argument: $1" >&2; usage >&2; exit 2 ;;
  esac
done

source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/worker-deployment-profile.sh"
worker_profile_load "$NAME"

REGION_ID="${CTYUN_WORKER_REGION_ID:-}"
PROJECT_ID="${CTYUN_WORKER_PROJECT_ID:-0}"
if [[ -z "$NAME" ]]; then
  echo "error: --name is required" >&2
  exit 2
fi
if [[ ! "$NAME" =~ ^[a-z0-9][a-z0-9_-]{1,63}$ ]]; then
  echo "error: --name must match ^[a-z0-9][a-z0-9_-]{1,63}\$" >&2
  exit 2
fi
if [[ -z "$REGION_ID" ]]; then
  echo "error: CTYUN_WORKER_REGION_ID is required" >&2
  exit 2
fi
if [[ ! "$CYCLE_COUNT" =~ ^[0-9]+$ || "$CYCLE_COUNT" -lt 1 || "$CYCLE_COUNT" -gt 60 ]]; then
  echo "error: --cycle-count must be 1-60" >&2
  exit 2
fi
for cmd in ctyun-cli jq timeout; do
  command -v "$cmd" >/dev/null 2>&1 || { echo "error: missing required command: $cmd" >&2; exit 2; }
done

ctyun() {
  local raw status
  raw="$(timeout -s TERM -k 15 120s ctyun-cli "$@" --output json 2>&1)" || {
    echo "error: ctyun-cli failed: $*" >&2
    echo "$raw" >&2
    return 1
  }
  status="$(jq -r '.statusCode // empty' <<<"$raw")"
  if [[ "$status" != "800" ]]; then
    echo "error: Tianyi Cloud API failed: $(jq -r '.errorCode // ""' <<<"$raw") $(jq -r '.message // ""' <<<"$raw")" >&2
    return 1
  fi
  printf '%s' "$raw"
}

INSTANCE_NAME="worker-${NAME}"
find_instance() {
  local resp
  resp="$(ctyun ecs ListEcsInstances --regionID "$REGION_ID" --projectID "$PROJECT_ID" \
    --instanceName "$INSTANCE_NAME" --pageNo 1 --pageSize 10)"
  jq -r --arg n "$INSTANCE_NAME" '.returnObj.results[]? | select(.instanceName == $n)' <<<"$resp" || true
}

instance="$(find_instance)"
[[ -n "$instance" ]] || { echo "error: instance $INSTANCE_NAME not found; renewal never creates a replacement" >&2; exit 1; }
instance_id="$(jq -r '.instanceID // ""' <<<"$instance")"
state="$(jq -r '.instanceStatus // .state // .status // ""' <<<"$instance" | tr '[:upper:]' '[:lower:]')"
pre_expires="$(jq -r '.expiredTime // ""' <<<"$instance")"
[[ -n "$instance_id" ]] || { echo "error: instance $INSTANCE_NAME has no instanceID" >&2; exit 1; }

case "$state" in
  running|active|stopped|shutoff|error|expired|freezing|frozen)
    operation="resubscribe"
    ;;
  unsubscribed)
    echo "error: instance $INSTANCE_NAME is unsubscribed; this region cannot resubscribe or recover it" >&2
    exit 1
    ;;
  released|deleted|bootdiskexpired|nobootdisk)
    echo "error: instance $INSTANCE_NAME is $state; it cannot be renewed" >&2
    exit 1
    ;;
  *)
    echo "error: instance $INSTANCE_NAME has unsupported provider state=$state" >&2
    exit 1
    ;;
esac

if [[ "$DRY_RUN" -eq 1 ]]; then
  printf '{"status":"dry-run","operation":"%s","instance_name":"%s","instance_id":"%s","cycle_count":%s}\n' \
    "$operation" "$INSTANCE_NAME" "$instance_id" "$CYCLE_COUNT"
  exit 0
fi

client_token="catsco-renew-${instance_id}-$(date +%s%N)"
resubscribed=0
for attempt in 1 2 3; do
  if ctyun ecs ResubscribeEcsInstance --regionID "$REGION_ID" --instanceID "$instance_id" \
    --clientToken "$client_token" --cycleType MONTH --cycleCount "$CYCLE_COUNT" >/dev/null; then
    resubscribed=1
    break
  fi
  [[ "$attempt" == "3" ]] || sleep 5
done
[[ "$resubscribed" -eq 1 ]] || { echo "error: failed to resubscribe instance_id=$instance_id" >&2; exit 1; }

auto_renew_attempted=0
auto_renew_disabled=0
confirmed=0
# 45 x 10s keeps the worst case inside the server's 10-minute script timeout so
# the unconfirmed-expiry fallback below is actually reachable before the
# caller kills the process.
for _ in $(seq 1 45); do
  instance="$(find_instance)"
  if [[ -n "$instance" ]]; then
    state="$(jq -r '.instanceStatus // .state // .status // ""' <<<"$instance" | tr '[:upper:]' '[:lower:]')"
    expires_at="$(jq -r '.expiredTime // ""' <<<"$instance")"
    case "$state" in
      running|active)
        if [[ "$auto_renew_attempted" -eq 0 ]]; then
          auto_renew_attempted=1
          for attempt in 1 2 3 4 5; do
            if ctyun ecs UpdateEcsAutoRenewConfig --regionID "$REGION_ID" \
              --instanceIDList "$instance_id" --autoRenewStatus 0 >/dev/null; then
              auto_renew_disabled=1
              break
            fi
            [[ "$attempt" == "5" ]] || sleep 3
          done
          if [[ "$auto_renew_disabled" -ne 1 ]]; then
            echo "warning: instance renewed but automatic renewal could not be disabled; operator reconciliation required (instance_id=$instance_id)" >&2
          fi
        fi
        # The renewal API can return before the provider publishes the new
        # expiredTime, so a read taken right after resubscribe still carries
        # the previous date. Wait until the date actually moves instead of
        # reporting (and persisting) a stale expiry, but never block on a
        # missing pre-renewal value.
        if [[ -z "$pre_expires" || ( -n "$expires_at" && "$expires_at" != "$pre_expires" ) ]]; then
          confirmed=1
          break
        fi
        ;;
      released|deleted)
        echo "error: instance $INSTANCE_NAME entered terminal state=$state during $operation" >&2
        exit 1
        ;;
    esac
  fi
  sleep 10
done
if [[ "$confirmed" -ne 1 ]]; then
  echo "warning: renewal completed but the provider has not published the new expiry yet (instance_id=$instance_id); reporting no expiry so callers keep the paid period" >&2
  expires_at=""
fi
jq -cn --arg status renewed --arg operation "$operation" --arg name "$INSTANCE_NAME" \
  --arg instanceID "$instance_id" --arg expiresAt "$expires_at" \
  --argjson autoRenewDisabled "$([[ "$auto_renew_disabled" -eq 1 ]] && echo true || echo false)" \
  '{status:$status,operation:$operation,instance_name:$name,instance_id:$instanceID,expires_at:$expiresAt,auto_renew_disabled:$autoRenewDisabled}'
exit 0
