import { collectSkillHubUpdateSummary } from './skillhub-update-summary';

describe('collectSkillHubUpdateSummary', () => {
  it('counts only owner Bots whose public SkillHub references were checked', async () => {
    const getDefinition = vi.fn(async (uid) => {
      if (String(uid) === '3') throw new Error('offline Bot');
      return {
        skills: String(uid) === '1'
          ? [{ skillId: 'arrowhaken/image-generation', version: '1.0.5', contentHash: 'a'.repeat(64) }]
          : [{ skillId: 'arrowhaken/image-generation', version: '1.0.6', contentHash: 'b'.repeat(64) }],
      };
    });
    const getSkill = vi.fn(async () => ({
      skill: { id: 'arrowhaken/image-generation', latestVersion: '1.0.6', contentHash: 'b'.repeat(64) },
    }));

    await expect(collectSkillHubUpdateSummary({
      bots: [
        { uid: 1, relation: 'owner' },
        { uid: 2, relation: 'owner' },
        { uid: 3, relation: 'owner' },
        { uid: 4, relation: 'friend', owner_id: 99 },
      ],
      userUid: 7,
      getDefinition,
      getSkill,
    })).resolves.toMatchObject({
      total: 1,
      byBot: {
        1: { count: 1, status: 'ready' },
        2: { count: 0, status: 'ready' },
        3: { count: null, status: 'unavailable' },
      },
      detailsBySkillID: {
        'arrowhaken/image-generation': {
          skillId: 'arrowhaken/image-generation',
          latestVersion: '1.0.6',
        },
      },
    });
    expect(getDefinition).toHaveBeenCalledTimes(3);
    expect(getSkill).toHaveBeenCalledTimes(1);
  });

  it('does not advertise an update for an incomplete historical reference', async () => {
    const getDefinition = vi.fn(async () => ({
      skills: [{ skillId: 'arrowhaken/image-generation', version: '1.0.5' }],
    }));
    const getSkill = vi.fn(async () => ({
      skill: { skillId: 'arrowhaken/image-generation', latestVersion: '1.0.6' },
    }));

    await expect(collectSkillHubUpdateSummary({
      bots: [{ uid: 1, relation: 'owner' }],
      userUid: 7,
      getDefinition,
      getSkill,
    })).resolves.toMatchObject({
      total: 0,
      byBot: { 1: { count: 0, status: 'ready' } },
    });
  });

  it('does not trust a local workspace entry as catalogue metadata', async () => {
    const getDefinition = vi.fn(async () => ({
      skills: [{
        skillId: 'arrowhaken/image-generation',
        version: '1.0.5',
        contentHash: 'a'.repeat(64),
      }],
    }));
    const getSkill = vi.fn(async () => ({
      skill: {
        skillId: 'arrowhaken/image-generation',
        latestVersion: '1.0.6',
        contentHash: 'b'.repeat(64),
      },
    }));

    await expect(collectSkillHubUpdateSummary({
      bots: [{ uid: 1, relation: 'owner' }],
      userUid: 7,
      getDefinition,
      getSkill,
      catalogueByID: new Map([['arrowhaken/image-generation', {
        skillId: 'arrowhaken/image-generation',
        latestVersion: '1.0.5',
        contentHash: 'a'.repeat(64),
        source: 'local',
        isLocalSkill: true,
      }]]),
    })).resolves.toMatchObject({
      total: 1,
      byBot: { 1: { count: 1, status: 'ready' } },
    });
    expect(getSkill).toHaveBeenCalledTimes(1);
  });

  it('does not count private references or friend Bots', async () => {
    const getDefinition = vi.fn(async () => ({
      skills: [
        { skillId: 'private/review', version: '1.0.0' },
        { skillId: 'arrowhaken/search', version: '1.0.0' },
      ],
    }));
    const getSkill = vi.fn(async () => ({
      skill: { id: 'arrowhaken/search', latestVersion: '1.0.0', contentHash: 'c'.repeat(64) },
    }));

    await expect(collectSkillHubUpdateSummary({
      bots: [{ uid: 1, relation: 'owner' }, { uid: 2, relation: 'friend', owner_id: 9 }],
      userUid: 7,
      getDefinition,
      getSkill,
    })).resolves.toMatchObject({ total: 0, byBot: { 1: { count: 0, status: 'ready' } } });
    expect(getDefinition).toHaveBeenCalledTimes(1);
    expect(getSkill).toHaveBeenCalledTimes(1);
  });
});
