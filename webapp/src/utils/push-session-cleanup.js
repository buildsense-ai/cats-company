import { getPushSubscription } from './push-notifications';

const PENDING_PUSH_UNSUBSCRIBE_KEY = 'oc_push_pending_unsubscribe_v1';

function pendingPushUnsubscribeEndpoint() {
  try {
    return String(globalThis.localStorage?.getItem(PENDING_PUSH_UNSUBSCRIBE_KEY) || '').trim();
  } catch {
    return '';
  }
}

function rememberPendingPushUnsubscribe(endpoint) {
  const normalizedEndpoint = String(endpoint || '').trim();
  if (!normalizedEndpoint) return;
  try {
    globalThis.localStorage?.setItem(PENDING_PUSH_UNSUBSCRIBE_KEY, normalizedEndpoint);
  } catch {
    // A later session can still reclaim the browser endpoint on the server.
  }
}

function clearPendingPushUnsubscribe(endpoint = '') {
  const normalizedEndpoint = String(endpoint || '').trim();
  try {
    const pendingEndpoint = pendingPushUnsubscribeEndpoint();
    if (normalizedEndpoint && pendingEndpoint !== normalizedEndpoint) return;
    globalThis.localStorage?.removeItem(PENDING_PUSH_UNSUBSCRIBE_KEY);
  } catch {
    // Storage can be unavailable in private browsing modes.
  }
}

async function unsubscribeBrowserSubscription(subscription) {
  try {
    return (await subscription.unsubscribe()) === true;
  } catch (error) {
    console.warn('Failed to remove browser push subscription:', error);
    return false;
  }
}

// Retry a failed browser unsubscribe without retaining an authenticated token
// after logout. Once the browser subscription is removed, the push provider
// rejects the stale server record and ordinary delivery cleanup removes it.
export async function retryPendingPushUnsubscribe({ coordinator, isLoggedOut }) {
  const endpoint = pendingPushUnsubscribeEndpoint();
  if (!endpoint) return true;
  if (typeof isLoggedOut !== 'function' || !isLoggedOut()) return false;

  let subscription;
  try {
    subscription = await getPushSubscription();
  } catch (error) {
    console.warn('Failed to inspect pending browser push cleanup:', error);
    return false;
  }
  if (!subscription || subscription.endpoint !== endpoint) {
    clearPendingPushUnsubscribe(endpoint);
    return true;
  }

  const removed = await coordinator?.runWhenNoOtherActiveTabs?.(async () => {
    if (!isLoggedOut()) return false;
    const unsubscribed = await unsubscribeBrowserSubscription(subscription);
    if (unsubscribed) clearPendingPushUnsubscribe(endpoint);
    return unsubscribed;
  });
  return Boolean(removed);
}

// The browser subscription is shared by every tab in a profile. Registration
// IDs give server records narrower ownership: a stale session may remove its
// own record while a newer, differently registered session stays active.
export async function cleanupPushForSession({
  coordinator,
  registrationID,
  registrationIDs = [registrationID],
  getCurrentToken,
  sessionRevision,
  getCurrentSessionRevision,
  unsubscribeOnServer,
}) {
  coordinator.setActive(false, registrationID);
  const cleanupRegistrationIDs = [...new Set(registrationIDs.filter(Boolean))];
  const currentRegistrationIDs = cleanupRegistrationIDs.filter((id) => id === registrationID);
  const legacyRegistrationIDs = cleanupRegistrationIDs.filter((id) => id !== registrationID);
  let reconciliationRequested = false;
  const requestReconciliation = () => {
    if (reconciliationRequested) return;
    reconciliationRequested = true;
    coordinator.requestReconcile?.();
  };
  const canRemoveBrowserSubscription = () => (
    !getCurrentToken() || (
      Number.isInteger(sessionRevision)
      && getCurrentSessionRevision?.() === sessionRevision
    )
  );
  const removeFromServer = async (endpoint, registrationIDsToRemove) => {
    if (!unsubscribeOnServer || registrationIDsToRemove.length === 0) return true;
    const results = await Promise.all(registrationIDsToRemove.map(async (id) => {
      try {
        await unsubscribeOnServer(endpoint, id);
        return true;
      } catch (error) {
        console.warn('Failed to remove push subscription from server:', error);
        return false;
      }
    }));
    return results.every(Boolean);
  };

  let subscription;
  try {
    subscription = await getPushSubscription();
  } catch (error) {
    console.warn('Failed to inspect browser push subscription during cleanup:', error);
    return false;
  }
  if (!subscription) {
    clearPendingPushUnsubscribe();
    return false;
  }

  if (currentRegistrationIDs.length > 0 && unsubscribeOnServer) {
    const removedCurrentRecord = await coordinator.runWhenRegistrationInactive?.(
      registrationID,
      () => removeFromServer(subscription.endpoint, currentRegistrationIDs),
    );
    if (!removedCurrentRecord) requestReconciliation();
  }

  const needsGlobalCleanup = legacyRegistrationIDs.length > 0 || canRemoveBrowserSubscription();
  if (needsGlobalCleanup) {
    const cleanedSharedSubscription = await coordinator.runWhenNoOtherActiveTabs?.(async () => {
      const removedLegacyRecords = await removeFromServer(subscription.endpoint, legacyRegistrationIDs);
      if (!canRemoveBrowserSubscription()) return removedLegacyRecords;
      const removedBrowserSubscription = await unsubscribeBrowserSubscription(subscription);
      if (removedBrowserSubscription) clearPendingPushUnsubscribe(subscription.endpoint);
      else rememberPendingPushUnsubscribe(subscription.endpoint);
      return removedLegacyRecords && removedBrowserSubscription;
    });
    if (!cleanedSharedSubscription) requestReconciliation();
  }

  return true;
}
