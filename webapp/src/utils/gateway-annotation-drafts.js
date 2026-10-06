import {
  readStorageValue,
  writeStorageValue,
  removeStorageValue,
} from './storage-access';
import { GATEWAY_ANNOTATIONS_CONTRACT as SHARED_CONTRACT, normalizeGatewayAnnotations } from '../gateway-annotations';

// Keep the contract constant sourced from the frozen shared module so the
// client cannot drift from the bridge contract both sides validate.

// Gateway annotation composer drafts. A draft is the pending annotation list
// attached to one conversation × agent × gateway application. It lives only in
// session storage scoped by user id (same policy as the shared composer draft
// store) and is cleared on send and on logout, so an unsent review is never
// carried into another conversation, agent, or application, and never survives
// login boundaries.
export const GATEWAY_ANNOTATION_DRAFTS_STORAGE_PREFIX = 'catsco_gateway_annotation_drafts:v1:';

// The contract value must serialize into the same bounded shape the server
// canonicalizes (see server/gateway_annotations.go). Keep this in lockstep:
// any change here that widens a bound breaks server ingestion.
export const GATEWAY_ANNOTATIONS_CONTRACT = 'catsco.gateway-annotations.v1';
export const GATEWAY_ANNOTATION_DRAFT_MAX_ANNOTATIONS = 20;
export const GATEWAY_ANNOTATION_DRAFT_MAX_BYTES = 16 * 1024;
export const GATEWAY_ANNOTATION_BODY_MAX_RUNES = 2000;
export const GATEWAY_ANNOTATION_LABEL_MAX_RUNES = 256;

function storageTarget(storage) {
  if (storage && typeof storage === 'object') return storage;
  const type = typeof storage === 'string' && storage ? storage : 'sessionStorage';
  try {
    const resolved = typeof globalThis !== 'undefined' ? globalThis[type] : null;
    return resolved && typeof resolved.getItem === 'function' ? resolved : null;
  } catch {
    return null;
  }
}

function storageKey(userID) {
  const normalized = String(userID || '').trim();
  return normalized ? `${GATEWAY_ANNOTATION_DRAFTS_STORAGE_PREFIX}${normalized}` : '';
}

function readDraftMap(userID, storage) {
  const key = storageKey(userID);
  if (!key) return null;
  const target = storageTarget(storage);
  if (!target) return null;
  try {
    const serialized = readStorageValue(key, target);
    if (!serialized) return null;
    const stored = JSON.parse(serialized);
    return stored && typeof stored === 'object' && !Array.isArray(stored)
      ? stored
      : null;
  } catch {
    return null;
  }
}

function writeDraftMap(userID, value, storage) {
  const key = storageKey(userID);
  if (!key) return;
  const target = storageTarget(storage);
  if (!target) return;
  try {
    if (!value || typeof value !== 'object' || Object.keys(value).length === 0) {
      removeStorageValue(key, target);
      return;
    }
    writeStorageValue(key, JSON.stringify(value), target);
  } catch {
    // Serialization or quota failure keeps the in-memory state but not the
    // persisted copy; the next successful write restores persistence.
  }
}

function draftBucketKey(topicId, agentUid, appId) {
  const normalizedTopic = String(topicId || '').trim();
  const normalizedAgent = Number(agentUid) > 0 ? String(Number(agentUid)) : '';
  const normalizedApp = String(appId || '').trim();
  return normalizedTopic && normalizedAgent && normalizedApp
    ? `${normalizedTopic}|${normalizedAgent}|${normalizedApp}`
    : '';
}

function validDraftAnnotation(value) {
  if (!value || typeof value !== 'object' || Array.isArray(value)) return null;
  const id = typeof value.id === 'string' ? value.id : '';
  const kind = value.kind === 'element' || value.kind === 'text' || value.kind === 'region'
    ? value.kind
    : '';
  const body = typeof value.body === 'string' ? value.body : '';
  if (!id || !kind || !body) return null;
  const label = typeof value.label === 'string' ? value.label : '';
  const target = value.target && typeof value.target === 'object' && !Array.isArray(value.target)
    ? value.target
    : {};
  const normalized = {
    id,
    kind,
    body,
    target: { ...target },
  };
  if (label) normalized.label = label;
  return normalized;
}

function draftValueFor(bucket, stored) {
  // The certificate proves the stored rows belong to the bucket identity;
  // a mismatch means the draft must never be resurrected for a different
  // topic, agent, or application than the key it was written under.
  const certificate = stored?.[`${bucket}.meta`];
  if (!certificate || typeof certificate !== 'object' || Array.isArray(certificate)) return null;
  const normalizedAgent = Number(certificate.agent_uid);
  if (normalizedAgent <= 0
    || certificate.app_id !== bucket.split('|')[2]
    || certificate.topic_id !== bucket.split('|')[0]) return null;
  const annotations = Array.isArray(stored[bucket])
    ? stored[bucket].map(validDraftAnnotation).filter(Boolean)
    : [];
  return annotations;
}

function withCertificate(stored, bucket, annotations) {
  const next = { ...stored };
  next[bucket] = annotations;
  next[`${bucket}.meta`] = {
    ...(next[`${bucket}.meta`] || {}),
    topic_id: bucket.split('|')[0],
    agent_uid: Number(bucket.split('|')[1]),
    app_id: bucket.split('|')[2],
    updated_at: Date.now(),
  };
  return next;
}

export function readGatewayAnnotationDrafts(userID, topicId, agentUid, appId, storage) {
  const bucket = draftBucketKey(topicId, agentUid, appId);
  if (!bucket) return [];
  const stored = readDraftMap(userID, storage);
  if (!stored) return [];
  return draftValueFor(bucket, stored) || [];
}

export function writeGatewayAnnotationDrafts(userID, topicId, agentUid, appId, annotations, storage) {
  const bucket = draftBucketKey(topicId, agentUid, appId);
  if (!bucket) return;
  if (!Array.isArray(annotations) || annotations.length === 0) {
    const stored = readDraftMap(userID, storage);
    if (stored && (bucket in stored || `${bucket}.meta` in stored)) {
      delete stored[bucket];
      delete stored[`${bucket}.meta`];
      writeDraftMap(userID, stored, storage);
    }
    return;
  }
  const bounded = annotations
    .slice(0, GATEWAY_ANNOTATION_DRAFT_MAX_ANNOTATIONS)
    .map(validDraftAnnotation)
    .filter(Boolean);
  if (bounded.length === 0) {
    writeGatewayAnnotationDrafts(userID, topicId, agentUid, appId, [], storage);
    return;
  }
  const serialized = JSON.stringify(bounded);
  if (serialized.length > GATEWAY_ANNOTATION_DRAFT_MAX_BYTES) {
    // A draft over the ingestion limit can never be sent; keep trimming until
    // it fits, so persistence never holds an unsentable payload.
    if (bounded.length > 1) {
      writeGatewayAnnotationDrafts(userID, topicId, agentUid, appId, bounded.slice(0, bounded.length - 1), storage);
      return;
    }
    writeGatewayAnnotationDrafts(userID, topicId, agentUid, appId, [], storage);
    return;
  }
  const stored = readDraftMap(userID, storage) || {};
  writeDraftMap(userID, withCertificate(stored, bucket, bounded), storage);
}

// Build (and validate) the gateway_annotations metadata value for one send.
// Returns null when there is nothing to attach or the draft set has become
// non-canonical — the caller must then refuse the send instead of silently
// dropping user-written comments.
export function buildGatewayAnnotationsMetadata(context, drafts, page) {
  if (!context || !Array.isArray(drafts) || drafts.length === 0) return null;
  const agentUid = Number(context.agentUid);
  const appId = String(context.appId || '');
  if (agentUid <= 0 || !appId) return null;
  const path = String(page?.path || '');
  const revision = typeof page?.revision === 'string' && page.revision ? page.revision : '';
  if (!path.startsWith('/')) return null;
  const metadata = normalizeGatewayAnnotations({
    contract_version: SHARED_CONTRACT,
    agent_uid: agentUid,
    app_id: appId,
    page: revision ? { path, revision } : { path },
    annotations: drafts.map((draft) => ({
      id: draft.id,
      kind: draft.kind,
      label: draft.label || '',
      body: draft.body,
      target: draft.target,
    })),
  });
  return metadata && Array.isArray(metadata.annotations) && metadata.annotations.length > 0
    ? metadata
    : null;
}

export function gatewayAnnotationDraftKey(topicId, agentUid, appId) {
  return draftBucketKey(topicId, agentUid, appId);
}
