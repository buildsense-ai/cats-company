export function formatRelayUsagePill(summary, { customLabel = '自定义模型', showModel = true } = {}) {
  if (summary?.source === 'custom' || summary?.status === 'custom') {
    if (!showModel) return customLabel;
    const model = shortCustomModelName(summary?.model);
    return model ? `${model} · 自备` : customLabel;
  }
  if (!summary || !summary.model) return '';

  if (summary.status === 'over_limit') {
    return showModel ? `${shortRelayModelName(summary.model)} 已用 100%+` : '已用 100%+';
  }

  const remainingPercent = Number(summary.remaining_percent);
  if (!Number.isFinite(remainingPercent)) return '';

  const clamped = Math.max(0, Math.min(100, remainingPercent));
  const remainingLabel = `剩余 ${Math.round(clamped)}%`;
  return showModel ? `${shortRelayModelName(summary.model)} ${remainingLabel}` : remainingLabel;
}

export function shortCustomModelName(model) {
  const text = String(model || '').trim();
  if (!text || /^custom$/i.test(text) || text === '自定义模型') return '';
  return text.length > 24 ? `${text.slice(0, 24)}...` : text;
}

export function resolveCurrentModelName(summary, defaultModel = 'MiniMax-M2.7') {
  const reportedModel = String(summary?.model || '').trim();
  if (summary?.source === 'custom' || summary?.status === 'custom') {
    return reportedModel && !/^custom$/i.test(reportedModel) ? reportedModel : '自定义模型';
  }
  return reportedModel || String(defaultModel || '').trim() || '模型未知';
}

export function resolveConversationModelDisplay(currentModelName, agentModelState) {
  if (agentModelState?.state === 'hidden') return null;

  const accountModel = String(currentModelName || '').trim() || '模型未知';
  if (!agentModelState?.isBot) {
    return {
      model: accountModel,
      meta: '',
      title: `当前使用的模型：${accountModel}`,
    };
  }

  const summary = agentModelState.summary;
  if (!summary) {
    const loading = agentModelState.state === 'loading';
    return {
      model: loading ? '模型同步中' : '模型未同步',
      meta: loading ? '' : '额度未同步',
      title: loading
        ? '正在读取当前虚拟员工的模型状态'
        : '当前虚拟员工尚未上报可用的模型与额度状态',
    };
  }

  const model = resolveCurrentModelName(summary, '模型未知');
  const custom = summary.source === 'custom' || summary.status === 'custom';
  const reasoningEffort = String(summary.reasoning_effort || '').trim();
  const quota = formatRelayUsagePill(summary, {
    customLabel: '自备模型',
    showModel: false,
  });
  const quotaMeta = quota || (custom ? '自备模型' : '额度未同步');
  const meta = [reasoningEffort, quotaMeta].filter(Boolean).join(' · ');
  const reasoningTitle = reasoningEffort ? `；推理强度 ${reasoningEffort}` : '';
  return {
    model,
    meta,
    title: custom
      ? `${model}${reasoningTitle}；该虚拟员工使用自备模型，不消耗 CatsCo 共享额度`
      : quota
        ? `${model}${reasoningTitle}；使用该虚拟员工所属账号的共享额度，${quota}`
        : `${model}${reasoningTitle}；当前额度暂未同步`,
  };
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
