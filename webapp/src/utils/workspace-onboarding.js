export const WORKSPACE_ONBOARDING_DISMISSED_VALUE = 'dismissed';

export function workspaceOnboardingStorageKey(userId) {
  const normalizedUserId = String(userId || '').trim();
  return normalizedUserId ? `cc_workspace_onboarding_v1:${normalizedUserId}` : '';
}
