import { describe, expect, it } from 'vitest';
import { formatGroupMentions, groupBotMembers, groupMemberName } from './group-delivery';

const members = [
  { user_id: 7, display_name: 'Bruce', is_bot: false },
  { user_id: 42, display_name: 'Saturday', username: 'bot-saturday', is_bot: true },
  { user_id: 43, display_name: 'Wanyu', username: 'bot-wanyu', account_type: 'bot' },
];

describe('group delivery identity helpers', () => {
  it('keeps only Agent members as activation candidates', () => {
    expect(groupBotMembers(members).map((member) => member.user_id)).toEqual([42, 43]);
  });

  it('formats canonical wire mentions with readable Agent names', () => {
    expect(formatGroupMentions('@usr42 已收到，@usr43 继续', members)).toBe('@Saturday 已收到，@Wanyu 继续');
  });

  it('disambiguates duplicate Agent names', () => {
    const duplicates = [
      { user_id: 42, display_name: 'Saturday', username: 'bot-a', is_bot: true },
      { user_id: 43, display_name: 'Saturday', username: 'bot-b', is_bot: true },
    ];
    expect(groupMemberName(duplicates[0], duplicates)).toBe('Saturday · bot-a');
    expect(groupMemberName(duplicates[1], duplicates)).toBe('Saturday · bot-b');
  });
});
