#!/usr/bin/env bash
set -Eeuo pipefail

[[ "${CATSCO_ARTIFACT_GATEWAY_ENABLED:-0}" == "1" ]] \
  || { echo '{"ok":true,"status":"disabled"}'; exit 0; }

for command in ctyun-cli jq node; do
  command -v "$command" >/dev/null 2>&1 \
    || { echo "error: missing required command: $command" >&2; exit 2; }
done

REGION_ID="${CTYUN_WORKER_REGION_ID:-}"
NAT_GATEWAY_ID="${CTYUN_ARTIFACT_NAT_GATEWAY_ID:-}"
PUBLIC_IP="${CATSCO_ARTIFACT_GATEWAY_PUBLIC_IP:-${CTYUN_JUMP_IP:-}}"
PRIVATE_IP="${CTYUN_ARTIFACT_JUMP_PRIVATE_IP:-}"
SECURITY_GROUP_ID="${CTYUN_ARTIFACT_JUMP_SECURITY_GROUP_ID:-}"
EIP_ID="${CTYUN_ARTIFACT_EIP_ID:-}"
HTTPS_PORT="${CATSCO_ARTIFACT_GATEWAY_HTTPS_PORT:-19991}"
OPS_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

for value in "$REGION_ID" "$NAT_GATEWAY_ID" "$PUBLIC_IP" "$PRIVATE_IP" "$SECURITY_GROUP_ID"; do
  [[ -n "$value" ]] || { echo "error: Artifact gateway network environment is incomplete" >&2; exit 2; }
done
[[ "$HTTPS_PORT" =~ ^[1-9][0-9]{0,4}$ && "$HTTPS_PORT" -le 65535 ]] \
  || { echo "error: invalid Artifact gateway HTTPS port" >&2; exit 2; }

ctyun() {
  local raw status
  raw="$(timeout -s TERM -k 15 120s ctyun-cli "$@" --output json 2>&1)" || {
    echo "error: ctyun-cli failed: $*" >&2; echo "$raw" >&2; return 1
  }
  status="$(jq -r '(.statusCode // "") | tostring' <<<"$raw")"
  [[ "$status" == "800" ]] || {
    echo "error: Tianyi Cloud API failed: $(jq -r '.errorCode // ""' <<<"$raw") $(jq -r '.message // ""' <<<"$raw")" >&2
    return 1
  }
  printf '%s' "$raw"
}

gen_uuid() {
  if [[ -r /proc/sys/kernel/random/uuid ]]; then cat /proc/sys/kernel/random/uuid
  else printf 'catsco-%s-%s-%s\n' "$$" "$(date +%s)" "${RANDOM}${RANDOM}"; fi
}

dnat_json="$(ctyun nat DescribeInternetnatDnatEntries \
  --regionID "$REGION_ID" --natGatewayID "$NAT_GATEWAY_ID")"
conflict="$(jq -r --arg ip "$PUBLIC_IP" --argjson port "$HTTPS_PORT" \
  '.returnObj[]? | select(.externalIp == $ip and (.externalPort|tonumber) == $port) | @base64' \
  <<<"$dnat_json" | head -n1)"
dnat_status="created"
if [[ -n "$conflict" ]]; then
  decoded="$(printf '%s' "$conflict" | base64 -d)"
  existing_internal_ip="$(jq -r '.internalIp // ""' <<<"$decoded")"
  existing_internal_port="$(jq -r '.internalPort // 0' <<<"$decoded")"
  existing_protocol="$(jq -r '.protocol // ""' <<<"$decoded" | tr '[:upper:]' '[:lower:]')"
  if [[ "$existing_internal_ip" != "$PRIVATE_IP" || "$existing_internal_port" != "$HTTPS_PORT" || "$existing_protocol" != "tcp" ]]; then
    echo "error: public Artifact port already has a conflicting DNAT rule" >&2
    exit 1
  fi
  dnat_status="unchanged"
fi

if [[ "$dnat_status" == "created" ]]; then
  if [[ -z "$EIP_ID" ]]; then
    EIP_ID="$(jq -r --arg ip "$PUBLIC_IP" '.returnObj[]? | select(.externalIp == $ip) | .externalID // empty' <<<"$dnat_json" | head -n1)"
  fi
  [[ -n "$EIP_ID" ]] || { echo "error: Artifact gateway EIP ID cannot be resolved" >&2; exit 1; }
  ctyun nat CreateInternetnatDnatEntry \
    --regionID "$REGION_ID" --natGatewayID "$NAT_GATEWAY_ID" \
    --clientToken "$(gen_uuid)" --externalID "$EIP_ID" \
    --externalPort "$HTTPS_PORT" --internalPort "$HTTPS_PORT" \
    --internalIp "$PRIVATE_IP" --virtualMachineType 2 --protocol tcp \
    --description catsco-artifact-gateway >/dev/null
fi

dnat_ready=0
for _ in $(seq 1 30); do
  current_dnat="$(ctyun nat DescribeInternetnatDnatEntries \
    --regionID "$REGION_ID" --natGatewayID "$NAT_GATEWAY_ID")"
  if jq -e --arg ip "$PUBLIC_IP" --arg private "$PRIVATE_IP" --argjson port "$HTTPS_PORT" \
    '.returnObj[]? | select(.externalIp == $ip and (.externalPort|tonumber) == $port
      and .internalIp == $private and (.internalPort|tonumber) == $port
      and (.protocol|ascii_downcase) == "tcp")' <<<"$current_dnat" >/dev/null; then
    dnat_ready=1
    break
  fi
  sleep 2
done
[[ "$dnat_ready" == "1" ]] \
  || { echo "error: Artifact gateway DNAT did not become visible" >&2; exit 1; }

rules="$(ctyun vpc DescribeVpcSecurityGroupRules \
  --regionID "$REGION_ID" --securityGroupID "$SECURITY_GROUP_ID" --pageNo 1 --pageSize 50)"
rule_count="$(jq -r --arg port "$HTTPS_PORT" '[.returnObj.results[]? | select(
  (.direction|ascii_downcase) == "ingress" and (.action|ascii_downcase) == "accept"
  and (.protocol|ascii_downcase) == "tcp" and (.ethertype|ascii_downcase) == "ipv4"
  and .destCidrIp == "0.0.0.0/0" and (.range|tostring) == $port)] | length' <<<"$rules")"
security_status="unchanged"
if [[ "$rule_count" == "0" ]]; then
  rule_json="$(jq -cn --arg port "$HTTPS_PORT" '[{
    direction:"ingress",remoteType:0,action:"accept",priority:2,protocol:"TCP",
    ethertype:"IPv4",destCidrIp:"0.0.0.0/0",description:"catsco-artifact-gateway",range:$port
  }]')"
  ctyun vpc CreateVpcSecurityGroupIngress --regionID "$REGION_ID" \
    --securityGroupID "$SECURITY_GROUP_ID" --clientToken "$(gen_uuid)" \
    --securityGroupRules "$rule_json" >/dev/null
  security_status="created"
fi

export VOLC_ACCESSKEY="${CATSCO_ARTIFACT_DNS_ACCESS_KEY:-${VOLC_ACCESSKEY:-}}"
export VOLC_SECRETKEY="${CATSCO_ARTIFACT_DNS_SECRET_KEY:-${VOLC_SECRETKEY:-}}"
export CATSCO_ARTIFACT_GATEWAY_PUBLIC_IP="$PUBLIC_IP"
dns_json="$(node "$OPS_DIR/artifact-gateway-dns.mjs")"

jq -cn --arg dnat "$dnat_status" --arg security "$security_status" \
  --argjson dns "$dns_json" \
  '{ok:true,status:"ready",dnat:$dnat,security_group:$security,dns:$dns}'
