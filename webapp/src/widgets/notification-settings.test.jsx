import React, { act } from 'react';
import { createRoot } from 'react-dom/client';
import { Simulate } from 'react-dom/test-utils';

vi.mock('../api', () => ({
  api: {
    getPushConfig: vi.fn(),
    subscribePush: vi.fn(),
    unsubscribePush: vi.fn(),
    testPush: vi.fn(),
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
    api.testPush.mockResolvedValue({ accepted: true });
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

  it('sends a real test to the current browser registration and explains delivery uncertainty', async () => {
    await renderSettings();
    const testButton = Array.from(container.querySelectorAll('button'))
      .find((button) => button.textContent.includes('测试后台推送'));

    await act(async () => {
      Simulate.click(testButton);
      await Promise.resolve();
    });

    expect(api.testPush).toHaveBeenCalledWith('registration-current');
    expect(api.subscribePush).toHaveBeenCalledWith({
      endpoint: subscription.endpoint,
      keys: subscription.keys,
    }, 'registration-current');
    expect(container.textContent).toContain('后台测试通知已发送');
    expect(container.textContent).toContain('当前设备的后台推送通道可能不可用');
    expect(container.textContent).toContain('部分国产 Android 手机');
  });

  it('tests notification display locally without calling the push provider', async () => {
    await renderSettings();
    const displayButton = Array.from(container.querySelectorAll('button'))
      .find((button) => button.textContent.includes('测试本机显示'));

    await act(async () => {
      Simulate.click(displayButton);
      await Promise.resolve();
    });

    expect(showNotification).toHaveBeenCalledWith('CatsCo 本机显示测试', expect.objectContaining({
      body: expect.stringContaining('本机通知'),
      tag: expect.stringMatching(/^catsco-local-display-test-\d+$/),
    }));
    expect(api.testPush).not.toHaveBeenCalled();
    expect(container.textContent).toContain('已请求本机显示通知');
  });

  it('reports a local display failure separately from push delivery', async () => {
    showNotification.mockRejectedValue(new Error('display blocked'));
    await renderSettings();
    const displayButton = Array.from(container.querySelectorAll('button'))
      .find((button) => button.textContent.includes('测试本机显示'));

    await act(async () => {
      Simulate.click(displayButton);
      await Promise.resolve();
    });

    expect(container.textContent).toContain('本机通知无法显示');
    expect(api.testPush).not.toHaveBeenCalled();
  });
});
