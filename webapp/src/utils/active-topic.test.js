import { normalizeActiveTopic } from './active-topic';

describe('active topic normalization', () => {
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
});
