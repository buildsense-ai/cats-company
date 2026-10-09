import { readSkillAdditionTimes, recordSkillAdditions } from './skillhub-addition-order';

describe('Skill addition times', () => {
  beforeEach(() => {
    localStorage.clear();
    vi.useFakeTimers();
    vi.setSystemTime(new Date('2026-10-09T01:00:00Z'));
  });
  afterEach(() => vi.useRealTimers());

  it('records successful additions per user and Agent without inventing legacy dates', () => {
    recordSkillAdditions('7', '42', [{ skillId: 'legacy' }], [{ skillId: 'legacy' }, { skillId: 'new' }]);
    expect(readSkillAdditionTimes('7', '42')).toEqual({ new: '2026-10-09T01:00:00.000Z' });
    expect(readSkillAdditionTimes('8', '42')).toEqual({});
    expect(readSkillAdditionTimes('7', '43')).toEqual({});
  });

  it('keeps the addition date on updates and resets it after removal and readdition', () => {
    recordSkillAdditions('7', '42', [], [{ skillId: 'a', version: '1' }]);
    vi.setSystemTime(new Date('2026-10-09T02:00:00Z'));
    recordSkillAdditions('7', '42', [{ skillId: 'a', version: '1' }], [{ skillId: 'a', version: '2' }]);
    expect(readSkillAdditionTimes('7', '42').a).toBe('2026-10-09T01:00:00.000Z');
    recordSkillAdditions('7', '42', [{ skillId: 'a' }], []);
    expect(readSkillAdditionTimes('7', '42')).toEqual({});
    recordSkillAdditions('7', '42', [], [{ skillId: 'a' }]);
    expect(readSkillAdditionTimes('7', '42').a).toBe('2026-10-09T02:00:00.000Z');
  });

  it('ignores damaged browser storage', () => {
    localStorage.setItem('catsco.skillhub.added-at.7.42', '{broken');
    expect(readSkillAdditionTimes('7', '42')).toEqual({});
    localStorage.setItem('catsco.skillhub.added-at.7.42', JSON.stringify({ invalid: 'no date', a: '2026-10-01' }));
    expect(readSkillAdditionTimes('7', '42')).toEqual({ a: '2026-10-01' });
  });
});
