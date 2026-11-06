import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { api } from '../api';
import { forkTargetTopic, normalizeForkRecord } from './conversation-fork';
import { readStorageValue, removeStorageValue, writeStorageValue } from './storage-access';

const requestOptions = { timeoutMs: 15000 };
const refreshConversations = () => window.dispatchEvent(new Event('cc:data-changed'));
const forkChangedEvent = 'cc:conversation-fork-changed';

function storedPayload(key, topic) {
  try {
    const saved = JSON.parse(readStorageValue(key));
    return saved?.source_topic_id === topic && typeof saved?.request_id === 'string' && saved.request_id ? saved : null;
  } catch { return null; }
}

export function useConversationFork({ topic, userId, onActivateTopic }) {
  const scope = `${userId}:${topic}`;
  const owner = useMemo(() => {
    const key = `cc:conversation-fork:${scope}`;
    return { key, payload: storedPayload(key, topic), active: true, busy: false, refreshRequested: false, targetRevision: 0, lastAnchor: 0 };
  }, [scope, topic]);
  const current = useRef(owner);
  current.current = owner;
  const [source, setSource] = useState(null);
  const [target, setTarget] = useState(null);
  // A terminal failure is only a report: the copy is already unusable and the
  // topic stays fenced server-side, so the banner cannot be "fixed" by clicking.
  // Without a way to acknowledge it, every visit to the copy re-renders the same
  // error and the conversation looks permanently broken.
  //
  // The dismissal records the failed request ID rather than a bare flag, so a
  // later copy that fails for a new reason still surfaces instead of being
  // hidden by an old acknowledgement.
  const dismissKey = `cc:conversation-fork-dismissed:${scope}`;
  const [dismissRevision, setDismissRevision] = useState(0);
  const dismissedRequestId = useMemo(
    () => String(readStorageValue(dismissKey) || ''),
    [dismissKey, dismissRevision],
  );
  const isCurrent = useCallback(() => owner.active && current.current === owner, [owner]);
  const sourceState = source?.owner === owner ? source : {};
  const targetState = target?.owner === owner ? target : {};
  const operation = sourceState.record || null;
  const needsTargetCheck = /^grp_\d+$/.test(topic || '');
  const blocked = needsTargetCheck && (!targetState.checked || Boolean(targetState.error)
    || (targetState.record && targetState.record.state !== 'ready'));

  // Only a terminal failure is dismissable. It is a report the user cannot act
  // on from inside the copy: the topic stays fenced server-side, so hiding the
  // banner never makes it writable. Without this the same error re-renders on
  // every visit and the conversation looks permanently broken.
  //
  // The stored value is the failed request id, not a bare flag, so a later copy
  // that fails for a different reason surfaces again instead of inheriting an
  // old acknowledgement.
  const failedRequestId = targetState.record?.state === 'failed'
    ? String(targetState.record.request_id || '')
    : operation?.state === 'failed' ? String(operation.request_id || '') : '';
  const noticeDismissed = Boolean(failedRequestId) && dismissedRequestId === failedRequestId;
  const dismissNotice = useCallback(() => {
    if (!failedRequestId) return;
    writeStorageValue(dismissKey, failedRequestId);
    setDismissRevision(revision => revision + 1);
  }, [dismissKey, failedRequestId]);

  const acceptSource = useCallback(record => {
    if (!isCurrent()) return;
    setSource({ owner, record, error: '', busy: false });
    refreshConversations();
  }, [isCurrent, owner]);

  const checkSource = useCallback(async () => {
    if (owner.busy) { owner.refreshRequested = true; return; }
    owner.payload = storedPayload(owner.key, topic) || owner.payload;
    if (!owner.payload) return;
    owner.busy = true;
    setSource(state => ({ owner, record: state?.owner === owner && state.record?.request_id === owner.payload.request_id ? state.record : null, busy: true, error: '' }));
    try {
      const result = await api.getConversationFork({ requestId: owner.payload.request_id }, requestOptions);
      acceptSource(normalizeForkRecord(result, { requestId: owner.payload.request_id, sourceTopic: topic }));
    } catch (error) {
      if (isCurrent()) setSource(state => ({
        ...(state?.owner === owner ? state : {}), owner,
        error: error.message || '无法确认复制结果，请重试。',
        // Set from this failure, not carried over: a previous create may have
        // been rejected with a 4xx, and a network failure here would then be
        // reported as a rejection.
        errorStatus: Number(error?.status) || 0,
        busy: false,
      }));
    } finally {
      owner.busy = false;
      if (owner.refreshRequested && isCurrent()) {
        owner.refreshRequested = false;
        void checkSource();
      }
    }
  }, [acceptSource, isCurrent, owner, topic]);

  const checkTarget = useCallback(async () => {
    if (!needsTargetCheck) return;
    const revision = ++owner.targetRevision;
    setTarget(state => ({ ...(state?.owner === owner ? state : {}), owner, checking: true }));
    try {
      const result = await api.getConversationFork({ topicId: topic }, requestOptions);
      const record = normalizeForkRecord(result, { targetTopic: topic });
      if (isCurrent() && revision === owner.targetRevision) setTarget({ owner, checked: true, record });
    } catch (error) {
      if (!isCurrent() || revision !== owner.targetRevision) return;
      // Only an authoritative "not a fork" response makes an ordinary topic writable.
      if (error.status === 404 && error.message === 'session_fork_not_found') setTarget({ owner, checked: true, record: null });
      else setTarget(state => ({ ...(state?.owner === owner ? state : {}), owner, checked: true, checking: false, error: error.message || '无法确认会话状态，请重试。' }));
    }
  }, [isCurrent, needsTargetCheck, owner, topic]);

  useEffect(() => {
    owner.active = true;
    const changed = event => {
      if (event.detail?.key === owner.key && event.detail?.owner !== owner) void checkSource();
    };
    window.addEventListener(forkChangedEvent, changed);
    void checkTarget();
    void checkSource();
    return () => {
      owner.active = false;
      window.removeEventListener(forkChangedEvent, changed);
    };
  }, [checkSource, checkTarget, owner]);

  useEffect(() => {
    if (operation?.state !== 'pending' || sourceState.error || sourceState.busy) return undefined;
    const timer = setTimeout(() => void checkSource(), 2000);
    return () => clearTimeout(timer);
  }, [checkSource, operation, sourceState.busy, sourceState.error]);

  useEffect(() => {
    if (targetState.record?.state !== 'pending' || targetState.error || targetState.checking) return undefined;
    const timer = setTimeout(() => void checkTarget(), 2000);
    return () => clearTimeout(timer);
  }, [checkTarget, targetState]);

  const create = useCallback(async ({ newRequest = false, untilMessageId } = {}) => {
    if (owner.busy || !isCurrent()) return;
    // The header and sidebar are two controls for the same operation. Reuse
    // an unseen request instead of allocating a competing copy from stale UI.
    owner.payload = storedPayload(owner.key, topic) || owner.payload;
    // The anchor is part of the request's identity: the server compares it
    // against the stored one and answers 409 on a mismatch. Reusing a payload
    // whose anchor differs would therefore either fail again with the same
    // reason or — worse — drop the anchor entirely and copy the whole
    // conversation while the user believes they picked a point.
    //
    // A 4xx never creates a server-side record, so a payload that was written
    // before the request left stays dangling. It would be picked up as an
    // identity by any later click, so it has to go when the request failed
    // that way.
    //
    // Three cases, and the difference matters:
    //
    //   undefined — a retry of this copy. It inherits the anchor this copy was
    //               made with, from memory or from the stored payload, because
    //               the user is retrying the same thing.
    //   0         — an explicit "copy everything" request from the header or
    //               the sidebar. Those entry points never had a point.
    //   > 0       — a point picked on a message.
    //
    // The retry case has to read the stored payload too: the anchor lives in
    // memory only until the page is reloaded, and after a reload the stored
    // payload is the only place it is left.
    const explicit = Number.isSafeInteger(untilMessageId) && untilMessageId > 0 ? untilMessageId : 0;
    // Both explicit forms have to be recorded, including the unbounded one.
    // Writing only `> 0` left the previous point in place, so a later retry
    // inherited it: copy from a message, then copy everything, then retry — and
    // the retry silently came back bounded to the old point.
    if (untilMessageId !== undefined) owner.lastAnchor = explicit;
    const anchor = untilMessageId === undefined
      ? (Number(owner.lastAnchor || 0) || Number(owner.payload?.until_message_id || 0))
      : explicit;
    const storedAnchor = Number(owner.payload?.until_message_id || 0);
    const anchorChanged = Boolean(owner.payload) && storedAnchor !== anchor;
    // A different anchor is a different copy, so it has to go through even
    // while another copy of this conversation is still pending: the user is
    // answering the mistake they just made, not asking for that copy again.
    if (anchorChanged) {
      owner.payload = { source_topic_id: topic, request_id: crypto.randomUUID() };
      if (anchor > 0) owner.payload.until_message_id = anchor;
      writeStorageValue(owner.key, JSON.stringify(owner.payload));
    } else {
      if (!newRequest && ['ready', 'failed'].includes(operation?.state)) return;
      if (newRequest && operation && !['ready', 'failed'].includes(operation.state)) return;
      if (!owner.payload
        || (newRequest && operation?.request_id === owner.payload.request_id && ['ready', 'failed'].includes(operation?.state))) {
        owner.payload = { source_topic_id: topic, request_id: crypto.randomUUID() };
        if (anchor > 0) owner.payload.until_message_id = anchor;
        writeStorageValue(owner.key, JSON.stringify(owner.payload));
      }
    }
    owner.busy = true;
    window.dispatchEvent(new CustomEvent(forkChangedEvent, { detail: { key: owner.key, owner } }));
    setSource({ owner, record: newRequest ? null : operation, busy: true, error: '' });
    try {
      const result = await api.forkConversation(owner.payload, requestOptions);
      acceptSource(normalizeForkRecord(result, { requestId: owner.payload.request_id, sourceTopic: topic }));
    } catch (error) {
      // A 4xx never creates a server-side record: the request was rejected, so
      // no session fork exists to be found later. The payload was written
      // before the request left, which would leave a request id that no server
      // knows about — and the next click would reuse it as its identity, so the
      // same rejection would repeat with no way out. Drop it.
      if (error?.status >= 400 && error?.status < 500 && owner.payload) {
        removeStorageValue(owner.key);
        owner.payload = null;
      }
      if (isCurrent()) setSource(state => ({
        ...state, owner, busy: false,
        error: error.message || '复制请求未确认，请重试。',
        errorStatus: Number(error?.status) || 0,
      }));
    } finally {
      owner.busy = false;
      window.dispatchEvent(new CustomEvent(forkChangedEvent, { detail: { key: owner.key, owner } }));
      if (owner.refreshRequested && isCurrent()) {
        owner.refreshRequested = false;
        void checkSource();
      }
    }
  }, [acceptSource, checkSource, isCurrent, operation, owner, topic]);

  const cancel = useCallback(async () => {
    if (owner.busy || !isCurrent()) return;
    owner.payload = storedPayload(owner.key, topic) || owner.payload;
    if (!owner.payload) return;
    owner.busy = true;
    setSource(state => ({ ...(state?.owner === owner ? state : {}), owner, busy: true, error: '' }));
    try {
      const result = await api.cancelConversationFork({ requestId: owner.payload.request_id }, requestOptions);
      const record = normalizeForkRecord(result, { requestId: owner.payload.request_id, sourceTopic: topic });
      if (record.state === 'ready') {
        // A copy that finished before the cancellation arrived stays openable.
        if (isCurrent()) setSource({ owner, record, busy: false, error: '' });
      } else {
        removeStorageValue(owner.key);
        owner.payload = null;
        if (isCurrent()) setSource({ owner, record: null, busy: false, error: '' });
      }
    } catch (error) {
      if (isCurrent()) setSource(state => ({ ...state, owner, busy: false, error: error.message || '取消复制失败，请重试。' }));
    } finally {
      owner.busy = false;
      window.dispatchEvent(new CustomEvent(forkChangedEvent, { detail: { key: owner.key, owner } }));
      if (owner.refreshRequested && isCurrent()) {
        owner.refreshRequested = false;
        void checkSource();
      }
    }
  }, [checkSource, isCurrent, owner, topic]);

  const open = useCallback(async () => {
    if (operation?.state !== 'ready' || owner.busy) return;
    owner.busy = true;
    setSource(state => ({ ...state, busy: true, error: '' }));
    try {
      // A summary supplies the ordinary sidebar title, but IDs alone are sufficient.
      const result = await api.getConversations().catch(() => ({ conversations: [] }));
      if (isCurrent()) {
        onActivateTopic?.(forkTargetTopic(operation, result?.conversations));
        refreshConversations();
      }
    } catch (error) {
      if (isCurrent()) setSource(state => ({ ...state, error: error.message }));
    } finally {
      owner.busy = false;
      if (isCurrent()) setSource(state => ({ ...state, busy: false }));
    }
  }, [isCurrent, onActivateTopic, operation, owner]);

  return {
    operation, error: sourceState.error || '', errorStatus: sourceState.errorStatus || 0, busy: sourceState.busy || false,
    provenance: targetState.record || null, targetError: targetState.error || '',
    checking: needsTargetCheck && (!targetState.checked || targetState.checking),
    blocked: Boolean(blocked), create, open, cancel, checkTarget,
    noticeDismissed, dismissNotice,
  };
}
