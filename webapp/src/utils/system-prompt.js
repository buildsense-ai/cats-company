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

export function resolvePromptApplyState(response) {
  if (!response?.configured) return { kind: 'unconfigured', label: '等待初始化' };
  const revision = Number(response?.revision || 0);
  const runtime = response?.runtime || {};
  const attemptedRevision = Number(runtime.lastAttemptRevision || 0);
  const appliedRevision = Number(runtime.appliedRevision || 0);
  const hasApplicationEvidence = Boolean(
    runtime.appliedAt || runtime.appliedKind || runtime.appliedModelId,
  );
  if (attemptedRevision === revision && runtime.lastError) {
    return { kind: 'error', label: '应用失败', detail: String(runtime.lastError) };
  }
  if (appliedRevision === revision && (revision > 0 || hasApplicationEvidence)) {
    return { kind: 'applied', label: '已生效', detail: runtime.appliedAt || '' };
  }
  return { kind: 'pending', label: '待应用' };
}
