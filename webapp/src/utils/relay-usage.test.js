import { formatRelayUsagePill, shortCustomModelName } from './relay-usage';

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

  test('bounds unusually long custom model names', () => {
    expect(shortCustomModelName('vendor-model-name-that-is-unusually-long'))
      .toBe('vendor-model-name-that-i...');
  });
});
