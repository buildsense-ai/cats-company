import {
  api,
  getPushRegistrationID,
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
  const subscription = await ensurePushSubscription(
    publicKey,
    (endpoint) => api.unsubscribePush(endpoint, undefined, registrationID),
    isCurrent,
  );
  if (!subscription || !isCurrent()) return null;

  const serialized = serializePushSubscription(subscription);
  if (signal) await api.subscribePush(serialized, registrationID, signal);
  else await api.subscribePush(serialized, registrationID);
  if (!isCurrent()) return null;
  await setWSPushSubscriptionEndpoint(subscription.endpoint);
  return { registrationID, subscription };
}
