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
  ensurePushSubscription,
  getPushSubscription,
  readPushEnabled,
  serializePushSubscription,
  writePushEnabled,
} from '../utils/push-notifications';
import { enqueuePushOperation } from '../utils/push-operation';

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
  const message = String(error?.message || '');
  if (message.includes('no active push subscription')) {
    return '当前设备没有有效通知订阅，请关闭后重新开启通知再试。';
  }
  if (message.includes('provider rejected')) {
    return '推送服务未接受测试通知，请稍后重试。';
  }
  return message || '测试通知发送失败，请稍后重试。';
}

async function registerCurrentBrowser() {
  const registrationID = getPushRegistrationID();
  const config = await api.getPushConfig();
  if (!config.enabled || !config.public_key) throw new Error('推送服务尚未配置。');
  const subscription = await ensurePushSubscription(
    config.public_key,
    (endpoint) => api.unsubscribePush(endpoint, undefined, registrationID),
  );
  if (!subscription) throw new Error('未能创建浏览器通知订阅。');
  await api.subscribePush(serializePushSubscription(subscription), registrationID);
  await setWSPushSubscriptionEndpoint(subscription.endpoint);
  return registrationID;
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
    const inspect = async () => {
      if (!supported || !readPushEnabled(owner)) {
        if (!cancelled) {
          setEnabled(false);
          setLoading(false);
        }
        return;
      }
      try {
        const subscription = await getPushSubscription();
        if (!cancelled) setEnabled(Boolean(subscription));
      } catch {
        if (!cancelled) setEnabled(false);
      } finally {
        if (!cancelled) setLoading(false);
      }
    };
    inspect();
    return () => { cancelled = true; };
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
      writePushEnabled(owner, false);
      setEnabled(false);
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
        const subscription = await getPushSubscription();
        if (subscription) {
          await Promise.allSettled([
            api.unsubscribePush(subscription.endpoint),
            subscription.unsubscribe(),
          ]);
        }
        await setWSPushSubscriptionEndpoint('');
      });
      setMessage('已在当前设备关闭消息通知。');
    } catch (err) {
      setError(err?.message || '通知已关闭，但订阅清理未完成。');
    } finally {
      setBusy(false);
    }
  };

  const handleToggle = () => {
    if (busy || loading || !supported || permission === 'denied') return;
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
        await api.verifyPush(registrationID);
      });
      setMessage('验证通知已发送。若本机通知可见但验证通知未到达，说明当前设备的后台推送通道可能不可用。');
    } catch (err) {
      setError(testErrorCopy(err));
    } finally {
      setTesting(false);
    }
  };

  const testLocalDisplay = async () => {
    setDisplayTesting(true);
    setMessage('');
    setError('');
    try {
      const registration = await navigator.serviceWorker.ready;
      if (typeof registration.showNotification !== 'function') {
        throw new Error('notification display unavailable');
      }
      await registration.showNotification('CatsCo 本机显示测试', {
        body: '如果你看到这条本机通知，说明浏览器与系统可以显示通知。',
        icon: '/pwa-192x192.png',
        badge: '/pwa-notification-badge-96x96.png',
        tag: `catsco-local-display-test-${Date.now()}`,
        data: { url: '/' },
      });
      setMessage('已请求本机显示通知。若仍未看到，请检查 Chrome 通知权限、系统通知设置与专注模式。');
    } catch {
      setError('本机通知无法显示，请检查 Chrome 与系统通知权限后再试。');
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
          disabled={busy || loading || !supported || permission === 'denied'}
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
          onClick={testLocalDisplay}
        >
          {displayTesting ? <LoaderCircle className="oc-spin" size={15} aria-hidden="true" /> : <MonitorCheck size={15} aria-hidden="true" />}
          {displayTesting ? '测试中' : '测试本机显示'}
        </button>
        <button
          type="button"
          className="oc-btn oc-btn-default oc-notification-test"
          disabled={!enabled || busy || displayTesting || testing}
          onClick={sendTestNotification}
        >
          {testing ? <LoaderCircle className="oc-spin" size={15} aria-hidden="true" /> : <Send size={15} aria-hidden="true" />}
          {testing ? '发送中' : '验证后台通知'}
        </button>
      </div>
      {message && <div className="oc-notification-feedback is-success" role="status">{message}</div>}
      {error && <div className="oc-notification-feedback is-error" role="alert">{error}</div>}
    </div>
  );
}
