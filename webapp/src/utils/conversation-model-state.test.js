import {
  applyScopedModelUpdate,
  resolveScopedModelState,
} from './conversation-model-state';

const ready = (model) => ({
  isBot: true,
  state: 'ready',
  summary: { source: 'relay', model, remaining_percent: 80 },
});

describe('conversation model state isolation', () => {
  test('hides a group immediately while its members are loading', () => {
    expect(resolveScopedModelState({ topicId: 'grp_1', isGroup: true }, null)).toEqual({
      isBot: false,
      state: 'hidden',
      summary: null,
    });
  });

  test('does not carry a single-Agent model into another group', () => {
    const first = { topicId: 'grp_1', modelState: ready('gpt-5.6-terra') };
    expect(resolveScopedModelState({ topicId: 'grp_2', isGroup: true }, first)?.state).toBe('hidden');
  });

  test('hides a direct conversation until its participant type is resolved', () => {
    const first = { topicId: 'grp_1', modelState: ready('gpt-5.6-terra') };
    expect(resolveScopedModelState({ topicId: 'p2p_1_2', isGroup: false }, first)?.state).toBe('hidden');
  });

  test('accepts only updates for the currently active topic', () => {
    const current = { topicId: 'grp_2', modelState: ready('MiniMax-M3') };
    expect(applyScopedModelUpdate(current, {
      activeTopicId: 'grp_2',
      topicId: 'grp_1',
      modelState: ready('gpt-5.6-terra'),
    })).toBe(current);
    expect(applyScopedModelUpdate(current, {
      activeTopicId: 'grp_2',
      topicId: 'grp_2',
      modelState: { isBot: false, state: 'hidden', summary: null },
    })).toEqual({
      topicId: 'grp_2',
      modelState: { isBot: false, state: 'hidden', summary: null },
    });
  });

  test('keeps a rapid single-multi-single sequence scoped to the last task', () => {
    let scoped = applyScopedModelUpdate(null, {
      activeTopicId: 'grp_1',
      topicId: 'grp_1',
      modelState: ready('gpt-5.6-terra'),
    });
    expect(resolveScopedModelState({ topicId: 'grp_1', isGroup: true }, scoped).summary.model)
      .toBe('gpt-5.6-terra');

    expect(resolveScopedModelState({ topicId: 'grp_2', isGroup: true }, scoped).state).toBe('hidden');
    scoped = applyScopedModelUpdate(scoped, {
      activeTopicId: 'grp_2',
      topicId: 'grp_1',
      modelState: ready('stale-model'),
    });
    expect(resolveScopedModelState({ topicId: 'grp_2', isGroup: true }, scoped).state).toBe('hidden');

    scoped = applyScopedModelUpdate(scoped, {
      activeTopicId: 'grp_3',
      topicId: 'grp_3',
      modelState: ready('MiniMax-M3'),
    });
    expect(resolveScopedModelState({ topicId: 'grp_3', isGroup: true }, scoped).summary.model)
      .toBe('MiniMax-M3');
  });
});
