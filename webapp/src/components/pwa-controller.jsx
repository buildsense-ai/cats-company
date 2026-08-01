import React, {
  useCallback, useEffect, useMemo, useRef, useState,
} from 'react';
import { registerSW } from 'virtual:pwa-register';
import { api, getPushRegistrationID } from '../api';
import {
  canUsePush,
  ensurePushSubscription,
  pushDismissedStorageKey,
  serializePushSubscription,
  shouldOfferPush,
} from '../utils/push-notifications';
import { enqueuePushOperation } from '../utils/push-operation';
import { pushTabCoordinator } from '../utils/push-tab-coordination';
import './pwa-controller.css';

function readDismissed(owner) {
  const storageKey = pushDismissedStorageKey(owner);
  return Boolean(storageKey) && localStorage.getItem(storageKey) === 'true';
}

function persistDismissed(owner) {
  const storageKey = pushDismissedStorageKey(owner);
  if (storageKey) localStorage.setItem(storageKey, 'true');
}

export default function PwaController({
  loggedIn,
  pushPromptOwner,
  sessionRevision,
}) {
  const sessionRevisionRef = useRef(sessionRevision);
  sessionRevisionRef.current = sessionRevision;
  const [online, setOnline] = useState(() => navigator.onLine);
  const [dismissed, setDismissed] = useState(() => readDismissed(pushPromptOwner));
  const [permission, setPermission] = useState(() => (
    'Notification' in window ? Notification.permission : 'unsupported'
  ));
  const [busy, setBusy] = useState(false);
  const [pushError, setPushError] = useState('');
  const [needRefresh, setNeedRefresh] = useState(false);
  const [offlineReady, setOfflineReady] = useState(false);

  const updateServiceWorker = useMemo(() => registerSW({
    immediate: true,
    onNeedRefresh: () => setNeedRefresh(true),
    onOfflineReady: () => setOfflineReady(true),
    onRegisterError: (error) => console.warn('PWA registration failed:', error),
  }), []);

  useEffect(() => {
    const handleOnline = () => setOnline(true);
    const handleOffline = () => setOnline(false);
    window.addEventListener('online', handleOnline);
    window.addEventListener('offline', handleOffline);
    return () => {
      window.removeEventListener('online', handleOnline);
      window.removeEventListener('offline', handleOffline);
    };
  }, []);

  useEffect(() => {
    if (!offlineReady) return undefined;
    const timer = window.setTimeout(() => setOfflineReady(false), 5000);
    return () => window.clearTimeout(timer);
  }, [offlineReady]);

  useEffect(() => {
    setDismissed(readDismissed(pushPromptOwner));
    setPushError('');
  }, [pushPromptOwner]);

  useEffect(() => {
    pushTabCoordinator.setActive(
      loggedIn && canUsePush() && Notification.permission === 'granted',
    );
    return () => pushTabCoordinator.setActive(false);
  }, [loggedIn, permission]);

  useEffect(() => {
    if (!canUsePush() || !loggedIn || Notification.permission !== 'granted') return undefined;
    let cancelled = false;
    const isCurrent = () => (
      !cancelled && sessionRevisionRef.current === sessionRevision
    );

    const controller = new AbortController();
    const reconcilePush = async () => {
      try {
        const registrationID = getPushRegistrationID();
        const config = await api.getPushConfig(controller.signal);
        if (!isCurrent()) return;
        const publicKey = config.public_key;
        if (!config.enabled || !publicKey) return;
        const subscription = await ensurePushSubscription(
          publicKey,
          (endpoint) => api.unsubscribePush(endpoint, undefined, registrationID),
          isCurrent,
        );
        if (!subscription || !isCurrent()) return;
        await api.subscribePush(serializePushSubscription(subscription), registrationID, controller.signal);
      } catch (error) {
        if (!cancelled) console.warn('Push subscription reconciliation failed:', error);
      }
    };
    enqueuePushOperation(reconcilePush);
    return () => {
      cancelled = true;
      controller.abort();
    };
  }, [loggedIn, sessionRevision]);

  const offerPush = shouldOfferPush({ loggedIn, permission, dismissed });

  const dismissPush = useCallback(() => {
    persistDismissed(pushPromptOwner);
    setDismissed(true);
  }, [pushPromptOwner]);

  const enablePush = useCallback(async () => {
    if (!canUsePush() || busy) return;
    const requestedRevision = sessionRevision;
    const isCurrent = () => (
      sessionRevisionRef.current === requestedRevision
    );
    const controller = new AbortController();
    const abortOnSessionChange = () => controller.abort();
    window.addEventListener('cc:auth-changed', abortOnSessionChange, { once: true });
    setBusy(true);
    setPushError('');
    try {
      await enqueuePushOperation(async () => {
        const registrationID = getPushRegistrationID();
        const config = await api.getPushConfig(controller.signal);
        if (!isCurrent()) return;
        const publicKey = config.public_key;
        if (!config.enabled || !publicKey) throw new Error('推送服务尚未配置');

        const nextPermission = await Notification.requestPermission();
        if (!isCurrent()) return;
        setPermission(nextPermission);
        if (nextPermission !== 'granted') {
          persistDismissed(pushPromptOwner);
          setDismissed(true);
          return;
        }

        const subscription = await ensurePushSubscription(
          publicKey,
          (endpoint) => api.unsubscribePush(endpoint, undefined, registrationID),
          isCurrent,
        );
        if (!subscription || !isCurrent()) return;
        await api.subscribePush(serializePushSubscription(subscription), registrationID, controller.signal);
        if (isCurrent()) {
          persistDismissed(pushPromptOwner);
          setDismissed(true);
        }
      });
    } catch (error) {
      if (isCurrent()) setPushError(error?.message || '通知开启失败，请稍后重试');
    } finally {
      window.removeEventListener('cc:auth-changed', abortOnSessionChange);
      setBusy(false);
    }
  }, [busy, pushPromptOwner, sessionRevision]);

  return (
    <div className="cc-pwa-status" aria-live="polite">
      {!online && <div className="cc-pwa-offline">当前离线，消息将在网络恢复后重新加载</div>}
      {offlineReady && online && <div className="cc-pwa-toast">离线页面已准备好</div>}
      {needRefresh && (
        <div className="cc-pwa-prompt">
          <span>发现新版本</span>
          <button type="button" onClick={() => updateServiceWorker(true)}>立即更新</button>
          <button type="button" className="secondary" onClick={() => setNeedRefresh(false)}>稍后</button>
        </div>
      )}
      {offerPush && (
        <div className="cc-pwa-prompt">
          <span>开启通知，及时收到新消息</span>
          <button type="button" disabled={busy} onClick={enablePush}>{busy ? '开启中' : '开启'}</button>
          <button type="button" className="secondary" disabled={busy} onClick={dismissPush}>暂不</button>
        </div>
      )}
      {pushError && (
        <div className="cc-pwa-prompt error">
          <span>{pushError}</span>
          <button type="button" className="secondary" onClick={() => setPushError('')}>关闭</button>
        </div>
      )}
    </div>
  );
}
