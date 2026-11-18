import {
  formatRelayUsagePill,
  formatModelDisplayName,
  isCreditBilledSummary,
  relayUsageTone,
  resolveConversationModelDisplay,
  resolveCurrentModelName,
  shortCustomModelName,
} from './relay-usage';

describe('relay usage labels', () => {
  test('uses DeepSeek brand casing only for display and preserves the reported model ID', () => {
    const summary = { source: 'relay', model: 'deepseek-flash', remaining_percent: 100 };
    const display = resolveConversationModelDisplay('', { isBot: true, state: 'ready', summary });
    expect(display.model).toBe('DeepSeek-flash');
    expect(display.title).toContain('DeepSeek-flash');
    expect(summary.model).toBe('deepseek-flash');
    expect(resolveCurrentModelName(summary)).toBe('deepseek-flash');
    expect(resolveConversationModelDisplay('deepseek-flash', { isBot: false }).model).toBe('DeepSeek-flash');
    expect(formatModelDisplayName('DeepSeek Flash')).toBe('DeepSeek Flash');
    expect(formatModelDisplayName('deepseek-v4-flash')).toBe('DeepSeek-v4-flash');
    expect(formatModelDisplayName('vendor/deepseek-custom')).toBe('vendor/deepseek-custom');
  });

  test('identifies lifetime trial separately from the recurring shared pool', () => {
    const result = resolveConversationModelDisplay('gpt-5.6-terra', {
      isBot: true, summary: { source: 'relay', model: 'gpt-5.6-terra', remaining_percent: 0, reset_duration: 'never' },
    });
    expect(result.title).toContain('一次性新人试用额度，用完不恢复');
    expect(result.title).not.toContain('共享额度');
  });
  test('shows the reported custom model name', () => {
    expect(formatRelayUsagePill(
      { source: 'custom', status: 'custom', model: 'gpt-5.6-terra' },
      { customLabel: '自备模型' },
    )).toBe('gpt-5.6-terra · 自备');
  });

  test('falls back when an older client reports no custom model name', () => {
    expect(formatRelayUsagePill(
      { source: 'custom', status: 'custom', model: '自定义模型' },
      { customLabel: '自备模型' },
    )).toBe('自备模型');
  });

  test('can omit a model name already displayed by the model selector', () => {
    expect(formatRelayUsagePill(
      { source: 'custom', status: 'custom', model: 'gpt-5.6-sol' },
      { customLabel: '自备模型', showModel: false },
    )).toBe('自备模型');
    expect(formatRelayUsagePill(
      { status: 'ok', model: 'MiniMax-M2.7', remaining_percent: 64 },
      { showModel: false },
    )).toBe('剩余 64%');
  });

  test('bounds unusually long custom model names', () => {
    expect(shortCustomModelName('vendor-model-name-that-is-unusually-long'))
      .toBe('vendor-model-name-that-i...');
  });

  test('resolves the exact current relay and custom model names', () => {
    expect(resolveCurrentModelName(
      { source: 'relay', model: 'MiniMax-M3' },
      'MiniMax-M2.7',
    )).toBe('MiniMax-M3');
    expect(resolveCurrentModelName(
      { source: 'custom', status: 'custom', model: 'gpt-5.6-terra' },
      'MiniMax-M2.7',
    )).toBe('gpt-5.6-terra');
  });

  test('falls back to configured or generic model labels', () => {
    expect(resolveCurrentModelName(null, 'MiniMax-M3')).toBe('MiniMax-M3');
    expect(resolveCurrentModelName(
      { source: 'custom', status: 'custom', model: 'custom' },
      'MiniMax-M3',
    )).toBe('自定义模型');
  });

  test('combines a custom Agent model and source in one header display', () => {
    expect(resolveConversationModelDisplay('MiniMax-M2.7', {
      isBot: true,
      state: 'ready',
      summary: { source: 'custom', status: 'custom', model: 'gpt-5.6-terra' },
    })).toEqual({
      model: 'gpt-5.6-terra',
      meta: '自备模型',
      title: 'gpt-5.6-terra；该虚拟员工使用自备模型，不消耗 CatsCo 共享额度',
    });
  });

  test('combines a relay Agent model and remaining quota in one header display', () => {
    expect(resolveConversationModelDisplay('MiniMax-M2.7', {
      isBot: true,
      state: 'ready',
      summary: { source: 'relay', status: 'normal', model: 'MiniMax-M3', remaining_percent: 72 },
    })).toEqual({
      model: 'MiniMax-M3',
      meta: '剩余 72%',
      title: 'MiniMax-M3；使用该虚拟员工所属账号的共享额度，剩余 72%',
    });
  });

  test('keeps the applied reasoning effort with a friend-visible Agent model', () => {
    expect(resolveConversationModelDisplay('MiniMax-M2.7', {
      isBot: true,
      state: 'ready',
      summary: {
        source: 'relay',
        status: 'normal',
        model: 'gpt-5.6-sol',
        reasoning_effort: 'xhigh',
        remaining_percent: 68,
      },
    })).toEqual({
      model: 'gpt-5.6-sol',
      meta: '极高 · 剩余 68%',
      title: 'gpt-5.6-sol；推理强度 极高；使用该虚拟员工所属账号的共享额度，剩余 68%',
    });
  });

  test('makes a missing Agent status visible instead of leaving a blank quota area', () => {
    expect(resolveConversationModelDisplay('MiniMax-M2.7', {
      isBot: true,
      state: 'unavailable',
      summary: null,
    })).toEqual({
      model: '模型未同步',
      meta: '额度未同步',
      title: '当前虚拟员工尚未上报可用的模型与额度状态',
    });
  });

  test('hides the model for conversations without a single responsible Agent', () => {
    expect(resolveConversationModelDisplay('MiniMax-M2.7', {
      isBot: false,
      state: 'hidden',
      summary: null,
    })).toBeNull();
  });

  test('labels a credit-billed model as account credits instead of a legacy percentage', () => {
    const summary = {
      source: 'credits',
      status: 'credits',
      billing_mode: 'credits',
      model: 'deepseek-flash',
    };
    expect(isCreditBilledSummary(summary)).toBe(true);
    expect(formatRelayUsagePill(summary, { showModel: false })).toBe('使用账户点数');
    expect(formatRelayUsagePill(summary)).toBe('DS 使用账户点数');
    // 零余额/缺省百分比不能被当成「剩余 0%」或超额提示。
    expect(formatRelayUsagePill({ ...summary, remaining_percent: 0 })).toBe('DS 使用账户点数');
    expect(formatRelayUsagePill({ ...summary, status: 'over_limit', remaining_percent: 0 })).toBe('DS 使用账户点数');
    expect(relayUsageTone(summary)).toBe('muted');
    expect(relayUsageTone({ ...summary, remaining_percent: 0 })).not.toBe('warning');
  });

  test('keeps the legacy percentage for a model without a credit billing mode', () => {
    const summary = { source: 'relay', status: 'normal', model: 'MiniMax-M3', remaining_percent: 72 };
    expect(isCreditBilledSummary(summary)).toBe(false);
    expect(formatRelayUsagePill(summary)).toBe('M3 剩余 72%');
    expect(relayUsageTone(summary)).toBe('');
    // 自备模型分支不变，不会被 credit 文案覆盖。
    expect(formatRelayUsagePill(
      { source: 'custom', status: 'custom', model: 'gpt-5.6-terra', billing_mode: 'credits' },
    )).toBe('gpt-5.6-terra · 自备');
  });

  test('reports account credits for a credit-billed Agent model without claiming shared quota', () => {
    expect(resolveConversationModelDisplay('MiniMax-M2.7', {
      isBot: true,
      state: 'ready',
      summary: {
        source: 'credits',
        status: 'credits',
        billing_mode: 'credits',
        model: 'deepseek-flash',
        reasoning_effort: 'high',
      },
    })).toEqual({
      model: 'DeepSeek-flash',
      meta: '高 · 使用账户点数',
      title: 'DeepSeek-flash；推理强度 高；该虚拟员工使用所属账号的点数计费，不占用旧额度',
    });
  });
});
