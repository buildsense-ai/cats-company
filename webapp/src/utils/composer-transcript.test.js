import { insertTranscriptAtSelection } from './composer-transcript';

describe('insertTranscriptAtSelection', () => {
  it('inserts normalized speech at the captured selection', () => {
    expect(insertTranscriptAtSelection(' 语音文字 ', {
      baseValue: '前后',
      start: 1,
      end: 1,
    })).toEqual({
      baseValue: '前后',
      value: '前语音文字后',
      caret: 5,
    });
  });

  it('falls back to the current textarea selection', () => {
    const textarea = { value: '替换这里', selectionStart: 2, selectionEnd: 4 };
    expect(insertTranscriptAtSelection('内容', null, textarea, '旧值')).toEqual({
      baseValue: '替换这里',
      value: '替换内容',
      caret: 4,
    });
  });

  it('ignores an empty final transcript', () => {
    expect(insertTranscriptAtSelection('   ', null, null, '草稿')).toBeNull();
  });
});
