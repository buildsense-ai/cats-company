import React, { act } from 'react';
import { createRoot } from 'react-dom/client';
import { Simulate } from 'react-dom/test-utils';

vi.mock('../api', () => ({
  api: {
    getPushConfig: vi.fn(),
    subscribePush: vi.fn(),
    unsubscribePush: vi.fn(),
    sendPushTest: vi.fn(),
  },
  getPushRegistrationID: vi.fn(() => 'registration-current'),
  setWSPushSubscriptionEndpoint: vi.fn(() => Promise.resolve('subscription-id')),
}));

vi.mock('../utils/push-operation', () => ({
  enqueuePushOperation: vi.fn((operation) => operation()),
}));

import { api, setWSPushSubscriptionEndpoint } from '../api';
import NotificationSettings from './notification-settings';

describe('NotificationSettings', () => {
  let container;
  let root;
  let subscription;

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
    expect(api.unsubscribePush).toHaveBeenCalledWith(subscription.endpoint);
    expect(setWSPushSubscriptionEndpoint).toHaveBeenCalledWith('');
    expect(localStorage.getItem('cc_push_enabled_v1:user:7')).toBe('false');
    expect(container.textContent).toContain('已在当前设备关闭消息通知');
  });

  it('reports incomplete cleanup and remembers a failed browser unsubscribe', async () => {
    subscription.unsubscribe.mockResolvedValue(false);
    await renderSettings();

    await act(async () => {
      Simulate.click(container.querySelector('[role="switch"]'));
      await Promise.resolve();
    });

    await vi.waitFor(() => expect(container.textContent).toContain('订阅清理未完成'));
    expect(localStorage.getItem('oc_push_pending_unsubscribe_v1')).toBe(subscription.endpoint);
    expect(container.textContent).not.toContain('已在当前设备关闭消息通知');
  });

  it('reports incomplete cleanup when the server unsubscribe fails', async () => {
    api.unsubscribePush.mockRejectedValue(new Error('server unavailable'));
    await renderSettings();

    await act(async () => {
      Simulate.click(container.querySelector('[role="switch"]'));
      await Promise.resolve();
    });

    await vi.waitFor(() => expect(container.textContent).toContain('订阅清理未完成'));
    expect(subscription.unsubscribe).toHaveBeenCalledTimes(1);
    expect(container.textContent).not.toContain('已在当前设备关闭消息通知');
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
