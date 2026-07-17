import { formatRelayUsagePill, resolveCurrentModelName, shortCustomModelName } from './relay-usage';

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
});
