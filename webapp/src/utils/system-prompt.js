export const MAX_SYSTEM_PROMPT_BYTES = 1024 * 1024;

export function promptByteLength(value) {
  const text = String(value || '');
  if (typeof TextEncoder === 'function') return new TextEncoder().encode(text).length;
  return unescape(encodeURIComponent(text)).length;
}

export function normalizePromptDefinition(response) {
  const prompt = response?.definition?.prompt || {};
  const selected = prompt.selected === 'custom' ? 'custom' : 'default';
  return {
    selected,
    customSystemPrompt: String(prompt.customSystemPrompt || ''),
  };
}

function numberOrZero(value) {
  const number = Number(value);
  return Number.isFinite(number) && number >= 0 ? number : 0;
}

function firstValue(...values) {
  return values.find((value) => value !== undefined && value !== null && value !== '') ?? '';
}

/**
 * Normalize the current application projection while retaining compatibility
 * with older BotDefinition responses that only expose runtime acknowledgements.
 */
export function normalizePromptApplication(response) {
  const application = response?.application
    || response?.applyStatus
    || response?.apply_status
    || {};
  const runtime = response?.runtime || {};
  const desiredRevision = numberOrZero(firstValue(
    application.desired_revision,
    application.desiredRevision,
    response?.desired_revision,
    response?.desiredRevision,
    runtime.desiredRevision,
    response?.revision,
  ));
  const appliedRevision = numberOrZero(firstValue(
    application.applied_revision,
    application.appliedRevision,
    runtime.appliedRevision,
  ));
  const lastAttemptRevision = numberOrZero(firstValue(
    application.last_attempt_revision,
    application.lastAttemptRevision,
    runtime.lastAttemptRevision,
  ));
  const appliedAt = String(firstValue(
    application.applied_at,
    application.appliedAt,
    runtime.appliedAt,
  ));
  const lastAttemptAt = String(firstValue(
    application.last_attempt_at,
    application.lastAttemptAt,
    runtime.lastAttemptAt,
  ));
  const lastError = String(firstValue(
    application.last_error,
    application.lastError,
    runtime.lastError,
  ));
  const onlineValue = firstValue(
    application.is_online,
    application.isOnline,
    response?.is_online,
    response?.isOnline,
  );
  const isOnline = onlineValue === true || onlineValue === false ? onlineValue : null;
  const explicitStatus = String(firstValue(
    application.status,
    application.state,
    response?.application_status,
    response?.applicationStatus,
  )).trim().toLowerCase();

  let status = ['saved', 'pending', 'applied', 'failed'].includes(explicitStatus)
    ? explicitStatus
    : '';
  if (!status) {
    if (desiredRevision > 0 && appliedRevision === desiredRevision
      && (appliedAt || appliedRevision > 0)) {
      status = 'applied';
    } else if (desiredRevision > 0 && lastError && lastAttemptRevision === desiredRevision) {
      status = 'failed';
    } else if (desiredRevision > 0 && (
      isOnline === true
      || lastAttemptRevision === desiredRevision
      || Boolean(lastAttemptAt)
    )) {
      status = 'pending';
    } else {
      status = 'saved';
    }
  }

  return {
    status,
    desiredRevision,
    appliedRevision,
    lastAttemptRevision,
    appliedAt,
    lastAttemptAt,
    isOnline,
    lastError,
  };
}

export function resolvePromptApplicationState(response) {
  const application = normalizePromptApplication(response);
  switch (application.status || 'saved') {
    case 'applied': {
      const revision = application.appliedRevision || application.desiredRevision;
      return {
        ...application,
        kind: 'applied',
        label: revision > 0 ? `Agent 已应用 revision ${revision}` : 'Agent 已应用',
      };
    }
    case 'pending':
      return { ...application, kind: 'pending', label: '等待 Agent 应用' };
    case 'failed':
      return {
        ...application,
        kind: 'failed',
        label: '应用失败，请重启或检查 Agent',
      };
    default:
      return { ...application, kind: 'saved', label: '已保存到云端' };
  }
}

// Kept for callers introduced before the application projection was added.
export function resolvePromptApplyState(response) {
  return resolvePromptApplicationState(response);
}
