import React, {
  useCallback, useEffect, useRef, useState,
} from 'react';
import { Bell } from 'lucide-react';
import { registerSW } from 'virtual:pwa-register';
import {
  api,
  getPushRegistrationID,
} from '../api';
import {
  canUsePush,
  pushDismissedStorageKey,
  pushEnabledStorageKey,
  readPushEnabled,
  shouldOfferPush,
  writePushEnabled,
} from '../utils/push-notifications';
import { enqueuePushOperation } from '../utils/push-operation';
import { registerBrowserPush } from '../utils/push-registration';
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
  const [pushEnabled, setPushEnabled] = useState(() => readPushEnabled(pushPromptOwner));
  const [permission, setPermission] = useState(() => (
    'Notification' in window ? Notification.permission : 'unsupported'
  ));
  const [pushConfig, setPushConfig] = useState(null);
  const [busy, setBusy] = useState(false);
  const [pushError, setPushError] = useState('');
  const [needRefresh, setNeedRefresh] = useState(false);
  const [reconcileVersion, setReconcileVersion] = useState(0);
  const updateServiceWorkerRef = useRef(null);

  useEffect(() => {
    if (updateServiceWorkerRef.current) return;
    updateServiceWorkerRef.current = registerSW({
      immediate: true,
      onNeedRefresh: () => {
        // Activate transport fixes immediately. Otherwise the new WebApp can
        // keep running behind an older worker that still clones POST bodies.
        Promise.resolve().then(() => {
          const updateServiceWorker = updateServiceWorkerRef.current;
          if (updateServiceWorker) updateServiceWorker(true);
          else setNeedRefresh(true);
        });
      },
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
    setPushEnabled(readPushEnabled(pushPromptOwner));
    setPushError('');
  }, [pushPromptOwner]);

  useEffect(() => {
    const syncPreference = () => {
      setPushEnabled(readPushEnabled(pushPromptOwner));
      setPermission('Notification' in window ? Notification.permission : 'unsupported');
    };
    const handlePreferenceChanged = (event) => {
      if (event.detail?.owner && event.detail.owner !== pushPromptOwner) return;
      syncPreference();
    };
    const handleStorage = (event) => {
      if (event.key === pushEnabledStorageKey(pushPromptOwner)) syncPreference();
    };
    window.addEventListener('cc:push-preference-changed', handlePreferenceChanged);
    window.addEventListener('storage', handleStorage);
    return () => {
      window.removeEventListener('cc:push-preference-changed', handlePreferenceChanged);
      window.removeEventListener('storage', handleStorage);
    };
  }, [pushPromptOwner]);

  useEffect(() => {
    const active = loggedIn && pushEnabled && canUsePush() && Notification.permission === 'granted';
    const registrationID = active ? getPushRegistrationID() : '';
    pushTabCoordinator.setActive(
      active,
      registrationID,
    );
    return () => pushTabCoordinator.setActive(false, registrationID);
  }, [loggedIn, permission, pushEnabled, sessionRevision]);

  useEffect(() => {
    if (!loggedIn || typeof pushTabCoordinator.onReconcile !== 'function') return undefined;
    return pushTabCoordinator.onReconcile(() => {
      setReconcileVersion((current) => current + 1);
    });
  }, [loggedIn]);

  useEffect(() => {
    if (!pushEnabled || !loggedIn || !online || !canUsePush()) {
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
  }, [loggedIn, online, pushEnabled, sessionRevision]);

  useEffect(() => {
    const publicKey = pushConfig?.public_key;
    if (!pushEnabled || !canUsePush() || !loggedIn || Notification.permission !== 'granted') return undefined;
    if (!publicKey) return undefined;
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
        const registration = await registerBrowserPush({
          publicKey,
          registrationID,
          signal: controller.signal,
          isCurrent,
        });
        if (!registration || !isCurrent()) return;
      } catch (error) {
        if (!cancelled) console.warn('Push subscription reconciliation failed:', error);
      }
    };
    enqueuePushOperation(reconcilePush);
    return () => {
      cancelled = true;
      controller.abort();
    };
  }, [loggedIn, pushConfig, pushEnabled, reconcileVersion, sessionRevision]);

  const offerPush = Boolean(pushConfig?.enabled && pushConfig.public_key)
    && pushEnabled
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
        writePushEnabled(pushPromptOwner, false);
        setPushEnabled(false);
        persistDismissed(pushPromptOwner);
        setDismissed(true);
        return;
      }

      await enqueuePushOperation(async () => {
        const registrationID = getPushRegistrationID();

        pushTabCoordinator.setActive(true, registrationID);
        const activeLockReady = await pushTabCoordinator.waitUntilActive?.(registrationID);
        if (activeLockReady === false || !isCurrent()) return;

        const registration = await registerBrowserPush({
          publicKey,
          registrationID,
          signal: controller.signal,
          isCurrent,
        });
        if (!registration || !isCurrent()) return;
        if (isCurrent()) {
          writePushEnabled(pushPromptOwner, true);
          setPushEnabled(true);
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
