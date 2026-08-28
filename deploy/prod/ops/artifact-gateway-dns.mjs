#!/usr/bin/env node
import net from "node:net";
import path from "node:path";
import process from "node:process";
import { pathToFileURL } from "node:url";
import { createVolcengineDnsClient } from "./volcengine-dns.mjs";

export async function ensureGatewayWildcard(options = {}) {
  const env = options.env || process.env;
  const zoneName = requiredText(env.CATSCO_ARTIFACT_DNS_ZONE || "catsco.fun", "DNS zone");
  const hostSuffix = normalizeHostname(
    env.CATSCO_ARTIFACT_HOST_SUFFIX || "artifacts.catsco.fun"
  );
  const publicIp = requiredIPv4(
    env.CATSCO_ARTIFACT_GATEWAY_PUBLIC_IP || env.CTYUN_JUMP_IP,
    "Artifact gateway public IP"
  );
  const client = options.client || createVolcengineDnsClient({ env });
  const zone = await client.findZone(zoneName);
  const relativeSuffix = relativeName(hostSuffix, zoneName);
  const host = `*.${relativeSuffix}`;
  const records = await client.listRecords(zone.ZID, { host, type: "A" });
  if (records.length > 1) throw new Error(`multiple wildcard A records exist for *.${hostSuffix}`);
  if (!records.length) {
    const created = await client.createRecord({
      zoneId: zone.ZID,
      host,
      type: "A",
      value: publicIp,
      line: "default",
      ttl: 600
    });
    return { ok: true, status: "created", host: `*.${hostSuffix}`, value: publicIp, record_id: created.RecordID };
  }
  const current = records[0];
  if (String(current.Value || "").trim() === publicIp) {
    return { ok: true, status: "unchanged", host: `*.${hostSuffix}`, value: publicIp, record_id: String(current.RecordID) };
  }
  await client.updateRecord({
    recordId: current.RecordID,
    host,
    type: "A",
    value: publicIp,
    line: current.Line || "default",
    ttl: Number(current.TTL || 600),
    weight: current.Weight
  });
  return {
    ok: true,
    status: "updated",
    host: `*.${hostSuffix}`,
    value: publicIp,
    previous_value: String(current.Value || ""),
    record_id: String(current.RecordID)
  };
}

function relativeName(hostname, zone) {
  const suffix = `.${zone.toLowerCase()}`;
  const normalized = hostname.toLowerCase();
  if (!normalized.endsWith(suffix)) throw new Error("Artifact host suffix is outside the DNS zone");
  const relative = normalized.slice(0, -suffix.length);
  if (!relative) throw new Error("Artifact host suffix cannot be the DNS zone apex");
  return relative;
}

function normalizeHostname(value) {
  const text = requiredText(value, "Artifact host suffix").replace(/\.$/, "").toLowerCase();
  if (!text.includes(".") || text.split(".").some(part =>
    !/^[a-z0-9](?:[a-z0-9-]*[a-z0-9])?$/.test(part)
  )) throw new Error("Artifact host suffix is invalid");
  return text;
}

function requiredText(value, label) {
  const text = String(value || "").trim();
  if (!text) throw new Error(`${label} is required`);
  return text;
}

function requiredIPv4(value, label) {
  const text = requiredText(value, label);
  if (net.isIP(text) !== 4) throw new Error(`${label} must be an IPv4 address`);
  return text;
}

if (process.argv[1] && import.meta.url === pathToFileURL(path.resolve(process.argv[1])).href) {
  ensureGatewayWildcard().then(result => console.log(JSON.stringify(result))).catch(error => {
    console.error(JSON.stringify({ ok: false, error: error instanceof Error ? error.message : String(error) }));
    process.exit(1);
  });
}
