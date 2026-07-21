import { describe, expect, it } from 'vitest';
import { mergeInviteMemberCandidates } from './invite-member-candidates';

describe('mergeInviteMemberCandidates', () => {
  it('uses the Agent roster as the authority and keeps legacy bot markers as a fallback', () => {
    const result = mergeInviteMemberCandidates(
      [
        { id: 8, display_name: 'Alice' },
        { id: 42, display_name: 'Unmarked Agent Friend' },
        { id: 44, display_name: 'Legacy Bot', bot: true },
      ],
      [
        { uid: 42, display_name: 'Owned Agent', relation: 'owner', is_bot: true },
        { id: 43, uid: 43, display_name: 'Agent Outside Friends', relation: 'owner', is_bot: true },
      ],
    );

    expect(result.friends.map((member) => member.id)).toEqual([8]);
    expect(result.agents.map((member) => member.id)).toEqual([42, 43, 44]);
    expect(result.agents[0]).toMatchObject({
      id: 42,
      display_name: 'Owned Agent',
      relation: 'owner',
      isAgent: true,
    });
  });

  it('deduplicates Agent aliases and accepts every explicit legacy marker', () => {
    const result = mergeInviteMemberCandidates(
      [
        { id: 51, display_name: 'is_bot', is_bot: true },
        { id: 52, display_name: 'account_type', account_type: 'bot' },
        { id: 53, display_name: 'accountType', accountType: 'bot' },
      ],
      [
        { id: 51, uid: 51, display_name: 'Roster Agent' },
        { id: 51, uid: 51, display_name: 'Latest Roster Agent' },
      ],
    );

    expect(result.friends).toEqual([]);
    expect(result.agents.map((member) => member.id)).toEqual([51, 52, 53]);
    expect(result.agents[0].display_name).toBe('Latest Roster Agent');
  });
});
