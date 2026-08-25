#!/usr/bin/env bash
# list-worker-releases.sh - list published CatsCo application releases from
# the private TOS bucket. Output contract:
# version<TAB>publishedUnixTime
set -Eeuo pipefail

for cmd in tos-fetch awk timeout; do
  command -v "$cmd" >/dev/null 2>&1 || { echo "error: missing required command: $cmd" >&2; exit 2; }
done

ARTIFACT_BUCKET="${CATSCO_WORKER_ARTIFACT_BUCKET:-}"
[[ -n "$ARTIFACT_BUCKET" ]] || { echo "error: CATSCO_WORKER_ARTIFACT_BUCKET must name the dedicated worker artifact bucket" >&2; exit 2; }
ARTIFACT_PREFIX="${CATSCO_WORKER_ARTIFACT_PREFIX:-update/worker}"
ARTIFACT_PREFIX="${ARTIFACT_PREFIX#/}"
ARTIFACT_PREFIX="${ARTIFACT_PREFIX%/}"
ARTIFACT_REGION="${CATSCO_WORKER_ARTIFACT_REGION:-cn-guangzhou}"
ARTIFACT_ENDPOINT="${CATSCO_WORKER_ARTIFACT_ENDPOINT:-https://tos-cn-guangzhou.volces.com}"

timeout -s TERM -k 15 120s tos-fetch \
  --endpoint "$ARTIFACT_ENDPOINT" \
  --region "$ARTIFACT_REGION" \
  --bucket "$ARTIFACT_BUCKET" \
  --list-prefix "$ARTIFACT_PREFIX/" \
  | awk -F '\t' -v prefix="$ARTIFACT_PREFIX/" '
      index($1, prefix) == 1 {
        path = substr($1, length(prefix) + 1)
        count = split(path, parts, "/")
        version = parts[1]
        if (count == 2 && parts[2] == "manifest.json" && length(version) <= 64 &&
            version ~ /^[0-9A-Za-z][0-9A-Za-z._+-]*$/) {
          print version "\t" $2
        }
      }
    '
