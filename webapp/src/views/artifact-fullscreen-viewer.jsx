import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import {
  api,
  connectWS,
  disconnectWS,
  hasArtifactPreviewSession,
  reconnectWS,
  sendWSActiveTopic,
  sendWSPageFocus,
  sendWSPageVisibility,
  setToken,
  wsSendArtifactResultReceipt,
} from '../api';
import {
  artifactContextRefFromSnapshot,
  artifactRefFromPreviewFile,
  artifactURLForVersion,
  normalizeArtifactResultDelivery,
  requestArtifactPageContext,
  requestArtifactResultApply,
} from '../artifact-context';
import {
  artifactPreviewCoordinationID,
  createArtifactPreviewChannel,
  createArtifactPreviewMessage,
  normalizeArtifactPreviewMessage,
  parseArtifactViewerLocation,
  sameArtifactPreviewIdentity,
} from '../artifact-preview-coordinator';
import { createArtifactTaskHost } from '../artifact-task-host';
import { useFeedback } from '../components/feedback-system';
import { clearPersistedComposerDrafts } from '../utils/composer-draft-storage';
import { createCloudArtifactPreviewFile, previewFileDescriptor } from '../widgets/chat-message';
import ControlledArtifactPreview from '../widgets/controlled-artifact-preview';
import './artifact-fullscreen-viewer.css';

const VIEWER_HEARTBEAT_MS = 2000;
const VIEWER_SNAPSHOT_TIMEOUT_MS = 3000;

function viewerErrorMessage(error) {
  const code = String(error?.code || error?.message || error || '');
  if (code.includes('artifact_not_found')) return '这个应用已经不存在或不可访问。';
  if (code.includes('artifact_viewer_invalid')) return '这个应用链接不完整。';
  if (code.includes('artifact_viewer_version')) return '指定的应用版本不可用。';
  if (code.includes('artifact_viewer_channel')) return '当前浏览器无法建立应用连接。';
  if (code.includes('artifact_viewer_connection')) return '应用已打开，但暂时无法连接虚拟员工。';
  return '暂时无法打开这个应用。';
}

export default function ArtifactFullscreenViewer({ location = window.location } = {}) {
  const feedback = useFeedback();
  const params = useMemo(
    () => parseArtifactViewerLocation(location),
    [location.pathname, location.search],
  );
  const identity = params ? {
    topicId: params.topicId,
    agentUid: params.agentUid,
    artifactId: params.artifactId,
    displayedVersion: params.displayedVersion,
  } : null;
  const identityKey = identity
    ? `${identity.topicId}|${identity.agentUid}|${identity.artifactId}|${identity.displayedVersion}`
    : '';
  const viewerIdRef = useRef(artifactPreviewCoordinationID('viewer'));
  const channelRef = useRef(null);
  const bindingRef = useRef(null);
  const fileRef = useRef(null);
  const snapshotRef = useRef(null);
  const taskHostRef = useRef(null);
  const taskFeedbackRef = useRef(feedback);
  const captureQueueRef = useRef(Promise.resolve());
  const coordinationHandlerRef = useRef(null);
  const resultHandlerRef = useRef(null);
  const recoverWSRef = useRef(null);
  const readyAttemptRef = useRef(false);
  const readyRef = useRef(false);
  const releasedRef = useRef(false);
  const sessionEpochRef = useRef(0);
  const [file, setFile] = useState(null);
  const [loading, setLoading] = useState(Boolean(params));
  const [error, setError] = useState(params ? '' : 'artifact_viewer_invalid');
  const [frameReady, setFrameReady] = useState(false);
  const [sessionReady, setSessionReady] = useState(false);
  const [ready, setReady] = useState(false);
  const [released, setReleased] = useState(false);
  fileRef.current = file;
  taskFeedbackRef.current = feedback;
  const descriptor = useMemo(() => previewFileDescriptor(file), [file]);

  const postCoordination = useCallback((type, extra = {}) => {
    if (!identity || !channelRef.current) return false;
    const message = createArtifactPreviewMessage(type, identity, {
      viewer_id: viewerIdRef.current,
      handoff_id: params?.handoffId,
      sent_at: Date.now(),
      ...extra,
    });
    if (!message) return false;
    try {
      channelRef.current.postMessage(message);
      return true;
    } catch {
      return false;
    }
  }, [identityKey, params?.handoffId]);

  const invalidateCurrentSnapshot = useCallback(() => {
    const contextRef = String(snapshotRef.current?.contextRef || '');
    snapshotRef.current = null;
    if (!contextRef) return;
    api.invalidateArtifactContextSnapshot(contextRef, {
      timeoutMs: VIEWER_SNAPSHOT_TIMEOUT_MS,
    }).catch(() => {});
  }, []);

  const captureSnapshotNow = useCallback(async () => {
    if (!identity || releasedRef.current || !hasArtifactPreviewSession()) return '';
    const sessionEpoch = sessionEpochRef.current;
    const currentFile = file;
    const binding = bindingRef.current;
    const artifactRef = artifactRefFromPreviewFile(currentFile, identity.agentUid);
    if (!currentFile || !binding || !artifactRef
      || binding.artifactId !== identity.artifactId
      || Number(binding.agentUid || 0) !== identity.agentUid
      || Number(artifactRef.displayed_version || 0) !== identity.displayedVersion) return '';

    const pageContext = await requestArtifactPageContext(binding, artifactRef);
    if (releasedRef.current
      || sessionEpochRef.current !== sessionEpoch
      || !hasArtifactPreviewSession()
      || bindingRef.current !== binding) return '';
    let response;
    try {
      response = await api.createArtifactContextSnapshot({
        topic_id: identity.topicId,
        artifact_ref: artifactRef,
        ...(pageContext ? { page_context: pageContext } : {}),
      }, { timeoutMs: VIEWER_SNAPSHOT_TIMEOUT_MS });
    } catch {
      return '';
    }
    const contextRef = artifactContextRefFromSnapshot(response);
    if (!contextRef
      || releasedRef.current
      || sessionEpochRef.current !== sessionEpoch
      || !hasArtifactPreviewSession()
      || bindingRef.current !== binding) {
      if (contextRef) {
        api.invalidateArtifactContextSnapshot(contextRef, {
          timeoutMs: VIEWER_SNAPSHOT_TIMEOUT_MS,
        }).catch(() => {});
      }
      return '';
    }
    snapshotRef.current = {
      contextRef,
      identity,
      binding,
    };
    return contextRef;
  }, [file, identityKey]);

  const captureSnapshot = useCallback(() => {
    const run = captureQueueRef.current
      .catch(() => undefined)
      .then(() => captureSnapshotNow());
    captureQueueRef.current = run.then(() => undefined, () => undefined);
    return run;
  }, [captureSnapshotNow]);

  const releaseViewer = useCallback((reason = 'sidebar_claimed') => {
    if (releasedRef.current) return;
    releasedRef.current = true;
    sessionEpochRef.current += 1;
    readyAttemptRef.current = false;
    readyRef.current = false;
    setReady(false);
    setSessionReady(false);
    setError('');
    setReleased(true);
    taskHostRef.current?.deactivate();
    invalidateCurrentSnapshot();
    postCoordination('viewer_released', { error: reason });
    disconnectWS();
  }, [invalidateCurrentSnapshot, postCoordination]);

  const handleArtifactResult = useCallback(async (value) => {
    if (!identity || releasedRef.current || !readyRef.current) return;
    const delivery = normalizeArtifactResultDelivery(value);
    if (delivery?.taskId) {
      await taskHostRef.current?.handleResultDelivery(value);
      return;
    }
    const snapshot = snapshotRef.current;
    const binding = bindingRef.current;
    if (!delivery || !snapshot || !binding
      || snapshot.contextRef !== delivery.contextRef
      || delivery.topicId !== identity.topicId
      || delivery.agentUid !== identity.agentUid
      || delivery.artifactId !== identity.artifactId
      || delivery.displayedVersion !== identity.displayedVersion
      || snapshot.binding !== binding) return;

    const receipt = await requestArtifactResultApply(binding, delivery);
    if (!receipt || releasedRef.current
      || snapshotRef.current !== snapshot
      || bindingRef.current !== binding) return;
    wsSendArtifactResultReceipt({
      type: 'receipt',
      origin_node_id: delivery.originNodeId,
      context_ref: delivery.contextRef,
      writeback_ref: delivery.writebackRef,
      topic_id: delivery.topicId,
      agent_uid: String(delivery.agentUid),
      artifact_id: delivery.artifactId,
      displayed_version: delivery.displayedVersion,
      result_id: delivery.resultId,
      receipt,
    });
  }, [identityKey]);
  resultHandlerRef.current = handleArtifactResult;

  useEffect(() => {
    if (!identity) return undefined;
    const host = createArtifactTaskHost({
      getCurrentSession: () => {
        const currentFile = fileRef.current;
        const binding = bindingRef.current;
        const artifactRef = artifactRefFromPreviewFile(currentFile, identity.agentUid);
        if (releasedRef.current || !readyRef.current || !hasArtifactPreviewSession()
          || !currentFile || !binding || !artifactRef
          || binding.artifactId !== identity.artifactId
          || Number(binding.agentUid || 0) !== identity.agentUid
          || Number(artifactRef.displayed_version || 0) !== identity.displayedVersion) return null;
        return {
          token: binding,
          identityKey,
          topicId: identity.topicId,
          topicGeneration: 0,
          agentUid: identity.agentUid,
          artifactId: identity.artifactId,
          displayedVersion: identity.displayedVersion,
          artifactRef,
          binding,
        };
      },
      confirmTask: () => taskFeedbackRef.current.confirm({
        title: '发送给虚拟员工？',
        message: '该应用希望把你刚才的操作作为一条新消息交给当前虚拟员工处理。',
        confirmLabel: '确认发送',
        cancelLabel: '取消',
      }),
    });
    taskHostRef.current = host;
    window.addEventListener('message', host.handleWindowMessage);
    if (readyRef.current) host.connect(bindingRef.current);
    return () => {
      window.removeEventListener('message', host.handleWindowMessage);
      host.dispose();
      if (taskHostRef.current === host) taskHostRef.current = null;
    };
  }, [identityKey]);

  coordinationHandlerRef.current = async (rawMessage) => {
    const message = normalizeArtifactPreviewMessage(rawMessage);
    if (!message
      || !identity
      || message.handoffId !== params?.handoffId
      || !sameArtifactPreviewIdentity(message, identity)) return;
    if (message.type === 'sidebar_claimed') {
      if (message.viewerId && message.viewerId !== viewerIdRef.current) return;
      releaseViewer();
      return;
    }
    if (message.type === 'viewer_rejected') {
      if (message.viewerId !== viewerIdRef.current) return;
      releaseViewer(message.error || 'viewer_rejected');
      return;
    }
    if (message.type === 'viewer_accepted') {
      if (message.viewerId !== viewerIdRef.current
        || releasedRef.current
        || !readyAttemptRef.current
        || !snapshotRef.current
        || !bindingRef.current
        || !hasArtifactPreviewSession()) return;
      readyAttemptRef.current = false;
      readyRef.current = true;
      setReady(true);
      taskHostRef.current?.resume();
      taskHostRef.current?.connect(bindingRef.current);
      return;
    }
    if (message.type === 'request_current_preview') {
      if (message.viewerId === viewerIdRef.current
        && message.requestId
        && readyRef.current
        && !releasedRef.current) {
        postCoordination('current_preview', {
          request_id: message.requestId,
          context_ref: snapshotRef.current?.contextRef || '',
        });
      }
      return;
    }
    if (message.type !== 'context_request'
      || message.viewerId !== viewerIdRef.current
      || !message.requestId
      || releasedRef.current
      || !readyRef.current) return;
    const contextRef = await captureSnapshot();
    postCoordination('context_response', {
      request_id: message.requestId,
      context_ref: contextRef,
      ...(contextRef ? {} : { error: 'artifact_context_unavailable' }),
    });
  };

  useEffect(() => {
    if (!identity) return undefined;
    let cancelled = false;
    setLoading(true);
    setError('');
    api.getCloudArtifacts(identity.agentUid, 'active')
      .then((response) => {
        if (cancelled) return;
        const artifact = (Array.isArray(response?.artifacts) ? response.artifacts : [])
          .find((item) => String(item?.id || '') === identity.artifactId);
        const latestVersion = Number(artifact?.publish_version || 0);
        if (!artifact) throw new Error('artifact_not_found');
        if (latestVersion <= 0 || identity.displayedVersion > latestVersion) {
          throw new Error('artifact_viewer_version');
        }
        const exactURL = artifactURLForVersion(artifact.url, identity.displayedVersion);
        if (!exactURL) throw new Error('artifact_viewer_version');
        const previewFile = createCloudArtifactPreviewFile({
          ...artifact,
          url: exactURL,
          publish_version: identity.displayedVersion,
          agent_uid: identity.agentUid,
        });
        if (!artifactRefFromPreviewFile(previewFile, identity.agentUid)) {
          throw new Error('artifact_viewer_invalid');
        }
        document.title = `${artifact.title || artifact.id} - CatsCo`;
        setFile(previewFile);
      })
      .catch((loadError) => {
        if (!cancelled) setError(String(loadError?.message || 'artifact_not_found'));
      })
      .finally(() => {
        if (!cancelled) setLoading(false);
      });
    return () => {
      cancelled = true;
    };
  }, [identityKey]);

  useEffect(() => {
    if (!identity) return undefined;
    const channel = createArtifactPreviewChannel();
    if (!channel) {
      setError('artifact_viewer_channel');
      return undefined;
    }
    channelRef.current = channel;
    channel.onmessage = (event) => {
      void coordinationHandlerRef.current?.(event.data);
    };
    postCoordination('viewer_hello');
    return () => {
      postCoordination('viewer_closed');
      channel.onmessage = null;
      channel.close();
      if (channelRef.current === channel) channelRef.current = null;
    };
  }, [identityKey, postCoordination]);

  useEffect(() => {
    if (!identity) return undefined;
    const suspendViewerSession = () => {
      const wasAnnounced = readyAttemptRef.current || readyRef.current;
      sessionEpochRef.current += 1;
      readyAttemptRef.current = false;
      readyRef.current = false;
      setReady(false);
      setSessionReady(false);
      taskHostRef.current?.suspend();
      invalidateCurrentSnapshot();
      if (wasAnnounced) postCoordination('viewer_closed', { error: 'connection_lost' });
    };
    const handleWSMessage = (message) => {
      if (message?._type === 'ws_auth_expired') {
        suspendViewerSession();
        setToken(null);
        clearPersistedComposerDrafts();
        setError('artifact_viewer_connection');
        return;
      }
      if (message?._type === 'ws_close' || message?._type === 'ws_connecting') {
        suspendViewerSession();
      } else if (hasArtifactPreviewSession()) {
        setSessionReady(true);
      }
      if (message?.artifact_result) {
        void resultHandlerRef.current?.(message.artifact_result);
      }
    };
    sendWSActiveTopic(identity.topicId);
    connectWS(handleWSMessage);

    const recover = (force = false) => {
      if (releasedRef.current) return;
      if (document.visibilityState !== 'visible' || navigator.onLine === false) return;
      if (force) reconnectWS(handleWSMessage);
      else connectWS(handleWSMessage);
    };
    recoverWSRef.current = recover;
    const handleVisibility = () => {
      if (releasedRef.current) return;
      sendWSPageVisibility(document.visibilityState);
      sendWSPageFocus(document.visibilityState === 'visible' && document.hasFocus());
      if (document.visibilityState === 'visible') recover(false);
    };
    const handleFocus = () => {
      if (releasedRef.current) return;
      sendWSPageFocus(true);
      recover(false);
    };
    const handleBlur = () => {
      if (!releasedRef.current) sendWSPageFocus(false);
    };
    const handleOnline = () => recover(true);
    document.addEventListener('visibilitychange', handleVisibility);
    window.addEventListener('focus', handleFocus);
    window.addEventListener('blur', handleBlur);
    window.addEventListener('online', handleOnline);
    return () => {
      document.removeEventListener('visibilitychange', handleVisibility);
      window.removeEventListener('focus', handleFocus);
      window.removeEventListener('blur', handleBlur);
      window.removeEventListener('online', handleOnline);
      if (recoverWSRef.current === recover) recoverWSRef.current = null;
      disconnectWS();
    };
  }, [identityKey, invalidateCurrentSnapshot, postCoordination]);

  useEffect(() => {
    if (!identity || !file || !frameReady || !sessionReady || error
      || released || readyAttemptRef.current || readyRef.current) return;
    const sessionEpoch = sessionEpochRef.current;
    readyAttemptRef.current = true;
    void captureSnapshot().then((contextRef) => {
      if (sessionEpochRef.current !== sessionEpoch) return;
      if (!contextRef || releasedRef.current) {
        readyAttemptRef.current = false;
        setError('artifact_viewer_connection');
        return;
      }
      if (!postCoordination('viewer_ready', { context_ref: contextRef })) {
        readyAttemptRef.current = false;
        setError('artifact_viewer_connection');
      }
    });
  }, [captureSnapshot, error, file, frameReady, identityKey, postCoordination, released, sessionReady]);

  useEffect(() => {
    if (!ready || released || !identity) return undefined;
    const heartbeat = () => postCoordination('viewer_heartbeat', {
      context_ref: snapshotRef.current?.contextRef || '',
    });
    heartbeat();
    const timer = window.setInterval(heartbeat, VIEWER_HEARTBEAT_MS);
    return () => window.clearInterval(timer);
  }, [identityKey, postCoordination, ready, released]);

  useEffect(() => {
    const handlePageHide = (event) => {
      if (event.persisted) {
        sessionEpochRef.current += 1;
        captureQueueRef.current = Promise.resolve();
        readyAttemptRef.current = false;
        readyRef.current = false;
        setReady(false);
        setSessionReady(false);
        taskHostRef.current?.suspend();
        postCoordination('viewer_closed', { error: 'bfcache' });
        invalidateCurrentSnapshot();
        return;
      }
      postCoordination('viewer_closed');
      taskHostRef.current?.deactivate();
      invalidateCurrentSnapshot();
    };
    const handlePageShow = (event) => {
      if (!event.persisted || releasedRef.current) return;
      setError('');
      postCoordination('viewer_hello');
      recoverWSRef.current?.(true);
    };
    window.addEventListener('pagehide', handlePageHide);
    window.addEventListener('pageshow', handlePageShow);
    return () => {
      window.removeEventListener('pagehide', handlePageHide);
      window.removeEventListener('pageshow', handlePageShow);
      invalidateCurrentSnapshot();
    };
  }, [invalidateCurrentSnapshot, postCoordination]);

  const handleBindingChange = useCallback((binding) => {
    bindingRef.current = binding;
    setFrameReady(Boolean(binding));
    if (!binding) taskHostRef.current?.deactivate();
    else if (readyRef.current) taskHostRef.current?.connect(binding);
  }, []);

  return (
    <main className="artifact-fullscreen-viewer" aria-label="CatsCo 应用查看器">
      {file && !released && (
        <ControlledArtifactPreview
          file={file}
          descriptor={descriptor}
          className="artifact-fullscreen-viewer-frame"
          title={file.name || 'CatsCo Artifact'}
          onBindingChange={handleBindingChange}
          onError={() => setError('artifact_viewer_connection')}
        />
      )}
      {(loading || (file && !ready && !error && !released)) && (
        <div className="artifact-fullscreen-viewer-state" role="status">
          <span className="artifact-fullscreen-viewer-spinner" aria-hidden="true" />
          <span>正在打开应用…</span>
        </div>
      )}
      {error && (
        <div className="artifact-fullscreen-viewer-state is-error" role="alert">
          <strong>{viewerErrorMessage(error)}</strong>
          <button type="button" onClick={() => window.close()}>关闭此页</button>
        </div>
      )}
      {released && (
        <div className="artifact-fullscreen-viewer-state" role="status">
          <strong>应用已经回到对话侧边栏。</strong>
          <button type="button" onClick={() => window.close()}>关闭此页</button>
        </div>
      )}
    </main>
  );
}
