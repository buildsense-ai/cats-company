import React, { useCallback, useEffect, useMemo, useState } from 'react';
import { registerSW } from 'virtual:pwa-register';
import { api } from '../api';
import {
  canUsePush,
  cleanupPushSubscription,
  ensurePushSubscription,
  PUSH_DISMISSED_KEY,
  serializePushSubscription,
  shouldOfferPush,
} from '../utils/push-notifications';
import './pwa-controller.css';

function readDismissed() {
  return localStorage.getItem(PUSH_DISMISSED_KEY) === 'true';
}

export default function PwaController({ loggedIn }) {
  const [online, setOnline] = useState(() => navigator.onLine);
  const [dismissed, setDismissed] = useState(readDismissed);
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
    if (!canUsePush()) return undefined;
    if (!loggedIn) {
      cleanupPushSubscription().catch((error) => {
        console.warn('Push cleanup after logout failed:', error);
      });
      return undefined;
    }
    if (Notification.permission !== 'granted') return undefined;

    let cancelled = false;
    const reconcilePush = async () => {
      try {
        const config = await api.getPushConfig();
        const publicKey = config.public_key || config.vapid_public_key || config.vapidPublicKey;
        if (!config.enabled || !publicKey) return;
        const subscription = await ensurePushSubscription(
          publicKey,
          (endpoint) => api.unsubscribePush(endpoint),
        );
        await api.subscribePush(serializePushSubscription(subscription));
      } catch (error) {
        if (!cancelled) console.warn('Push subscription reconciliation failed:', error);
      }
    };
    reconcilePush();
    return () => {
      cancelled = true;
    };
  }, [loggedIn]);

  const offerPush = shouldOfferPush({ loggedIn, permission, dismissed });

  const dismissPush = useCallback(() => {
    localStorage.setItem(PUSH_DISMISSED_KEY, 'true');
    setDismissed(true);
  }, []);

  const enablePush = useCallback(async () => {
    if (!canUsePush() || busy) return;
    setBusy(true);
    setPushError('');
    try {
      const config = await api.getPushConfig();
      const publicKey = config.public_key || config.vapid_public_key || config.vapidPublicKey;
      if (!config.enabled || !publicKey) throw new Error('推送服务尚未配置');

      const nextPermission = await Notification.requestPermission();
      setPermission(nextPermission);
      if (nextPermission !== 'granted') {
        localStorage.setItem(PUSH_DISMISSED_KEY, 'true');
        setDismissed(true);
        return;
      }

      const subscription = await ensurePushSubscription(
        publicKey,
        (endpoint) => api.unsubscribePush(endpoint),
      );
      await api.subscribePush(serializePushSubscription(subscription));
      setDismissed(true);
    } catch (error) {
      setPushError(error?.message || '通知开启失败，请稍后重试');
    } finally {
      setBusy(false);
    }
  }, [busy]);

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
