import React, {
  useCallback, useEffect, useRef, useState,
} from 'react';
import { Bell } from 'lucide-react';
import { registerSW } from 'virtual:pwa-register';
import {
  api,
  getPushRegistrationID,
  getToken,
  setWSPushSubscriptionEndpoint,
} from '../api';
import {
  canUsePush,
  ensurePushSubscription,
  pushDismissedStorageKey,
  serializePushSubscription,
  shouldOfferPush,
} from '../utils/push-notifications';
import { enqueuePushOperation } from '../utils/push-operation';
import { retryPendingPushUnsubscribe } from '../utils/push-session-cleanup';
import { pushTabCoordinator } from '../utils/push-tab-coordination';
import './pwa-controller.css';

const PUSH_CLEANUP_RETRY_DELAY_MS = 30_000;

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
  const [pushConfig, setPushConfig] = useState(null);
  const [busy, setBusy] = useState(false);
  const [pushError, setPushError] = useState('');
  const [needRefresh, setNeedRefresh] = useState(false);
  const [reconcileVersion, setReconcileVersion] = useState(0);
  const [cleanupRetryVersion, setCleanupRetryVersion] = useState(0);
  const updateServiceWorkerRef = useRef(null);

  useEffect(() => {
    if (updateServiceWorkerRef.current) return;
    updateServiceWorkerRef.current = registerSW({
      immediate: true,
      onNeedRefresh: () => setNeedRefresh(true),
      onRegisterError: (error) => console.warn('PWA registration failed:', error),
    });
  }, []);

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
    setDismissed(readDismissed(pushPromptOwner));
    setPushError('');
  }, [pushPromptOwner]);

  useEffect(() => {
    const active = loggedIn && canUsePush() && Notification.permission === 'granted';
    const registrationID = active ? getPushRegistrationID() : '';
    pushTabCoordinator.setActive(
      active,
      registrationID,
    );
    return () => pushTabCoordinator.setActive(false, registrationID);
  }, [loggedIn, permission, sessionRevision]);

  useEffect(() => {
    if (!loggedIn || typeof pushTabCoordinator.onReconcile !== 'function') return undefined;
    return pushTabCoordinator.onReconcile(() => {
      setReconcileVersion((current) => current + 1);
    });
  }, [loggedIn]);

  useEffect(() => {
    if (loggedIn || !online || !canUsePush()) return undefined;
    let cancelled = false;
    let retryTimer;
    const retryPendingCleanup = async () => {
      const cleaned = await retryPendingPushUnsubscribe({
        coordinator: pushTabCoordinator,
        isLoggedOut: () => !getToken(),
      });
      if (!cleaned && !cancelled && !getToken()) {
        retryTimer = window.setTimeout(() => {
          setCleanupRetryVersion((current) => current + 1);
        }, PUSH_CLEANUP_RETRY_DELAY_MS);
      }
    };
    enqueuePushOperation(retryPendingCleanup).catch((error) => {
      console.warn('Pending push cleanup retry failed:', error);
    });
    return () => {
      cancelled = true;
      window.clearTimeout(retryTimer);
    };
  }, [cleanupRetryVersion, loggedIn, online]);

  useEffect(() => {
    if (!loggedIn || !online || !canUsePush()) {
      setPushConfig(null);
      return undefined;
    }

    let cancelled = false;
    const controller = new AbortController();
    setPushConfig(null);
    api.getPushConfig(controller.signal)
      .then((config) => {
        if (cancelled) return;
        const publicKey = String(config?.public_key || '').trim();
        setPushConfig(config?.enabled && publicKey ? {
          enabled: true,
          public_key: publicKey,
        } : null);
      })
      .catch((error) => {
        if (!cancelled && error?.name !== 'AbortError') {
          console.warn('Push configuration lookup failed:', error);
        }
        if (!cancelled) setPushConfig(null);
      });

    return () => {
      cancelled = true;
      controller.abort();
    };
  }, [loggedIn, online, sessionRevision]);

  useEffect(() => {
    const publicKey = pushConfig?.public_key;
    if (!canUsePush() || !loggedIn || Notification.permission !== 'granted' || !publicKey) return undefined;
    let cancelled = false;
    const isCurrent = () => (
      !cancelled && sessionRevisionRef.current === sessionRevision
    );

    const controller = new AbortController();
    const reconcilePush = async () => {
      try {
        const registrationID = getPushRegistrationID();
        const activeLockReady = await pushTabCoordinator.waitUntilActive?.(registrationID);
        if (activeLockReady === false || !isCurrent()) return;
        const subscription = await ensurePushSubscription(
          publicKey,
          (endpoint) => api.unsubscribePush(endpoint, undefined, registrationID),
          isCurrent,
        );
        if (!subscription || !isCurrent()) return;
        await setWSPushSubscriptionEndpoint(subscription.endpoint);
        if (!isCurrent()) return;
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
  }, [loggedIn, pushConfig, reconcileVersion, sessionRevision]);

  const offerPush = Boolean(pushConfig?.enabled && pushConfig.public_key)
    && shouldOfferPush({ loggedIn, permission, dismissed });

  const dismissPush = useCallback(() => {
    persistDismissed(pushPromptOwner);
    setDismissed(true);
  }, [pushPromptOwner]);

  const enablePush = useCallback(async () => {
    const publicKey = pushConfig?.public_key;
    if (!canUsePush() || busy || !pushConfig?.enabled || !publicKey) return;
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
      // iOS only allows the permission prompt when it is directly caused by a
      // user gesture. Do this before any network or lock await so Safari does
      // not lose the transient activation while we fetch the VAPID config.
      const nextPermission = await Notification.requestPermission();
      if (!isCurrent()) return;
      setPermission(nextPermission);
      if (nextPermission !== 'granted') {
        persistDismissed(pushPromptOwner);
        setDismissed(true);
        return;
      }

      await enqueuePushOperation(async () => {
        const registrationID = getPushRegistrationID();

        pushTabCoordinator.setActive(true, registrationID);
        const activeLockReady = await pushTabCoordinator.waitUntilActive?.(registrationID);
        if (activeLockReady === false || !isCurrent()) return;

        const subscription = await ensurePushSubscription(
          publicKey,
          (endpoint) => api.unsubscribePush(endpoint, undefined, registrationID),
          isCurrent,
        );
        if (!subscription || !isCurrent()) return;
        await setWSPushSubscriptionEndpoint(subscription.endpoint);
        if (!isCurrent()) return;
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
  }, [busy, pushConfig, pushPromptOwner, sessionRevision]);

  return (
    <div className="cc-pwa-status" aria-live="polite">
      {!online && <div className="cc-pwa-offline">当前离线，消息将在网络恢复后重新加载</div>}
      {needRefresh && (
        <div className="cc-pwa-prompt cc-pwa-prompt--compact">
          <div className="cc-pwa-prompt-copy">
            <strong>发现新版本</strong>
          </div>
          <div className="cc-pwa-prompt-actions">
            <button type="button" onClick={() => updateServiceWorkerRef.current?.(true)}>立即更新</button>
            <button type="button" className="secondary" onClick={() => setNeedRefresh(false)}>稍后</button>
          </div>
        </div>
      )}
      {offerPush && (
        <aside className="cc-pwa-prompt cc-pwa-prompt--push" aria-label="消息通知设置">
          <span className="cc-pwa-prompt-icon" aria-hidden="true">
            <Bell size={18} strokeWidth={2.2} />
          </span>
          <div className="cc-pwa-prompt-copy">
            <strong>开启通知，及时收到新消息</strong>
            <span>离开页面后，也能收到新消息提醒</span>
          </div>
          <div className="cc-pwa-prompt-actions">
            <button type="button" disabled={busy} onClick={enablePush}>{busy ? '开启中' : '开启'}</button>
            <button type="button" className="secondary" disabled={busy} onClick={dismissPush}>暂不</button>
          </div>
        </aside>
      )}
      {pushError && (
        <div className="cc-pwa-prompt cc-pwa-prompt--error" role="alert">
          <div className="cc-pwa-prompt-copy">
            <strong>通知未开启</strong>
            <span>{pushError}</span>
          </div>
          <div className="cc-pwa-prompt-actions">
            <button type="button" className="secondary" onClick={() => setPushError('')}>关闭</button>
          </div>
        </div>
      )}
    </div>
  );
}
