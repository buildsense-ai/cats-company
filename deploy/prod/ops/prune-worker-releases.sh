#!/usr/bin/env bash
# prune-worker-releases.sh - bounded, fail-closed cleanup for worker artifacts.
# Default mode is dry-run. Pass --apply only from an operator-controlled timer.
set -Eeuo pipefail

KEEP_COUNT="${CATSCO_WORKER_RELEASE_KEEP_COUNT:-10}"
PREFIX="${CATSCO_WORKER_ARTIFACT_PREFIX:-update/worker}"
BUCKET="${CATSCO_WORKER_ARTIFACT_BUCKET:-}"
REGION="${CATSCO_WORKER_ARTIFACT_REGION:-cn-guangzhou}"
ENDPOINT="${CATSCO_WORKER_ARTIFACT_ENDPOINT:-https://tos-cn-guangzhou.volces.com}"
STATUS_SCRIPT="${CATSCO_WORKER_STATUS_SCRIPT:-}"
APPLY=0
for arg in "$@"; do
  case "$arg" in
    --apply) APPLY=1 ;;
    --dry-run) APPLY=0 ;;
    *) echo "usage: $0 [--dry-run|--apply]" >&2; exit 2 ;;
  esac
done

[[ "$KEEP_COUNT" =~ ^[1-9][0-9]*$ ]] || { echo "error: CATSCO_WORKER_RELEASE_KEEP_COUNT must be positive" >&2; exit 2; }
[[ -n "$BUCKET" ]] || { echo "error: CATSCO_WORKER_ARTIFACT_BUCKET is required" >&2; exit 2; }
for cmd in tos-fetch awk sort head sed timeout grep; do command -v "$cmd" >/dev/null 2>&1 || { echo "error: missing required command: $cmd" >&2; exit 2; }; done

list_args=(--endpoint "$ENDPOINT" --region "$REGION" --bucket "$BUCKET" --list-prefix "${PREFIX%/}/")
objects="$(timeout -s TERM -k 15 120s tos-fetch "${list_args[@]}")" || { echo "error: unable to list TOS objects; nothing removed" >&2; exit 1; }

# Release order is based on manifest LastModified, matching the control plane.
releases="$(printf '%s\n' "$objects" | awk -F '\t' -v p="${PREFIX%/}/" '
  index($1,p)==1 { rest=substr($1,length(p)+1); n=split(rest,a,"/"); if(n==2 && a[2]=="manifest.json") print a[1] "\t" $2 }
' | sort -t $'\t' -k2,2nr -k1,1r)"
protected=""
if [[ -n "$STATUS_SCRIPT" ]]; then
  status_out="$(timeout -s TERM -k 15 120s "$STATUS_SCRIPT")" || {
    [[ "$APPLY" -eq 0 ]] && echo "warning: status probe failed; dry-run only" >&2 || { echo "error: status probe failed; refusing deletion" >&2; exit 1; }
  }
  protected="$(printf '%s\n' "$status_out" | awk -F '\t' 'NF>=5 && $5!="" {print $5}' | sort -u)"
elif [[ "$APPLY" -eq 1 ]]; then
  echo "error: CATSCO_WORKER_STATUS_SCRIPT is required for --apply" >&2
  exit 2
fi

declare -A keep=()
index=0
while IFS=$'\t' read -r version published; do
  [[ -n "$version" ]] || continue
  index=$((index + 1))
  if [[ "$index" -le "$KEEP_COUNT" ]] || printf '%s\n' "$protected" | grep -Fxq "$version"; then
    keep["$version"]=1
  fi
done <<< "$releases"

printf '%s\n' "$objects" | awk -F '\t' -v p="${PREFIX%/}/" '{if(index($1,p)==1) print}' | while IFS=$'\t' read -r key modified; do
  rest="${key#${PREFIX%/}/}"; version="${rest%%/*}"
  [[ -n "$version" && "$version" != "$rest" ]] || continue
  [[ -z "${keep[$version]+x}" ]] || continue
  printf '%s\t%s\n' "$key" "$modified"
  if [[ "$APPLY" -eq 1 ]]; then
    timeout -s TERM -k 15 120s tos-fetch --endpoint "$ENDPOINT" --region "$REGION" --bucket "$BUCKET" --delete-key "$key" || {
      echo "error: deletion failed for $key; stopping" >&2; exit 1;
    }
  fi
done

if [[ "$APPLY" -eq 0 ]]; then
  echo "dry-run only; pass --apply after reviewing protected/current versions" >&2
fi
