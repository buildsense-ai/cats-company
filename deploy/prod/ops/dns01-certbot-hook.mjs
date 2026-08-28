#!/usr/bin/env node
import crypto from "node:crypto";
import dns from "node:dns/promises";
import fs from "node:fs";
import path from "node:path";
import process from "node:process";
import { pathToFileURL } from "node:url";
import { createVolcengineDnsClient, relativeHost } from "./volcengine-dns.mjs";

export const DNS01_STATE_VERSION = "cloud-html-artifact.dns01-state.v1";
export const DEFAULT_DNS01_STATE_DIR =
  "/var/lib/catsco-cloud-html-artifact/dns01-state";

export async function authorizeDns01Challenge(options = {}) {
  const env = options.env || process.env;
  const zoneName = requiredEnv(env, "CATSCO_ARTIFACT_DNS_ZONE");
  const domain = normalizeDomain(requiredEnv(env, "CERTBOT_DOMAIN"));
  const validation = requiredEnv(env, "CERTBOT_VALIDATION");
  const challengeFqdn = `_acme-challenge.${domain}`;
  const client = options.client || createVolcengineDnsClient({
    env,
    fetchImpl: options.fetchImpl,
    now: options.now
  });
  const zone = await client.findZone(zoneName);
  const record = await client.createRecord({
    zoneId: zone.ZID,
    host: relativeHost(challengeFqdn, zoneName),
    type: "TXT",
    value: validation,
    line: "default",
    ttl: positiveInteger(env.CATSCO_ARTIFACT_DNS_TTL, 600)
  });
  const state = {
    contract_version: DNS01_STATE_VERSION,
    record_id: String(record.RecordID),
    zone_name: zoneName,
    domain,
    challenge_fqdn: challengeFqdn,
    validation_sha256: sha256(validation),
    created_at: new Date().toISOString()
  };
  let target = "";
  try {
    target = writeChallengeState({ env, domain, validation, state });
    await waitForTxt(challengeFqdn, validation, {
      env,
      resolver: options.resolver,
      sleep: options.sleep,
      nowMs: options.nowMs
    });
  } catch (error) {
    await client.deleteRecord(record.RecordID).catch(() => {});
    if (target) fs.rmSync(target, { force: true });
    throw error;
  }

  return {
    ok: true,
    action: "auth",
    domain,
    challenge_fqdn: challengeFqdn,
    record_id: String(record.RecordID),
    propagated: true
  };
}

export async function cleanupDns01Challenge(options = {}) {
  const env = options.env || process.env;
  const domain = normalizeDomain(requiredEnv(env, "CERTBOT_DOMAIN"));
  const validation = requiredEnv(env, "CERTBOT_VALIDATION");
  const { target, state } = readChallengeState({ env, domain, validation });
  if (!state) {
    return { ok: true, action: "cleanup", domain, status: "no-state" };
  }
  if (
    state.contract_version !== DNS01_STATE_VERSION
    || state.domain !== domain
    || state.validation_sha256 !== sha256(validation)
    || !cleanText(state.record_id)
  ) {
    throw new Error("DNS-01 challenge state does not match the current Certbot challenge");
  }
  const client = options.client || createVolcengineDnsClient({
    env,
    fetchImpl: options.fetchImpl,
    now: options.now
  });
  const deleted = await client.deleteRecord(state.record_id);
  fs.rmSync(target, { force: true });
  return {
    ok: true,
    action: "cleanup",
    domain,
    record_id: state.record_id,
    deleted: deleted.deleted,
    already_absent: deleted.already_absent
  };
}

export async function inspectDnsCredentials(options = {}) {
  const env = options.env || process.env;
  const zoneName = requiredEnv(env, "CATSCO_ARTIFACT_DNS_ZONE");
  const client = options.client || createVolcengineDnsClient({
    env,
    fetchImpl: options.fetchImpl,
    now: options.now
  });
  const zone = await client.findZone(zoneName);
  return {
    ok: true,
    action: "inspect",
    zone: zoneName,
    zone_id: String(zone.ZID)
  };
}

export async function selfTestDnsCredentials(options = {}) {
  const env = options.env || process.env;
  const zoneName = requiredEnv(env, "CATSCO_ARTIFACT_DNS_ZONE");
  const client = options.client || createVolcengineDnsClient({
    env,
    fetchImpl: options.fetchImpl,
    now: options.now
  });
  const zone = await client.findZone(zoneName);
  const suffix = `${Date.now()}-${crypto.randomBytes(4).toString("hex")}`;
  const host = `_artifact-hook-self-test-${suffix}`;
  const value = `catsco-${crypto.randomBytes(12).toString("base64url")}`;
  let recordId = "";
  try {
    const record = await client.createRecord({
      zoneId: zone.ZID,
      host,
      type: "TXT",
      value,
      line: "default",
      ttl: positiveInteger(env.CATSCO_ARTIFACT_DNS_TTL, 600)
    });
    recordId = String(record.RecordID);
  } finally {
    if (recordId) await client.deleteRecord(recordId);
  }
  return {
    ok: true,
    action: "self-test",
    zone: zoneName,
    create_txt: Boolean(recordId),
    delete_txt: Boolean(recordId)
  };
}

export async function waitForTxt(fqdn, expectedValue, options = {}) {
  const env = options.env || process.env;
  const timeoutMs = positiveInteger(env.CATSCO_ARTIFACT_DNS_TIMEOUT_MS, 240_000);
  const pollMs = positiveInteger(env.CATSCO_ARTIFACT_DNS_POLL_MS, 5_000);
  const resolver = options.resolver || publicTxtValues;
  const sleep = options.sleep || delay;
  const nowMs = options.nowMs || Date.now;
  const deadline = nowMs() + timeoutMs;
  let lastValues = [];
  while (nowMs() < deadline) {
    lastValues = await resolver(fqdn);
    if (lastValues.includes(expectedValue)) return lastValues;
    await sleep(pollMs);
  }
  throw new Error(
    `TXT propagation timed out for ${fqdn}; observed ${lastValues.length} value(s)`
  );
}

export function challengeStatePath(options = {}) {
  const env = options.env || process.env;
  const directory = path.resolve(
    cleanText(options.stateDir)
    || cleanText(env.CATSCO_ARTIFACT_DNS_STATE_DIR)
    || DEFAULT_DNS01_STATE_DIR
  );
  const domain = normalizeDomain(options.domain);
  const validation = requiredText(options.validation, "CERTBOT_VALIDATION");
  return path.join(directory, `${sha256(`${domain}\n${validation}`)}.json`);
}

function writeChallengeState({ env, domain, validation, state }) {
  const target = challengeStatePath({ env, domain, validation });
  const directory = path.dirname(target);
  fs.mkdirSync(directory, { recursive: true, mode: 0o700 });
  fs.chmodSync(directory, 0o700);
  const temporary = `${target}.${process.pid}.${crypto.randomBytes(4).toString("hex")}.tmp`;
  fs.writeFileSync(temporary, `${JSON.stringify(state, null, 2)}\n`, {
    encoding: "utf8",
    mode: 0o600,
    flag: "wx"
  });
  fs.renameSync(temporary, target);
  fs.chmodSync(target, 0o600);
  return target;
}

function readChallengeState({ env, domain, validation }) {
  const target = challengeStatePath({ env, domain, validation });
  if (!fs.existsSync(target)) return { target, state: null };
  const state = JSON.parse(fs.readFileSync(target, "utf8"));
  return { target, state };
}

async function publicTxtValues(fqdn) {
  try {
    const records = await dns.resolveTxt(fqdn);
    return records.map(record => record.join(""));
  } catch (error) {
    if (["ENODATA", "ENOTFOUND", "ESERVFAIL", "ETIMEOUT"].includes(error?.code)) {
      return [];
    }
    throw error;
  }
}

async function runCli() {
  const command = process.argv[2];
  let result;
  if (command === "auth") result = await authorizeDns01Challenge();
  else if (command === "cleanup") result = await cleanupDns01Challenge();
  else if (command === "inspect") result = await inspectDnsCredentials();
  else if (command === "self-test") result = await selfTestDnsCredentials();
  else throw new Error("usage: dns01-certbot-hook.mjs <inspect|self-test|auth|cleanup>");
  console.log(JSON.stringify(result));
}

function isMainModule() {
  return process.argv[1]
    && import.meta.url === pathToFileURL(path.resolve(process.argv[1])).href;
}

function normalizeDomain(value) {
  const text = requiredText(value, "CERTBOT_DOMAIN")
    .replace(/\.$/, "")
    .replace(/^\*\./, "")
    .toLowerCase();
  if (
    text.length > 253
    || !text.includes(".")
    || text.split(".").some(part =>
      !part
      || part.length > 63
      || !/^[a-z0-9](?:[a-z0-9-]*[a-z0-9])?$/.test(part)
    )
  ) {
    throw new Error("CERTBOT_DOMAIN is not a valid DNS name");
  }
  return text;
}

function requiredEnv(env, name) {
  const value = cleanText(env[name]);
  if (!value) throw new Error(`missing required environment variable: ${name}`);
  return value;
}

function requiredText(value, label) {
  const text = cleanText(value);
  if (!text) throw new Error(`${label} is required`);
  return text;
}

function positiveInteger(value, fallback) {
  const number = Number(value || fallback);
  if (!Number.isInteger(number) || number < 1) throw new Error("expected a positive integer");
  return number;
}

function sha256(value) {
  return crypto.createHash("sha256").update(value).digest("hex");
}

function delay(milliseconds) {
  return new Promise(resolve => setTimeout(resolve, milliseconds));
}

function cleanText(value) {
  return typeof value === "string" ? value.trim() : "";
}

function messageOf(error) {
  return error instanceof Error ? error.message : String(error);
}

if (isMainModule()) {
  runCli().catch(error => {
    console.error(JSON.stringify({
      ok: false,
      command: process.argv[2] || "",
      error: messageOf(error),
      code: cleanText(error?.code) || "dns01_hook_failed"
    }));
    process.exitCode = 1;
  });
}
