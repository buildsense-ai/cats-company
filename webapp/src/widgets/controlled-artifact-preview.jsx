import React, { useLayoutEffect, useMemo, useRef } from 'react';
import {
  ARTIFACT_FRAME_BRIDGE_CONTRACT,
  artifactFrameBridgeNonce,
  artifactFrameURLWithBridgeNonce,
} from '../artifact-context';

export const HTML_PREVIEW_SANDBOX = 'allow-scripts allow-forms allow-popups allow-modals';
export const REMOTE_ARTIFACT_PREVIEW_SANDBOX = `${HTML_PREVIEW_SANDBOX} allow-same-origin`;

function artifactFrameKey(file, url) {
  return [
    Number(file?.artifact_agent_uid || 0),
    String(file?.artifact_id || ''),
    Number(file?.publish_version || 0),
    String(url || ''),
  ].join('|');
}

export default function ControlledArtifactPreview({
  file,
  descriptor,
  className = 'v3-file-preview-frame',
  title = 'Cloud Artifact Preview',
  onBindingChange,
  onReady,
  onError,
}) {
  const url = String(descriptor?.url || file?.url || '');
  const sameOrigin = Boolean(descriptor?.isSameOriginRemoteArtifact);
  const frameKey = artifactFrameKey(file, url);
  const bridgeNonce = useMemo(
    () => sameOrigin ? artifactFrameBridgeNonce() : '',
    [frameKey, sameOrigin],
  );
  const callbacksRef = useRef(null);
  callbacksRef.current = { onBindingChange, onReady, onError };
  const lifecycleRef = useRef({
    key: '',
    loadCount: 0,
    controller: null,
    binding: null,
  });
  const expectedKey = `${frameKey}|${bridgeNonce}`;

  useLayoutEffect(() => {
    const lifecycle = {
      key: expectedKey,
      loadCount: 0,
      controller: new AbortController(),
      binding: null,
    };
    lifecycleRef.current?.controller?.abort();
    lifecycleRef.current = lifecycle;
    callbacksRef.current?.onBindingChange?.(null);
    if (sameOrigin && !bridgeNonce) {
      callbacksRef.current?.onError?.('artifact_frame_bridge_unavailable');
    }
    return () => {
      lifecycle.controller.abort();
      lifecycle.binding = null;
      if (lifecycleRef.current === lifecycle) {
        callbacksRef.current?.onBindingChange?.(null);
      }
    };
  }, [bridgeNonce, expectedKey, sameOrigin]);

  const clearBinding = () => {
    const lifecycle = lifecycleRef.current;
    if (!lifecycle?.binding) return;
    lifecycle.binding = null;
    callbacksRef.current?.onBindingChange?.(null);
  };

  const fail = (code) => {
    const lifecycle = lifecycleRef.current;
    lifecycle?.controller?.abort();
    clearBinding();
    callbacksRef.current?.onError?.(code);
  };

  const handleLoad = (event) => {
    const frame = event.currentTarget;
    const lifecycle = lifecycleRef.current;
    if (!frame || !lifecycle || lifecycle.key !== expectedKey) return;
    lifecycle.loadCount += 1;
    lifecycle.controller?.abort();
    lifecycle.controller = new AbortController();
    if (lifecycle.loadCount !== 1) {
      fail('artifact_frame_navigated');
      return;
    }
    const binding = {
      frame,
      artifactId: String(file?.artifact_id || ''),
      agentUid: Number(file?.artifact_agent_uid || 0),
      url,
      bridge: sameOrigin ? ARTIFACT_FRAME_BRIDGE_CONTRACT : undefined,
      bridgeNonce: sameOrigin ? bridgeNonce : undefined,
      bridgeReady: sameOrigin,
      signal: lifecycle.controller.signal,
    };
    lifecycle.binding = binding;
    callbacksRef.current?.onBindingChange?.(binding);
    callbacksRef.current?.onReady?.(binding);
  };

  if (!descriptor?.isRemoteArtifact || !url || !file?.artifact_id
    || (sameOrigin && !bridgeNonce)) return null;

  return (
    <iframe
      src={sameOrigin ? artifactFrameURLWithBridgeNonce(url, bridgeNonce) : url}
      className={className}
      title={title}
      sandbox={sameOrigin ? HTML_PREVIEW_SANDBOX : REMOTE_ARTIFACT_PREVIEW_SANDBOX}
      credentialless=""
      referrerPolicy="no-referrer"
      onLoad={handleLoad}
      onError={() => fail('artifact_frame_load_failed')}
    />
  );
}
