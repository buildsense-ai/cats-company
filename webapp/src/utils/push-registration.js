import {
  api,
  getPushRegistrationID,
  getToken,
  setWSPushSubscriptionEndpoint,
} from '../api';
import {
  ensurePushSubscription,
  serializePushSubscription,
} from './push-notifications';

export async function registerBrowserPush({
  publicKey,
  registrationID = getPushRegistrationID(),
  signal,
  isCurrent = () => true,
}) {
  const registrationToken = getToken();
  const subscription = await ensurePushSubscription(
    publicKey,
    (endpoint) => api.unsubscribePush(endpoint, undefined, registrationID),
    isCurrent,
  );
  if (!subscription || !isCurrent()) return null;

  const serialized = serializePushSubscription(subscription);
  if (signal) await api.subscribePush(serialized, registrationID, signal);
  else await api.subscribePush(serialized, registrationID);
  const rollbackRegistration = async (cause) => {
    try {
      await api.unsubscribePush(subscription.endpoint, registrationToken, registrationID);
    } catch (rollbackCause) {
      const error = new Error('通知已注册，但当前页面同步失败，请刷新后重试。');
      error.code = 'PUSH_REGISTRATION_PARTIAL';
      error.cause = cause;
      error.rollbackCause = rollbackCause;
      throw error;
    }
  };
  if (!isCurrent()) {
    await rollbackRegistration(new Error('push registration session changed'));
    return null;
  }
  try {
    await setWSPushSubscriptionEndpoint(subscription.endpoint);
  } catch (cause) {
    await rollbackRegistration(cause);
    throw cause;
  }
  return { registrationID, subscription };
}
