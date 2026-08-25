#!/usr/bin/env bash
# renew-worker.sh — extend or recover one monthly cloud worker after a paid
# CatsCo plan renewal.
#
# Active instances use ResubscribeEcsInstance. Instances that Tianyi has moved
# to the unsubscribed retention state use RecoverEcsUnsubscribedInstance. The
# latter is still billable and must only be called after the CatsCo payment is
# durably fulfilled. This script never creates a replacement instance.
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
release_time="$(jq -r '.releaseTime // ""' <<<"$instance")"
[[ -n "$instance_id" ]] || { echo "error: instance $INSTANCE_NAME has no instanceID" >&2; exit 1; }

if [[ "$state" == "unsubscribed" ]]; then
  if [[ -n "$release_time" ]]; then
    release_epoch="$(date -d "$release_time" +%s 2>/dev/null || true)"
    if [[ -n "$release_epoch" && "$release_epoch" -le "$(date +%s)" ]]; then
      echo "error: instance $INSTANCE_NAME passed Tianyi releaseTime=$release_time; it cannot be recovered" >&2
      exit 1
    fi
  fi
  operation="recover"
else
  case "$state" in
    running|active|stopped|shutoff|error) operation="resubscribe" ;;
    released|deleted|bootdiskexpired|nobootdisk)
      echo "error: instance $INSTANCE_NAME is $state; it cannot be renewed" >&2
      exit 1
      ;;
    *)
      echo "error: instance $INSTANCE_NAME has unsupported provider state=$state" >&2
      exit 1
      ;;
  esac
fi

if [[ "$DRY_RUN" -eq 1 ]]; then
  printf '{"status":"dry-run","operation":"%s","instance_name":"%s","instance_id":"%s","cycle_count":%s}\n' \
    "$operation" "$INSTANCE_NAME" "$instance_id" "$CYCLE_COUNT"
  exit 0
fi

if [[ "$operation" == "recover" ]]; then
  ctyun ecs RecoverEcsUnsubscribedInstance --regionID "$REGION_ID" \
    --instanceIDList "[\"$instance_id\"]" --cycleType MONTH --cycleCount "$CYCLE_COUNT" >/dev/null
else
  ctyun ecs ResubscribeEcsInstance --regionID "$REGION_ID" --instanceID "$instance_id" \
    --clientToken "catsco-renew-${instance_id}-$(date +%s%N)" \
    --cycleType MONTH --cycleCount "$CYCLE_COUNT" >/dev/null
fi

for _ in $(seq 1 90); do
  instance="$(find_instance)"
  if [[ -n "$instance" ]]; then
    state="$(jq -r '.instanceStatus // .state // .status // ""' <<<"$instance" | tr '[:upper:]' '[:lower:]')"
    expires_at="$(jq -r '.expiredTime // ""' <<<"$instance")"
    case "$state" in
      running|active)
        jq -cn --arg status renewed --arg operation "$operation" --arg name "$INSTANCE_NAME" \
          --arg instanceID "$instance_id" --arg expiresAt "$expires_at" \
          '{status:$status,operation:$operation,instance_name:$name,instance_id:$instanceID,expires_at:$expiresAt}'
        exit 0
        ;;
      released|deleted)
        echo "error: instance $INSTANCE_NAME entered terminal state=$state during $operation" >&2
        exit 1
        ;;
    esac
  fi
  sleep 10
done
echo "error: timed out waiting for $operation of instance_id=$instance_id" >&2
exit 1
