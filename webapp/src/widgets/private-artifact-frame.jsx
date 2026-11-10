import React, { forwardRef, useCallback, useLayoutEffect, useRef, useState } from 'react';
import { flushSync } from 'react-dom';
import { getAuthRevision, getToken, isCurrentAuthSession, request } from '../auth-session';

// Gateway application launches have their own identity exchange. Only stored
// artifact paths use the scoped static-media credential.
export function isPrivateArtifactFrameURL(src) {
  try {
    const url = new URL(src, window.location.origin);
    return ['http:', 'https:'].includes(url.protocol)
      && /^\/(?:artifacts|by-agent)\//.test(url.pathname);
  } catch {
    return false;
  }
}

function sessionIdentity() {
  return { token: getToken(), revision: getAuthRevision() };
}

function authorizedTransportURL(src, transport) {
  if (typeof transport !== 'string' || !transport) return false;
  try {
    const original = new URL(src, window.location.origin);
    const signed = new URL(transport, window.location.origin);
    if (!['http:', 'https:'].includes(original.protocol)
      || !signed.searchParams.get('media_token')) return false;
    original.searchParams.delete('media_token');
    signed.searchParams.delete('media_token');
    original.searchParams.sort();
    signed.searchParams.sort();
    return original.href === signed.href;
  } catch {
    return false;
  }
}

// src remains the canonical identity owned by the caller. The signed address
// is transport state only, and never escapes through a binding or file record.
const PrivateArtifactFrame = forwardRef(function PrivateArtifactFrame({
  src,
  messageId,
  onError,
  onLoad,
  credentialless: _credentialless,
  ...props
}, forwardedRef) {
  const [identity, setIdentity] = useState(sessionIdentity);
  const [ready, setReady] = useState(null);
  const frameRef = useRef(null);
  const callbacks = useRef(null);
  callbacks.current = { onError, onLoad };
  const setFrame = useCallback((frame) => {
    frameRef.current = frame;
    if (typeof forwardedRef === 'function') forwardedRef(frame);
    else if (forwardedRef) forwardedRef.current = frame;
  }, [forwardedRef]);

  useLayoutEffect(() => {
    const changed = () => {
      // Commit removal in this event, before any old document can remain active
      // under the newly selected account.
      const hadFrame = Boolean(frameRef.current);
      flushSync(() => {
        setReady(null);
        setIdentity(sessionIdentity());
      });
      if (hadFrame) callbacks.current.onError?.(new Error('artifact_session_changed'));
    };
    window.addEventListener('cc:auth-changed', changed);
    return () => window.removeEventListener('cc:auth-changed', changed);
  }, []);

  useLayoutEffect(() => {
    const controller = new AbortController();
    const { token, revision } = identity;
    setReady(null);
    if (!src) return () => controller.abort();
    if (!isCurrentAuthSession(token, revision)) {
      callbacks.current.onError?.(new Error('artifact_session_required'));
      return () => controller.abort();
    }
    const body = { url: src };
    if (Number.isSafeInteger(messageId) && messageId > 0) body.message_id = messageId;
    request('POST', '/api/files/artifact-session', body, {
      authToken: token,
      signal: controller.signal,
      timeoutMs: 15_000,
    }).then((result) => {
      if (controller.signal.aborted || !isCurrentAuthSession(token, revision)) return;
      if (!authorizedTransportURL(src, result?.url)) throw new Error('artifact_session_invalid_url');
      setReady({ src, url: result.url, token, revision });
    }).catch((error) => {
      if (!controller.signal.aborted && isCurrentAuthSession(token, revision)) {
        callbacks.current.onError?.(error);
      }
    });
    return () => controller.abort();
  }, [src, messageId, identity.token, identity.revision]);

  if (!ready || ready.src !== src
    || !isCurrentAuthSession(ready.token, ready.revision)) return null;
  return (
    <iframe {...props} ref={setFrame} src={ready.url}
      onLoad={(event) => {
        if (frameRef.current === event.currentTarget && isCurrentAuthSession(ready.token, ready.revision)) callbacks.current.onLoad?.(event);
      }}
      onError={(event) => {
        if (frameRef.current === event.currentTarget && isCurrentAuthSession(ready.token, ready.revision)) callbacks.current.onError?.(event);
      }} />
  );
});

export default PrivateArtifactFrame;
