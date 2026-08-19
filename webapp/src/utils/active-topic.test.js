import {
  lastTopicStorageKey,
  normalizeActiveTopic,
  readStoredTopic,
  shouldForgetStoredTopic,
  writeStoredTopic,
} from './active-topic';

describe('active topic normalization', () => {
  beforeEach(() => {
    localStorage.clear();
  });

  test.each([
    ['grp_855', { topicId: 'grp_855', name: '', isGroup: true, groupId: 855 }],
    ['p2p_38_405', { topicId: 'p2p_38_405', name: '', isGroup: false, groupId: undefined }],
  ])('normalizes a stored topic string %s', (input, expected) => {
    expect(normalizeActiveTopic(input)).toEqual(expected);
  });

  test('repairs older group objects that omitted group metadata', () => {
    expect(normalizeActiveTopic({ topicId: 'grp_901', name: '旧任务' })).toEqual({
      topicId: 'grp_901',
      name: '旧任务',
      isGroup: true,
      groupId: 901,
      avatar_url: '',
      friendId: undefined,
    });
  });

  test('preserves explicit group and direct-chat metadata', () => {
    expect(normalizeActiveTopic({
      topicId: 'grp_11',
      name: '任务',
      isGroup: true,
      groupId: '42',
      avatar_url: '/avatar.png',
    })).toMatchObject({ isGroup: true, groupId: 42, avatar_url: '/avatar.png' });
    expect(normalizeActiveTopic({
      topicId: 'p2p_1_2',
      name: 'Agent',
      friendId: 2,
    })).toMatchObject({ isGroup: false, groupId: undefined, friendId: 2 });
  });

  test('preserves task classification metadata when it is provided', () => {
    expect(normalizeActiveTopic({
      topicId: 'grp_42',
      name: '协作任务',
      isGroup: true,
      isBot: false,
      hasBot: true,
      isAgentTask: true,
      memberCount: 4,
    })).toMatchObject({
      isBot: false,
      hasBot: true,
      isAgentTask: true,
      memberCount: 4,
    });
  });

  test.each([null, '', '[object Object]', {}, { topicId: '' }])('rejects invalid topic %j', (input) => {
    expect(normalizeActiveTopic(input)).toBeNull();
  });

  test('restores a user-scoped topic before falling back to legacy storage', () => {
    localStorage.setItem('v3_last_topic', JSON.stringify({ topicId: 'grp_1', name: 'legacy' }));
    localStorage.setItem(lastTopicStorageKey(38), JSON.stringify({ topicId: 'grp_891', name: 'current' }));

    expect(readStoredTopic(38)).toMatchObject({ topicId: 'grp_891', name: 'current', groupId: 891 });
  });

  test('writes and clears both scoped and legacy topic storage', () => {
    writeStoredTopic(38, { topicId: 'p2p_38_537', name: 'Agent' });
    expect(readStoredTopic(38)).toMatchObject({ topicId: 'p2p_38_537', name: 'Agent' });

    writeStoredTopic(38, null);
    expect(localStorage.getItem(lastTopicStorageKey(38))).toBeNull();
    expect(localStorage.getItem('v3_last_topic')).toBeNull();
  });

  test('forgets inaccessible topics but preserves them across transient failures', () => {
    expect(shouldForgetStoredTopic({ status: 400 })).toBe(true);
    expect(shouldForgetStoredTopic({ status: 403 })).toBe(true);
    expect(shouldForgetStoredTopic({ status: 404 })).toBe(true);
    expect(shouldForgetStoredTopic({ status: 429 })).toBe(false);
    expect(shouldForgetStoredTopic({ status: 503 })).toBe(false);
    expect(shouldForgetStoredTopic({ code: 'NETWORK_ERROR' })).toBe(false);
  });
});
