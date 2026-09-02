#!/usr/bin/env node
import { createVolcengineDnsClient } from "./volcengine-dns.mjs";

const env = {
  ...process.env,
  VOLC_ACCESSKEY: process.env.VOLC_ACCESSKEY || process.env.CATSCO_ARTIFACT_DNS_ACCESS_KEY,
  VOLC_SECRETKEY: process.env.VOLC_SECRETKEY || process.env.CATSCO_ARTIFACT_DNS_SECRET_KEY
};

const client = createVolcengineDnsClient({ env });
const result = await client.ensureARecord({
  zoneName: process.env.WECOM_DNS_ZONE || "catsco.cn",
  fqdn: process.env.WECOM_DNS_NAME || "wecom.catsco.cn",
  value: process.env.WECOM_GATEWAY_PUBLIC_IP || "121.11.233.2",
  ttl: Number(process.env.WECOM_DNS_TTL || 600)
});

const zone = await client.findZone(result.zone_name);
const records = await client.listRecords(zone.ZID, { host: result.host, type: "A" });
const record = records.find(item => String(item.RecordID || "") === result.record_id);

console.log(JSON.stringify({
  ...result,
  record_status: String(record?.Status || record?.Enable || "unknown")
}));
