import React, { useEffect, useState } from 'react';
import {
  Bell,
  CircleAlert,
  LoaderCircle,
  MonitorCheck,
  Send,
} from 'lucide-react';
import {
  api,
  getPushRegistrationID,
  setWSPushSubscriptionEndpoint,
} from '../api';
import {
  canUsePush,
  getPushSubscription,
  pushEnabledStorageKey,
  readPushEnabled,
  writePushEnabled,
} from '../utils/push-notifications';
import { enqueuePushOperation } from '../utils/push-operation';
import { registerBrowserPush } from '../utils/push-registration';
import {
  clearPendingPushUnsubscribe,
  rememberPendingPushUnsubscribe,
} from '../utils/push-session-cleanup';
import { pushTabCoordinator } from '../utils/push-tab-coordination';

function pushOwnerForUser(user) {
  const uid = user?.uid || user?.id;
  return uid ? `user:${uid}` : '';
}

function notifyPreferenceChanged(owner) {
  window.dispatchEvent(new CustomEvent('cc:push-preference-changed', {
    detail: { owner },
  }));
}

function statusCopy({ supported, permission, enabled }) {
  if (!supported) return '当前浏览器不支持消息通知。iPhone 或 iPad 需先将 CatsCo 添加到主屏幕。';
  if (permission === 'denied') return '通知已被系统或浏览器阻止，请在设备设置中重新授权。';
  if (enabled) return '已为当前设备与浏览器开启，离开页面后也可接收消息提醒。';
  return '当前设备不会在后台接收 CatsCo 消息提醒。';
}

function testErrorCopy(error) {
  const code = error?.data?.code || error?.code;
  if (code === 'push_subscription_missing') {
    return '当前设备没有有效通知订阅，请关闭后重新开启通知再试。';
  }
  if (code === 'push_subscription_expired') {
    return '当前设备的通知订阅已失效，请关闭后重新开启通知再试。';
  }
  if (code === 'push_provider_rejected') {
    return '推送服务未接受测试通知，请稍后重试。';
  }
  return error?.message || '测试通知发送失败，请稍后重试。';
}

async function registerCurrentBrowser() {
  const config = await api.getPushConfig();
  if (!config.enabled || !config.public_key) throw new Error('推送服务尚未配置。');
  const registration = await registerBrowserPush({ publicKey: config.public_key });
  if (!registration) throw new Error('未能创建浏览器通知订阅。');
  return registration.registrationID;
}

export default function NotificationSettings({ user }) {
  const owner = pushOwnerForUser(user);
  const supported = canUsePush();
  const [permission, setPermission] = useState(() => (
    'Notification' in window ? Notification.permission : 'unsupported'
  ));
  const [enabled, setEnabled] = useState(false);
  const [loading, setLoading] = useState(true);
  const [busy, setBusy] = useState(false);
  const [displayTesting, setDisplayTesting] = useState(false);
  const [testing, setTesting] = useState(false);
  const [message, setMessage] = useState('');
  const [error, setError] = useState('');

  useEffect(() => {
    let cancelled = false;
    let inspectionVersion = 0;
    const inspect = async () => {
      const version = ++inspectionVersion;
      const isCurrent = () => !cancelled && version === inspectionVersion;
      if (isCurrent()) setLoading(true);
      if (!supported || !readPushEnabled(owner)) {
        if (isCurrent()) {
          setEnabled(false);
          setLoading(false);
        }
        return;
      }
      try {
        const subscription = await getPushSubscription();
        if (isCurrent()) setEnabled(Boolean(subscription));
      } catch {
        if (isCurrent()) setEnabled(false);
      } finally {
        if (isCurrent()) setLoading(false);
      }
    };
    const handlePreferenceChanged = (event) => {
      if (event.detail?.owner && event.detail.owner !== owner) return;
      inspect();
    };
    const handleStorage = (event) => {
      if (event.key === pushEnabledStorageKey(owner)) inspect();
    };
    inspect();
    window.addEventListener('cc:push-preference-changed', handlePreferenceChanged);
    window.addEventListener('storage', handleStorage);
    return () => {
      cancelled = true;
      window.removeEventListener('cc:push-preference-changed', handlePreferenceChanged);
      window.removeEventListener('storage', handleStorage);
    };
  }, [owner, supported]);

  const enableNotifications = async () => {
    if (!supported) return;
    setBusy(true);
    setMessage('');
    setError('');
    try {
      const nextPermission = Notification.permission === 'granted'
        ? 'granted'
        : await Notification.requestPermission();
      setPermission(nextPermission);
      if (nextPermission !== 'granted') {
        writePushEnabled(owner, false);
        setEnabled(false);
        notifyPreferenceChanged(owner);
        setError('没有获得通知权限，请在系统或浏览器设置中允许 CatsCo 发送通知。');
        return;
      }

      await enqueuePushOperation(registerCurrentBrowser);

      writePushEnabled(owner, true);
      setEnabled(true);
      setMessage('已在当前设备开启消息通知。');
      notifyPreferenceChanged(owner);
    } catch (err) {
      const partiallyRegistered = err?.code === 'PUSH_REGISTRATION_PARTIAL';
      writePushEnabled(owner, partiallyRegistered);
      setEnabled(partiallyRegistered);
      notifyPreferenceChanged(owner);
      setError(err?.message || '通知开启失败，请稍后重试。');
    } finally {
      setBusy(false);
    }
  };

  const disableNotifications = async () => {
    setBusy(true);
    setMessage('');
    setError('');
    writePushEnabled(owner, false);
    setEnabled(false);
    notifyPreferenceChanged(owner);
    try {
      await enqueuePushOperation(async () => {
        const registrationID = getPushRegistrationID();
        const subscription = await getPushSubscription();
        if (subscription) {
          pushTabCoordinator.setActive(false, registrationID);
          let serverRemoved = false;
          try {
            await api.unsubscribeAllPushRegistrations(subscription.endpoint);
            serverRemoved = true;
          } catch {
            // A successful browser unsubscribe is also sufficient to stop delivery.
          }
          const browserRemoved = await pushTabCoordinator.runWhenNoOtherActiveTabs(async () => {
            try {
              const removed = (await subscription.unsubscribe()) === true;
              if (removed) clearPendingPushUnsubscribe(subscription.endpoint);
              else rememberPendingPushUnsubscribe(subscription.endpoint);
              return removed;
            } catch {
              rememberPendingPushUnsubscribe(subscription.endpoint);
              return false;
            }
          });
          if (!serverRemoved && !browserRemoved) {
            pushTabCoordinator.requestReconcile?.();
            throw new Error('通知关闭失败，请稍后重试。');
          }
        } else {
          await api.unsubscribePushRegistration(registrationID).catch(() => {});
        }
        await setWSPushSubscriptionEndpoint('').catch(() => {});
      });
      setMessage('已在当前设备关闭消息通知。');
    } catch (err) {
      writePushEnabled(owner, true);
      setEnabled(true);
      notifyPreferenceChanged(owner);
      setError(err?.message || '通知关闭失败，请稍后重试。');
    } finally {
      setBusy(false);
    }
  };

  const handleToggle = () => {
    if (busy || loading || displayTesting || testing || !supported || (permission === 'denied' && !enabled)) return;
    if (enabled) disableNotifications();
    else enableNotifications();
  };

  const sendTestNotification = async () => {
    setTesting(true);
    setMessage('');
    setError('');
    try {
      await enqueuePushOperation(async () => {
        const registrationID = await registerCurrentBrowser();
        await api.sendPushTest(registrationID);
      });
      setMessage('测试通知已交给推送服务。请切到后台或锁屏确认是否收到；未收到通常表示当前设备环境不可用。');
    } catch (err) {
      setError(testErrorCopy(err));
    } finally {
      setTesting(false);
    }
  };

  const testBrowserNotification = async () => {
    setDisplayTesting(true);
    setMessage('');
    setError('');
    try {
      const registration = await navigator.serviceWorker.ready;
      if (typeof registration.showNotification !== 'function') {
        throw new Error('notification display unavailable');
      }
      await registration.showNotification('CatsCo 浏览器通知测试', {
        body: '这条通知只验证浏览器与系统能否显示通知，不代表后台推送链路可用。',
        icon: '/pwa-192x192.png',
        badge: '/pwa-notification-badge-96x96.png',
        tag: `catsco-browser-display-test-${Date.now()}`,
        data: { url: '/' },
      });
      setMessage('已请求浏览器显示通知。若未看到，请检查浏览器与系统通知权限。');
    } catch {
      setError('浏览器通知无法显示，请检查浏览器与系统通知权限后再试。');
    } finally {
      setDisplayTesting(false);
    }
  };

  return (
    <div className="oc-settings-section oc-notification-settings">
      <div className="oc-settings-section-title">消息通知</div>
      <div className="oc-notification-row">
        <span className="oc-notification-icon" aria-hidden="true"><Bell size={18} /></span>
        <div className="oc-settings-list-text">
          <div className="oc-notification-label">接收消息通知</div>
          <div className="oc-settings-secondary">
            {loading ? '正在检查当前设备...' : statusCopy({ supported, permission, enabled })}
          </div>
        </div>
        <button
          type="button"
          className="oc-settings-switch"
          role="switch"
          aria-checked={enabled}
          aria-label="接收消息通知"
          disabled={busy || loading || displayTesting || testing || !supported || (permission === 'denied' && !enabled)}
          onClick={handleToggle}
        >
          <span aria-hidden="true" />
        </button>
      </div>
      <div className="oc-notification-warning">
        <CircleAlert size={16} aria-hidden="true" />
        <span>部分国产 Android 手机可能因浏览器、系统推送通道或 Google 服务不可用而收不到通知。测试结果以当前设备实际收到为准。</span>
      </div>
      <div className="oc-notification-test-row">
        <button
          type="button"
          className="oc-btn oc-btn-default oc-notification-test"
          disabled={!enabled || busy || displayTesting || testing}
          onClick={testBrowserNotification}
        >
          {displayTesting ? <LoaderCircle className="oc-spin" size={15} aria-hidden="true" /> : <MonitorCheck size={15} aria-hidden="true" />}
          {displayTesting ? '测试中' : '测试浏览器通知'}
        </button>
        <button
          type="button"
          className="oc-btn oc-btn-default oc-notification-test"
          disabled={!enabled || busy || displayTesting || testing}
          onClick={sendTestNotification}
        >
          {testing ? <LoaderCircle className="oc-spin" size={15} aria-hidden="true" /> : <Send size={15} aria-hidden="true" />}
          {testing ? '发送中' : '发送测试通知'}
        </button>
      </div>
      {message && <div className="oc-notification-feedback is-success" role="status">{message}</div>}
      {error && <div className="oc-notification-feedback is-error" role="alert">{error}</div>}
    </div>
  );
}
