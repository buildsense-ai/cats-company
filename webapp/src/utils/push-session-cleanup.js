import { getPushSubscription } from './push-notifications';

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
    if (!unsubscribeOnServer || registrationIDsToRemove.length === 0) return;
    await Promise.all(registrationIDsToRemove.map(async (id) => {
      try {
        await unsubscribeOnServer(endpoint, id);
      } catch (error) {
        console.warn('Failed to remove push subscription from server:', error);
      }
    }));
  };

  const subscription = await getPushSubscription();
  if (!subscription) return false;

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
      await removeFromServer(subscription.endpoint, legacyRegistrationIDs);
      if (!canRemoveBrowserSubscription()) return true;
      return subscription.unsubscribe();
    });
    if (!cleanedSharedSubscription) requestReconciliation();
  }

  return true;
}
