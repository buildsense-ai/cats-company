import {
  formatRelayUsagePill,
  resolveConversationModelDisplay,
  resolveCurrentModelName,
  shortCustomModelName,
} from './relay-usage';

describe('relay usage labels', () => {
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
});
