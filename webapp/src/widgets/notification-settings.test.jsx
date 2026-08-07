import React, { act } from 'react';
import { createRoot } from 'react-dom/client';
import { Simulate } from 'react-dom/test-utils';

vi.mock('../api', () => ({
  api: {
    getPushConfig: vi.fn(),
    subscribePush: vi.fn(),
    unsubscribePush: vi.fn(),
    unsubscribeAllPushRegistrations: vi.fn(),
    unsubscribePushRegistration: vi.fn(),
    sendPushTest: vi.fn(),
  },
  getPushRegistrationID: vi.fn(() => 'registration-current'),
  getToken: vi.fn(() => 'token-current'),
  setWSPushSubscriptionEndpoint: vi.fn(() => Promise.resolve('subscription-id')),
}));

vi.mock('../utils/push-operation', () => ({
  enqueuePushOperation: vi.fn((operation) => operation()),
}));

vi.mock('../utils/push-tab-coordination', () => ({
  pushTabCoordinator: {
    setActive: vi.fn(),
    runWhenNoOtherActiveTabs: vi.fn((operation) => operation()),
    requestReconcile: vi.fn(),
  },
}));

import { api, setWSPushSubscriptionEndpoint } from '../api';
import { pushTabCoordinator } from '../utils/push-tab-coordination';
import NotificationSettings from './notification-settings';

describe('NotificationSettings', () => {
  let container;
  let root;
  let subscription;
  let showNotification;

  beforeEach(() => {
    global.IS_REACT_ACT_ENVIRONMENT = true;
    localStorage.clear();
    subscription = {
      endpoint: 'https://push.example/subscription',
      keys: { p256dh: 'key', auth: 'auth' },
      options: { applicationServerKey: new Uint8Array([1, 2, 3]).buffer },
      unsubscribe: vi.fn().mockResolvedValue(true),
      toJSON() {
        return { endpoint: this.endpoint, keys: this.keys };
      },
    };
    showNotification = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(window, 'isSecureContext', { configurable: true, value: true });
    Object.defineProperty(window, 'Notification', {
      configurable: true,
      value: { permission: 'granted', requestPermission: vi.fn().mockResolvedValue('granted') },
    });
    Object.defineProperty(window, 'PushManager', { configurable: true, value: function PushManager() {} });
    Object.defineProperty(navigator, 'locks', {
      configurable: true,
      value: { request: vi.fn((_name, operation) => operation()) },
    });
    Object.defineProperty(navigator, 'serviceWorker', {
      configurable: true,
      value: {
        getRegistration: vi.fn().mockResolvedValue({
          pushManager: { getSubscription: vi.fn().mockResolvedValue(subscription) },
        }),
        ready: Promise.resolve({
          showNotification,
          pushManager: {
            getSubscription: vi.fn().mockResolvedValue(subscription),
            subscribe: vi.fn(),
          },
        }),
      },
    });
    api.getPushConfig.mockResolvedValue({ enabled: true, public_key: 'AQID' });
    api.subscribePush.mockResolvedValue({ subscribed: true });
    api.unsubscribePush.mockResolvedValue({ subscribed: false });
    api.unsubscribeAllPushRegistrations.mockResolvedValue({ subscribed: false });
    api.unsubscribePushRegistration.mockResolvedValue({ subscribed: false });
    api.sendPushTest.mockResolvedValue({ accepted: true });
    container = document.createElement('div');
    document.body.appendChild(container);
    root = createRoot(container);
  });

  afterEach(async () => {
    await act(async () => root.unmount());
    container.remove();
    vi.clearAllMocks();
  });

  async function renderSettings() {
    await act(async () => {
      root.render(<NotificationSettings user={{ uid: 7 }} />);
    });
    await vi.waitFor(() => {
      expect(container.querySelector('[role="switch"]').getAttribute('aria-checked')).toBe('true');
    });
  }

  it('turns off and removes the current browser subscription', async () => {
    await renderSettings();

    await act(async () => {
      Simulate.click(container.querySelector('[role="switch"]'));
      await Promise.resolve();
    });

    await vi.waitFor(() => expect(subscription.unsubscribe).toHaveBeenCalledTimes(1));
    expect(api.unsubscribeAllPushRegistrations).toHaveBeenCalledWith(subscription.endpoint);
    expect(setWSPushSubscriptionEndpoint).toHaveBeenCalledWith('');
    expect(localStorage.getItem('cc_push_enabled_v1:user:7')).toBe('false');
    expect(container.textContent).toContain('已在当前设备关闭消息通知');
  });

  it('keeps the browser cleanup pending when the server record was removed', async () => {
    subscription.unsubscribe.mockResolvedValue(false);
    await renderSettings();

    await act(async () => {
      Simulate.click(container.querySelector('[role="switch"]'));
      await Promise.resolve();
    });

    await vi.waitFor(() => expect(container.textContent).toContain('已在当前设备关闭消息通知'));
    expect(localStorage.getItem('oc_push_pending_unsubscribe_v1')).toBe(subscription.endpoint);
  });

  it('closes notifications when browser cleanup succeeds after a server failure', async () => {
    api.unsubscribeAllPushRegistrations.mockRejectedValue(new Error('server unavailable'));
    await renderSettings();

    await act(async () => {
      Simulate.click(container.querySelector('[role="switch"]'));
      await Promise.resolve();
    });

    await vi.waitFor(() => expect(container.textContent).toContain('已在当前设备关闭消息通知'));
    expect(subscription.unsubscribe).toHaveBeenCalledTimes(1);
  });

  it('preserves the shared browser subscription while another tab is active', async () => {
    pushTabCoordinator.runWhenNoOtherActiveTabs.mockResolvedValueOnce(false);
    await renderSettings();

    await act(async () => {
      Simulate.click(container.querySelector('[role="switch"]'));
      await Promise.resolve();
    });

    await vi.waitFor(() => expect(container.textContent).toContain('已在当前设备关闭消息通知'));
    expect(api.unsubscribeAllPushRegistrations).toHaveBeenCalledWith(subscription.endpoint);
    expect(subscription.unsubscribe).not.toHaveBeenCalled();
  });

  it('cleans an orphaned server registration when the local subscription disappears', async () => {
    await renderSettings();
    navigator.serviceWorker.getRegistration.mockResolvedValueOnce(null);

    await act(async () => {
      Simulate.click(container.querySelector('[role="switch"]'));
      await Promise.resolve();
    });

    await vi.waitFor(() => expect(api.unsubscribePushRegistration).toHaveBeenCalledWith('registration-current'));
    expect(container.textContent).toContain('已在当前设备关闭消息通知');
  });

  it('restores the enabled state when neither cleanup path can run', async () => {
    api.unsubscribeAllPushRegistrations.mockRejectedValueOnce(new Error('server unavailable'));
    pushTabCoordinator.runWhenNoOtherActiveTabs.mockResolvedValueOnce(false);
    await renderSettings();

    await act(async () => {
      Simulate.click(container.querySelector('[role="switch"]'));
      await Promise.resolve();
    });

    await vi.waitFor(() => expect(container.textContent).toContain('通知关闭失败'));
    expect(container.querySelector('[role="switch"]').getAttribute('aria-checked')).toBe('true');
    expect(localStorage.getItem('cc_push_enabled_v1:user:7')).toBe('true');
    expect(subscription.unsubscribe).not.toHaveBeenCalled();
    expect(pushTabCoordinator.requestReconcile).toHaveBeenCalledTimes(1);
  });

  it('restores the enabled state when both cleanup operations fail', async () => {
    api.unsubscribeAllPushRegistrations.mockRejectedValueOnce(new Error('server unavailable'));
    subscription.unsubscribe.mockResolvedValueOnce(false);
    await renderSettings();

    await act(async () => {
      Simulate.click(container.querySelector('[role="switch"]'));
      await Promise.resolve();
    });

    await vi.waitFor(() => expect(container.textContent).toContain('通知关闭失败'));
    expect(container.querySelector('[role="switch"]').getAttribute('aria-checked')).toBe('true');
    expect(localStorage.getItem('cc_push_enabled_v1:user:7')).toBe('true');
  });

  it('allows an existing subscription to be disabled after permission is denied', async () => {
    window.Notification.permission = 'denied';
    await renderSettings();
    const toggle = container.querySelector('[role="switch"]');

    expect(toggle.disabled).toBe(false);
    await act(async () => {
      Simulate.click(toggle);
      await Promise.resolve();
    });

    await vi.waitFor(() => expect(subscription.unsubscribe).toHaveBeenCalledTimes(1));
    expect(toggle.getAttribute('aria-checked')).toBe('false');
  });

  it('synchronizes the switch when another tab changes the preference', async () => {
    await renderSettings();

    localStorage.setItem('cc_push_enabled_v1:user:7', 'false');
    await act(async () => {
      window.dispatchEvent(new StorageEvent('storage', {
        key: 'cc_push_enabled_v1:user:7',
        newValue: 'false',
      }));
    });

    await vi.waitFor(() => {
      expect(container.querySelector('[role="switch"]').getAttribute('aria-checked')).toBe('false');
    });
  });

  it('rolls back the server record when WebSocket registration fails', async () => {
    localStorage.setItem('cc_push_enabled_v1:user:7', 'false');
    setWSPushSubscriptionEndpoint.mockRejectedValueOnce(new Error('digest unavailable'));
    await act(async () => root.render(<NotificationSettings user={{ uid: 7 }} />));
    await vi.waitFor(() => {
      expect(container.querySelector('[role="switch"]').getAttribute('aria-checked')).toBe('false');
    });

    await act(async () => {
      Simulate.click(container.querySelector('[role="switch"]'));
      await Promise.resolve();
    });

    await vi.waitFor(() => expect(container.textContent).toContain('digest unavailable'));
    expect(api.unsubscribePush).toHaveBeenCalledWith(
      subscription.endpoint,
      'token-current',
      'registration-current',
    );
    expect(localStorage.getItem('cc_push_enabled_v1:user:7')).toBe('false');
  });

  it('keeps notifications enabled when registration rollback also fails', async () => {
    localStorage.setItem('cc_push_enabled_v1:user:7', 'false');
    setWSPushSubscriptionEndpoint.mockRejectedValueOnce(new Error('digest unavailable'));
    api.unsubscribePush.mockRejectedValueOnce(new Error('rollback unavailable'));
    await act(async () => root.render(<NotificationSettings user={{ uid: 7 }} />));
    await vi.waitFor(() => {
      expect(container.querySelector('[role="switch"]').getAttribute('aria-checked')).toBe('false');
    });

    await act(async () => {
      Simulate.click(container.querySelector('[role="switch"]'));
      await Promise.resolve();
    });

    await vi.waitFor(() => expect(container.textContent).toContain('通知已注册'));
    expect(container.querySelector('[role="switch"]').getAttribute('aria-checked')).toBe('true');
    expect(localStorage.getItem('cc_push_enabled_v1:user:7')).toBe('true');
  });

  it('sends a background push test for the current browser registration', async () => {
    await renderSettings();
    const testButton = Array.from(container.querySelectorAll('button'))
      .find((button) => button.textContent.includes('发送测试通知'));

    await act(async () => {
      Simulate.click(testButton);
      await Promise.resolve();
    });

    expect(api.sendPushTest).toHaveBeenCalledWith('registration-current');
    expect(api.subscribePush).toHaveBeenCalledWith({
      endpoint: subscription.endpoint,
      keys: subscription.keys,
    }, 'registration-current');
    expect(container.textContent).toContain('测试通知已交给推送服务');
    expect(container.textContent).toContain('未收到通常表示当前设备环境不可用');
    expect(container.textContent).toContain('部分国产 Android 手机');
  });

  it('tests browser notification display without calling the push provider', async () => {
    await renderSettings();
    const browserTestButton = Array.from(container.querySelectorAll('button'))
      .find((button) => button.textContent.includes('测试浏览器通知'));

    await act(async () => {
      Simulate.click(browserTestButton);
      await Promise.resolve();
    });

    expect(showNotification).toHaveBeenCalledWith('CatsCo 浏览器通知测试', expect.objectContaining({
      body: expect.stringContaining('不代表后台推送链路可用'),
      tag: expect.stringMatching(/^catsco-browser-display-test-\d+$/),
    }));
    expect(api.sendPushTest).not.toHaveBeenCalled();
    expect(container.textContent).toContain('已请求浏览器显示通知');
  });

  it('reports browser notification display failures separately', async () => {
    showNotification.mockRejectedValueOnce(new Error('display blocked'));
    await renderSettings();
    const browserTestButton = Array.from(container.querySelectorAll('button'))
      .find((button) => button.textContent.includes('测试浏览器通知'));

    await act(async () => {
      Simulate.click(browserTestButton);
      await Promise.resolve();
    });

    expect(container.textContent).toContain('浏览器通知无法显示');
    expect(api.sendPushTest).not.toHaveBeenCalled();
  });

  it('does not allow the notification switch to change while a test is sending', async () => {
    let finishTest;
    api.sendPushTest.mockImplementationOnce(() => new Promise((resolve) => {
      finishTest = resolve;
    }));
    await renderSettings();
    const testButton = Array.from(container.querySelectorAll('button'))
      .find((button) => button.textContent.includes('发送测试通知'));
    const toggle = container.querySelector('[role="switch"]');

    await act(async () => {
      Simulate.click(testButton);
      await Promise.resolve();
    });
    expect(toggle.disabled).toBe(true);

    Simulate.click(toggle);
    expect(api.unsubscribeAllPushRegistrations).not.toHaveBeenCalled();
    expect(toggle.getAttribute('aria-checked')).toBe('true');

    await act(async () => finishTest({ accepted: true }));
  });

  it.each([
    ['push_subscription_missing', '没有有效通知订阅'],
    ['push_subscription_expired', '通知订阅已失效'],
    ['push_provider_rejected', '推送服务未接受测试通知'],
  ])('maps the %s server code to actionable copy', async (code, copy) => {
    const error = new Error('backend prose may change');
    error.data = { code };
    api.sendPushTest.mockRejectedValue(error);
    await renderSettings();
    const testButton = Array.from(container.querySelectorAll('button'))
      .find((button) => button.textContent.includes('发送测试通知'));

    await act(async () => {
      Simulate.click(testButton);
      await Promise.resolve();
    });

    await vi.waitFor(() => expect(container.textContent).toContain(copy));
  });
});
