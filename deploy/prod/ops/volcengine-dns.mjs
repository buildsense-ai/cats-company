#!/usr/bin/env node
import crypto from "node:crypto";
import net from "node:net";

export const VOLCENGINE_DNS_API_HOST = "dns.volcengineapi.com";
export const VOLCENGINE_DNS_API_VERSION = "2018-08-01";
export const VOLCENGINE_DNS_API_REGION = "cn-beijing";
export const VOLCENGINE_DNS_API_SERVICE = "dns";

export class VolcengineDnsError extends Error {
  constructor(message, options = {}) {
    super(message);
    this.name = "VolcengineDnsError";
    this.code = cleanText(options.code) || "volcengine_dns_error";
    this.action = cleanText(options.action);
    this.status = Number(options.status || 0);
  }
}

export function createVolcengineDnsClient(options = {}) {
  const env = options.env || process.env;
  const fetchImpl = options.fetchImpl || globalThis.fetch;
  const now = options.now || (() => new Date());
  const timeoutMs = positiveInteger(options.timeoutMs, 20_000);
  const accessKey = requiredSecret(env.VOLC_ACCESSKEY, "VOLC_ACCESSKEY");
  const secretKey = requiredSecret(env.VOLC_SECRETKEY, "VOLC_SECRETKEY");

  if (typeof fetchImpl !== "function") {
    throw new VolcengineDnsError("fetch is unavailable", {
      code: "dns_transport_unavailable"
    });
  }

  async function request(action, requestOptions = {}) {
    const method = cleanText(requestOptions.method || "GET").toUpperCase();
    const body = requestOptions.body === undefined || requestOptions.body === null
      ? null
      : requestOptions.body;
    const signed = signVolcengineDnsRequest({
      accessKey,
      secretKey,
      action,
      method,
      query: requestOptions.query || {},
      body,
      now: now()
    });

    let response;
    try {
      response = await fetchImpl(signed.url, {
        method,
        headers: signed.headers,
        body: signed.requestBody || undefined,
        signal: requestOptions.signal || AbortSignal.timeout(timeoutMs)
      });
    } catch (error) {
      throw new VolcengineDnsError(
        `${action} transport failed: ${messageOf(error)}`,
        { code: "dns_transport_failed", action }
      );
    }

    const text = await response.text();
    let payload = {};
    try {
      payload = text ? JSON.parse(text) : {};
    } catch {
      throw new VolcengineDnsError(
        `${action} returned invalid JSON with HTTP ${response.status}`,
        {
          code: "dns_invalid_response",
          action,
          status: response.status
        }
      );
    }

    const apiError = payload?.ResponseMetadata?.Error;
    if (!response.ok || apiError) {
      const code = cleanText(apiError?.Code) || `HTTP_${response.status}`;
      const detail = cleanText(apiError?.Message)
        || cleanText(response.statusText)
        || "unknown error";
      throw new VolcengineDnsError(`${action} failed: ${code}: ${detail}`, {
        code,
        action,
        status: response.status
      });
    }
    return payload?.Result || {};
  }

  async function listZones() {
    const zones = [];
    for (let pageNumber = 1; pageNumber <= 100; pageNumber += 1) {
      const result = await request("ListZones", {
        query: { PageNumber: pageNumber, PageSize: 100 }
      });
      const page = arrayValue(result.Zones);
      zones.push(...page);
      if (!hasNextPage(result, page.length, pageNumber, 100)) break;
    }
    return zones;
  }

  async function findZone(zoneName) {
    const normalized = normalizeDnsName(zoneName, "DNS zone");
    const zone = (await listZones()).find(item => {
      const candidate = cleanText(item?.ZoneName);
      return candidate
        && normalizeDnsName(candidate, "DNS zone result") === normalized;
    });
    if (!zone?.ZID) {
      throw new VolcengineDnsError(
        `DNS zone is not visible to the configured credentials: ${normalized}`,
        { code: "dns_zone_unavailable", action: "ListZones" }
      );
    }
    return zone;
  }

  async function listRecords(zoneId, filters = {}) {
    const records = [];
    const query = {
      ZID: normalizeZoneId(zoneId),
      PageSize: 100
    };
    const host = cleanText(filters.host);
    const type = cleanText(filters.type).toUpperCase();
    if (host) query.Host = host;
    if (type) query.Type = type;

    for (let pageNumber = 1; pageNumber <= 100; pageNumber += 1) {
      const result = await request("ListRecords", {
        query: { ...query, PageNumber: pageNumber }
      });
      const page = arrayValue(result.Records);
      records.push(...page);
      if (!hasNextPage(result, page.length, pageNumber, 100)) break;
    }

    return records.filter(record => {
      if (host && normalizeRecordHost(record?.Host) !== normalizeRecordHost(host)) return false;
      if (type && cleanText(record?.Type).toUpperCase() !== type) return false;
      return true;
    });
  }

  async function createRecord(input) {
    const body = {
      ZID: Number(normalizeZoneId(input.zoneId)),
      Host: requiredRecordHost(input.host),
      Type: requiredRecordType(input.type),
      Value: requiredRecordValue(input.value),
      Line: cleanText(input.line) || "default",
      TTL: positiveInteger(input.ttl, 600)
    };
    if (Number(input.weight) > 0) {
      body.Weight = positiveInteger(input.weight, 1);
    }
    const result = await request("CreateRecord", { method: "POST", body });
    const recordId = cleanText(String(result?.RecordID || ""));
    if (!recordId) {
      throw new VolcengineDnsError("CreateRecord succeeded without RecordID", {
        code: "dns_record_id_missing",
        action: "CreateRecord"
      });
    }
    return { ...result, RecordID: recordId };
  }

  async function updateRecord(input) {
    const body = {
      RecordID: requiredRecordId(input.recordId),
      Host: requiredRecordHost(input.host),
      Type: requiredRecordType(input.type),
      Value: requiredRecordValue(input.value),
      Line: cleanText(input.line) || "default",
      TTL: positiveInteger(input.ttl, 600)
    };
    if (Number(input.weight) > 0) {
      body.Weight = positiveInteger(input.weight, 1);
    }
    return request("UpdateRecord", { method: "POST", body });
  }

  async function deleteRecord(recordId) {
    try {
      await request("DeleteRecord", {
        method: "POST",
        body: { RecordID: requiredRecordId(recordId) }
      });
      return { deleted: true, already_absent: false };
    } catch (error) {
      if (error?.code === "ErrDBNotFound") {
        return { deleted: false, already_absent: true };
      }
      throw error;
    }
  }

  async function ensureARecord(input) {
    const zoneName = normalizeDnsName(input.zoneName, "DNS zone");
    const fqdn = normalizeDnsName(input.fqdn, "Artifact hostname");
    const value = validatePublicIPv4(input.value, "Artifact public IP");
    const ttl = positiveInteger(input.ttl, 600);
    const zone = await findZone(zoneName);
    const host = relativeHost(fqdn, zoneName);
    const records = await listRecords(zone.ZID, { host, type: "A" });

    if (records.length > 1) {
      throw new VolcengineDnsError(
        `multiple A records already exist for ${fqdn}; refusing to choose one automatically`,
        { code: "dns_a_record_conflict", action: "ListRecords" }
      );
    }

    if (!records.length) {
      const created = await createRecord({
        zoneId: zone.ZID,
        host,
        type: "A",
        value,
        line: "default",
        ttl
      });
      return {
        ok: true,
        action: "created",
        zone_name: zoneName,
        zone_id: String(zone.ZID),
        fqdn,
        host,
        value,
        record_id: created.RecordID
      };
    }

    const existing = records[0];
    const recordId = requiredRecordId(existing.RecordID);
    if (cleanText(existing.Value) === value) {
      return {
        ok: true,
        action: "reused",
        zone_name: zoneName,
        zone_id: String(zone.ZID),
        fqdn,
        host,
        value,
        record_id: recordId
      };
    }

    await updateRecord({
      recordId,
      host,
      type: "A",
      value,
      line: cleanText(existing.Line) || "default",
      ttl,
      weight: existing.Weight
    });
    return {
      ok: true,
      action: "updated",
      zone_name: zoneName,
      zone_id: String(zone.ZID),
      fqdn,
      host,
      value,
      previous_value: cleanText(existing.Value),
      record_id: recordId
    };
  }

  return {
    request,
    listZones,
    findZone,
    listRecords,
    createRecord,
    updateRecord,
    deleteRecord,
    ensureARecord
  };
}

export function signVolcengineDnsRequest(input) {
  const accessKey = requiredSecret(input.accessKey, "VOLC_ACCESSKEY");
  const secretKey = requiredSecret(input.secretKey, "VOLC_SECRETKEY");
  const action = requiredText(input.action, "DNS action");
  const method = cleanText(input.method || "GET").toUpperCase();
  const date = input.now instanceof Date ? input.now : new Date(input.now || Date.now());
  if (Number.isNaN(date.getTime())) throw new Error("request signing date is invalid");

  const xDate = date.toISOString().replace(/[:-]|\.\d{3}/g, "");
  const shortDate = xDate.slice(0, 8);
  const queryParameters = {
    Action: action,
    Version: VOLCENGINE_DNS_API_VERSION,
    ...(input.query || {})
  };
  const queryString = canonicalQuery(queryParameters);
  const requestBody = input.body === undefined || input.body === null
    ? ""
    : JSON.stringify(input.body);
  const bodyHash = sha256(requestBody);
  const signedHeaders = "host;x-content-sha256;x-date";
  const canonicalHeaders = [
    `host:${VOLCENGINE_DNS_API_HOST}`,
    `x-content-sha256:${bodyHash}`,
    `x-date:${xDate}`,
    ""
  ].join("\n");
  const canonicalRequest = [
    method,
    "/",
    queryString,
    canonicalHeaders,
    signedHeaders,
    bodyHash
  ].join("\n");
  const credentialScope =
    `${shortDate}/${VOLCENGINE_DNS_API_REGION}/${VOLCENGINE_DNS_API_SERVICE}/request`;
  const stringToSign = [
    "HMAC-SHA256",
    xDate,
    credentialScope,
    sha256(canonicalRequest)
  ].join("\n");
  const signature = crypto
    .createHmac("sha256", signingKey(secretKey, shortDate))
    .update(stringToSign)
    .digest("hex");
  const headers = {
    Authorization:
      `HMAC-SHA256 Credential=${accessKey}/${credentialScope}, `
      + `SignedHeaders=${signedHeaders}, Signature=${signature}`,
    Host: VOLCENGINE_DNS_API_HOST,
    "X-Content-Sha256": bodyHash,
    "X-Date": xDate
  };
  if (requestBody) headers["Content-Type"] = "application/json; charset=UTF-8";
  return {
    url: `https://${VOLCENGINE_DNS_API_HOST}/?${queryString}`,
    method,
    headers,
    requestBody
  };
}

export function relativeHost(fqdn, zoneName) {
  const normalizedFqdn = normalizeRecordDnsName(fqdn, "DNS record name");
  const normalizedZone = normalizeDnsName(zoneName, "DNS zone");
  if (normalizedFqdn === normalizedZone) return "@";
  const suffix = `.${normalizedZone}`;
  if (!normalizedFqdn.endsWith(suffix)) {
    throw new Error(`${normalizedFqdn} is not inside DNS zone ${normalizedZone}`);
  }
  return normalizedFqdn.slice(0, -suffix.length);
}

function canonicalQuery(parameters) {
  return Object.entries(parameters)
    .filter(([, value]) => value !== undefined && value !== null)
    .map(([key, value]) => [encodeQueryComponent(key), encodeQueryComponent(value)])
    .sort(([leftKey, leftValue], [rightKey, rightValue]) =>
      leftKey.localeCompare(rightKey) || leftValue.localeCompare(rightValue)
    )
    .map(([key, value]) => `${key}=${value}`)
    .join("&");
}

function encodeQueryComponent(value) {
  return encodeURIComponent(String(value)).replace(/[!'()*]/g, character =>
    `%${character.charCodeAt(0).toString(16).toUpperCase()}`
  );
}

function signingKey(secretKey, shortDate) {
  const dateKey = hmac(Buffer.from(secretKey, "utf8"), shortDate);
  const regionKey = hmac(dateKey, VOLCENGINE_DNS_API_REGION);
  const serviceKey = hmac(regionKey, VOLCENGINE_DNS_API_SERVICE);
  return hmac(serviceKey, "request");
}

function sha256(value) {
  return crypto.createHash("sha256").update(value).digest("hex");
}

function hmac(key, value) {
  return crypto.createHmac("sha256", key).update(value).digest();
}

function hasNextPage(result, pageLength, pageNumber, pageSize) {
  const total = Number(
    result?.TotalCount
    ?? result?.Total
    ?? result?.PageInfo?.TotalCount
    ?? 0
  );
  if (Number.isFinite(total) && total > 0) return pageNumber * pageSize < total;
  return pageLength >= pageSize;
}

function normalizeDnsName(value, label) {
  const text = requiredText(value, label).replace(/\.$/, "").toLowerCase();
  if (
    text.length > 253
    || !text.includes(".")
    || text.split(".").some(part =>
      !part
      || part.length > 63
      || !/^[a-z0-9](?:[a-z0-9-]*[a-z0-9])?$/.test(part)
    )
  ) {
    throw new Error(`${label} is not a valid DNS name`);
  }
  return text;
}

function normalizeRecordDnsName(value, label) {
  const text = requiredText(value, label).replace(/\.$/, "").toLowerCase();
  if (
    text.length > 253
    || !text.includes(".")
    || text.split(".").some(part =>
      !part
      || part.length > 63
      || !/^(?:_?[a-z0-9])(?:[a-z0-9_-]*[a-z0-9])?$/.test(part)
    )
  ) {
    throw new Error(`${label} is not a valid DNS record name`);
  }
  return text;
}

function normalizeRecordHost(value) {
  const text = cleanText(value).replace(/\.$/, "").toLowerCase();
  return text === "" ? "@" : text;
}

function requiredRecordHost(value) {
  const text = normalizeRecordHost(value);
  if (text === "@") return text;
  const parts = text.split(".");
  const wildcard = parts[0] === "*";
  const labels = wildcard ? parts.slice(1) : parts;
  if (
    text.length > 253
    || !labels.length
    || labels.some(part =>
      !part
      || part.length > 63
      || !/^(?:_?[a-z0-9])(?:[a-z0-9_-]*[a-z0-9])?$/.test(part)
    )
  ) {
    throw new Error("DNS record host is invalid");
  }
  return text;
}

function requiredRecordType(value) {
  const type = requiredText(value, "DNS record type").toUpperCase();
  if (!/^[A-Z][A-Z0-9]*$/.test(type)) throw new Error("DNS record type is invalid");
  return type;
}

function requiredRecordValue(value) {
  const text = requiredText(value, "DNS record value");
  if (text.length > 4096) throw new Error("DNS record value is too long");
  return text;
}

function requiredRecordId(value) {
  const text = requiredText(String(value || ""), "DNS RecordID");
  if (!/^[A-Za-z0-9_-]+$/.test(text)) throw new Error("DNS RecordID is invalid");
  return text;
}

function normalizeZoneId(value) {
  const text = requiredText(String(value || ""), "DNS zone ID");
  if (!/^[1-9]\d*$/.test(text)) throw new Error("DNS zone ID is invalid");
  return text;
}

function validatePublicIPv4(value, label) {
  const ip = cleanText(value);
  if (net.isIP(ip) !== 4) throw new Error(`${label} is not an IPv4 address`);
  const [a, b, c] = ip.split(".").map(Number);
  if (
    a === 0
    || a === 10
    || a === 127
    || (a === 100 && b >= 64 && b <= 127)
    || (a === 169 && b === 254)
    || (a === 172 && b >= 16 && b <= 31)
    || (a === 192 && b === 168)
    || (a === 192 && b === 0 && c === 0)
    || (a === 192 && b === 0 && c === 2)
    || (a === 198 && (b === 18 || b === 19))
    || (a === 198 && b === 51 && c === 100)
    || (a === 203 && b === 0 && c === 113)
    || a >= 224
  ) {
    throw new Error(`${label} is not a public IPv4 address`);
  }
  return ip;
}

function requiredSecret(value, name) {
  const text = cleanText(value);
  if (!text) {
    throw new VolcengineDnsError(`missing required environment variable: ${name}`, {
      code: "dns_credentials_unavailable"
    });
  }
  return text;
}

function requiredText(value, label) {
  const text = cleanText(value);
  if (!text) throw new Error(`${label} is required`);
  return text;
}

function positiveInteger(value, fallback) {
  const candidate = value === undefined || value === null || value === ""
    ? fallback
    : Number(value);
  if (!Number.isInteger(candidate) || candidate < 1) {
    throw new Error("expected a positive integer");
  }
  return candidate;
}

function arrayValue(value) {
  return Array.isArray(value) ? value : [];
}

function cleanText(value) {
  return typeof value === "string" ? value.trim() : "";
}

function messageOf(error) {
  return error instanceof Error ? error.message : String(error);
}
