import {
  getStorage,
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
  // Boolean result so a blocked/quota-full storage surfaces as a refused
  // write, never as a silent success the UI would present as saved.
  const key = storageKey(userID);
  if (!key) return false;
  const target = storageTarget(storage);
  if (!target) return false;
  try {
    if (!value || typeof value !== 'object' || Object.keys(value).length === 0) {
      removeStorageValue(key, target);
      return true;
    }
    return writeStorageValue(key, JSON.stringify(value), target) ? true : false;
  } catch {
    // Serialization or quota failure: refuse the write so the caller can
    // tell the user, instead of reporting a persistence that never happened.
    return false;
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
  // The page the annotation was captured on is part of the annotation:
  // once a bucket captures against one page/revision, later framework loads
  // or SPA navigations must not silently re-anchor the target to the new
  // document. Stored here so a repoen can surface the original capture page
  // instead of replacing it with the current viewer page.
  const page = value.page && typeof value.page === 'object' && !Array.isArray(value.page)
    ? value.page
    : null;
  const pagePath = page && typeof page.path === 'string' && page.path.length > 0
    && page.path.length <= 1024 && page.path.startsWith('/')
    ? page.path
    : '';
  if (pagePath) {
    const capturePage = { path: pagePath };
    if (typeof page.revision === 'string' && page.revision
      && page.revision.length <= 128 && !/[\u0000-\u001f\u007f]/.test(page.revision)) {
      capturePage.revision = page.revision;
    }
    normalized.page = capturePage;
  }
  return normalized;
}

function draftValueFor(bucket, stored) {
  // The certificate proves the stored rows belong to the bucket identity;
  // a mismatch means the draft must never be resurrected for a different
  // topic, agent, or application than the key it was written under. The
  // agent must equal the bucket's agent exactly — a merely positive value
  // would let a bucket written for agent A resurrect under agent B.
  const certificate = stored?.[`${bucket}.meta`];
  if (!certificate || typeof certificate !== 'object' || Array.isArray(certificate)) return null;
  const [bucketTopic, bucketAgent, bucketApp] = bucket.split('|');
  const normalizedAgent = Number(certificate.agent_uid);
  if (String(normalizedAgent) !== bucketAgent
    || certificate.app_id !== bucketApp
    || certificate.topic_id !== bucketTopic) return null;
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

// Writes (or clears) one draft bucket. Returns true when the given rows are
// now the persisted value, false when the write was refused — the caller is
// responsible for telling the user. A refused write never destroys the last
// successfully persisted rows: adding one oversized comment must not delete
// the whole previously saved draft.
export function writeGatewayAnnotationDrafts(userID, topicId, agentUid, appId, annotations, storage) {
  const bucket = draftBucketKey(topicId, agentUid, appId);
  if (!bucket) return false;
  if (!Array.isArray(annotations) || annotations.length === 0) {
    const stored = readDraftMap(userID, storage);
    if (stored && (bucket in stored || `${bucket}.meta` in stored)) {
      delete stored[bucket];
      delete stored[`${bucket}.meta`];
      if (!writeDraftMap(userID, stored, storage)) return false;
    }
    return true;
  }
  const bounded = annotations
    .slice(0, GATEWAY_ANNOTATION_DRAFT_MAX_ANNOTATIONS)
    .map(validDraftAnnotation)
    .filter(Boolean);
  if (bounded.length === 0) {
    writeGatewayAnnotationDrafts(userID, topicId, agentUid, appId, [], storage);
    return true;
  }
  // The canonical set must fit the server's 16KiB UTF-8 byte bound before it
  // is persisted (JS .length counts UTF-16 units and under-counts CJK). An
  // over-limit set is refused as-is: the previously persisted rows stay
  // untouched and the caller surfaces an explicit error, so no silent
  // trimming and no silent bucket destruction ever happen.
  const serialized = new TextEncoder().encode(JSON.stringify(bounded));
  if (serialized.length > GATEWAY_ANNOTATION_DRAFT_MAX_BYTES) {
    return false;
  }
  const stored = readDraftMap(userID, storage) || {};
  // The canonical envelope (certificates + rows) must also fit; the caller
  // treats false as "not saved" and keeps the editor open.
  return writeDraftMap(userID, withCertificate(stored, bucket, bounded), storage);
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

// Logout/session-expiry cleanup: removes every user's gateway annotation draft
// bucket from the given storage (sessionStorage + its localStorage mirror, the
// same shaped semantics as clearPersistedComposerDrafts). User-scoped keys are
// an isolation boundary, not retention: a same-account relogin that finds the
// old bucket would be able to resurrect stale targets without re-confirming
// the page, so logout removes all buckets unconditionally.
export function clearPersistedGatewayAnnotationDrafts(storage = 'sessionStorage') {
  const resolved = [];
  if (storage && typeof storage === 'object') {
    resolved.push(storage);
  } else if (typeof storage === 'string' && storage) {
    resolved.push(getStorage(storage));
    if (storage === 'sessionStorage') resolved.push(getStorage('localStorage'));
  }
  const targets = resolved.filter(Boolean);
  let removed = 0;
  targets.forEach((target) => {
    try {
      const doomed = [];
      for (let index = 0; index < target.length; index += 1) {
        const key = target.key(index);
        if (typeof key === 'string' && key.startsWith(GATEWAY_ANNOTATION_DRAFTS_STORAGE_PREFIX)) {
          doomed.push(key);
        }
      }
      doomed.forEach((key) => {
        if (removeStorageValue(key, target)) removed += 1;
      });
    } catch {
      // A blocked storage target must not prevent the other target cleanup.
    }
  });
  return removed;
}
