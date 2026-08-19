const HIDDEN_MODEL_STATE = Object.freeze({
  isBot: false,
  state: 'hidden',
  summary: null,
});

export function applyScopedModelUpdate(current, { activeTopicId, topicId, modelState }) {
  if (!topicId || topicId !== activeTopicId) return current;
  return { topicId, modelState };
}

export function resolveScopedModelState(activeTopic, scopedModelState) {
  const topicId = activeTopic?.topicId || '';
  if (!topicId) return null;
  if (scopedModelState?.topicId === topicId) {
    return scopedModelState.modelState;
  }
  return HIDDEN_MODEL_STATE;
}
