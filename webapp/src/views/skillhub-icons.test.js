import { describe, expect, it } from 'vitest';
import { skillIconKey } from './skillhub-icons';

describe('skillIconKey', () => {
  it('keeps known Skills on stable semantic icons regardless of category fallback', () => {
    expect(skillIconKey({ skillId: 'yii/image-generation' }, 'development')).toBe('image');
    expect(skillIconKey({ skillId: 'yii/image-generation' }, 'design')).toBe('image');
    expect(skillIconKey({ skillId: 'jk7534176/meeting-audio-minutes' }, 'office')).toBe('audio');
    expect(skillIconKey({ skillId: 'arrowhaken/blackboard-realtime' }, 'office')).toBe('collaboration');
  });

  it('recognizes browser automation before generic data wording', () => {
    expect(skillIconKey({
      name: 'agent-browser',
      description: 'Browser automation for data extraction with Playwright',
    }, 'data')).toBe('browser');
  });

  it('uses strong capability semantics before the category fallback', () => {
    expect(skillIconKey({ name: 'Meeting recorder', description: 'Audio transcription' }, 'office')).toBe('audio');
    expect(skillIconKey({ name: 'HTML artifact publisher' }, 'development')).toBe('app');
    expect(skillIconKey({ name: 'Generic helper' }, 'education')).toBe('education');
  });
});
