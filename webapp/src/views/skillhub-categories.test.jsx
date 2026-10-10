import { inferSkillCategory, localizedSkillText, skillUploadedTimestamp } from './skillhub-categories';
import { skillSourceQuery } from './skillhub-presentation';

describe('Skill catalogue presentation', () => {
  it('maps a reviewed Chinese title to the searchable source name', () => {
    expect(skillSourceQuery('图像生成')).toBe('image-generation');
    expect(skillSourceQuery('审稿')).toBe('');
    expect(skillSourceQuery('unknown')).toBe('unknown');
  });
  it.each([
    [{ displayName: 'image-generation' }, 'design'],
    [{ displayName: 'humanizer' }, 'writing'],
    [{ displayName: '会议纪要助手' }, 'office'],
    [{ displayName: '课程教案' }, 'education'],
    [{ skillId: 'design-team/stock-analysis' }, 'finance'],
    [{ displayName: 'Excel 清洗' }, 'data'],
    [{ displayName: 'unknown', description: '通过 GitHub CLI 管理仓库' }, 'development'],
    [{ displayName: 'unknown', tags: ['marketing'] }, 'business'],
    [{ displayName: 'unknown', primaryCategory: 'media' }, 'design'],
    [{ displayName: 'image-generation', primaryCategory: 'data' }, 'data'],
    [{ displayName: 'unknown', primaryCategory: 'invalid' }, 'other'],
    [{ skillId: 'yii/image-generation', latestVersion: '1.0.2', categories: ['legal'] }, 'design'],
  ])('classifies %j as %s', (skill, expected) => {
    expect(inferSkillCategory(skill)).toBe(expected);
  });

  it('sorts by uploaded time with aliases and leaves missing dates last', () => {
    const entries = [
      { skillId: 'missing' },
      { skillId: 'old', published_at: '2026-09-01' },
      { skillId: 'new', uploaded_at: '2026-10-01', publishedAt: '2026-08-01' },
      { skillId: 'invalid', uploadedAt: 'invalid' },
    ];
    expect(entries.sort((a, b) => skillUploadedTimestamp(b) - skillUploadedTimestamp(a)).map(s => s.skillId))
      .toEqual(['new', 'old', 'missing', 'invalid']);
  });

  it('prefers published Chinese text and keeps the original identity intact', () => {
    const skill = { skillId: 'tools/code', displayName: 'Code', description: 'Original', translations: { 'zh-CN': { name: '代码助手', description: '开发与调试' } } };
    expect(localizedSkillText(skill, 'source').name).toBe('Code');
    expect(localizedSkillText(skill, 'zh-CN')).toMatchObject({ name: '代码助手', description: '开发与调试', translated: true });
    expect(skill.displayName).toBe('Code');
  });

  it('keeps reviewed translations available after a catalogue version bump', () => {
    const skill = { skillId: 'yii/image-generation', latestVersion: '1.0.2', displayName: 'image-generation', description: 'Original' };
    expect(localizedSkillText(skill, 'zh-CN').name).toBe('图像生成与编辑');
    expect(localizedSkillText({ ...skill, latestVersion: '2.0.0' }, 'zh-CN'))
      .toMatchObject({ name: '图像生成与编辑', translated: true });
  });
});
