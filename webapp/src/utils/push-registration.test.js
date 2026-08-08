vi.mock('../api', () => ({
  api: {
    subscribePush: vi.fn(),
    unsubscribePush: vi.fn(),
  },
  getPushRegistrationID: vi.fn(() => 'registration-current'),
  getToken: vi.fn(() => 'token-at-start'),
  setWSPushSubscriptionEndpoint: vi.fn(),
}));

vi.mock('./push-notifications', () => ({
  ensurePushSubscription: vi.fn(),
  serializePushSubscription: vi.fn((subscription) => ({ endpoint: subscription.endpoint })),
}));

import { api, setWSPushSubscriptionEndpoint } from '../api';
import { ensurePushSubscription } from './push-notifications';
import { registerBrowserPush } from './push-registration';

describe('registerBrowserPush', () => {
  const subscription = { endpoint: 'https://push.example/subscription' };

  beforeEach(() => {
    ensurePushSubscription.mockResolvedValue(subscription);
    api.subscribePush.mockResolvedValue({ subscribed: true });
    api.unsubscribePush.mockResolvedValue({ subscribed: false });
    setWSPushSubscriptionEndpoint.mockResolvedValue('subscription-id');
  });

  afterEach(() => vi.clearAllMocks());

  it('removes a completed server registration when the session changes', async () => {
    let current = true;
    api.subscribePush.mockImplementation(async () => {
      current = false;
      return { subscribed: true };
    });

    await expect(registerBrowserPush({
      publicKey: 'public-key',
      isCurrent: () => current,
    })).resolves.toBeNull();

    expect(api.unsubscribePush).toHaveBeenCalledWith(
      subscription.endpoint,
      'token-at-start',
      'registration-current',
    );
    expect(setWSPushSubscriptionEndpoint).not.toHaveBeenCalled();
  });
});
