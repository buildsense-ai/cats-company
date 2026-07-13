export function formatRelayUsagePill(summary, { customLabel = '自定义模型' } = {}) {
  if (summary?.source === 'custom' || summary?.status === 'custom') {
    return customLabel;
  }
  if (!summary || !summary.model) return '';

  if (summary.status === 'over_limit') {
    return `${shortRelayModelName(summary.model)} 已用 100%+`;
  }

  const explicitRemaining = Number(summary.remaining_percent);
  const limit = Number(summary.limit_cny);
  const usedPercent = Number(summary.percent);
  let remainingPercent;
  if (Number.isFinite(explicitRemaining)) {
    remainingPercent = explicitRemaining;
  } else if (Number.isFinite(limit) && limit > 0 && Number.isFinite(usedPercent)) {
    remainingPercent = 100 - usedPercent;
  } else {
    return '';
  }

  const clamped = Math.max(0, Math.min(100, remainingPercent));
  return `${shortRelayModelName(summary.model)} 剩余 ${Math.round(clamped)}%`;
}

export function relayUsageTone(summary) {
  if (summary?.status === 'over_limit') return 'danger';
  if (summary?.status === 'high' || Number(summary?.remaining_percent) <= 10) return 'warning';
  if (summary?.status === 'custom') return 'muted';
  return '';
}

export function shortRelayModelName(model) {
  const text = String(model || '').trim();
  if (!text) return '模型';
  if (/minimax-m3/i.test(text)) return 'M3';
  if (/minimax-m2\.?7/i.test(text)) return 'M2.7';
  if (/deepseek/i.test(text)) return 'DS';
  if (/glm/i.test(text)) return 'GLM';
  return text.length > 8 ? `${text.slice(0, 8)}...` : text;
}
