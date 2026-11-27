import { describe, expect, it } from 'vitest';
import { getReplyExample } from './persona-reply-example';

describe('reply examples', () => {
  const answer = (value, parts = []) => getReplyExample(value ? { value } : null, parts, 'dinner');

  it('distinguishes all four priorities on the same question', () => {
    const values = ['先说结论', '尽量简短', '给出判断依据', '给出下一步'];
    const examples = values.map(value => answer(value));
    expect(new Set(examples).size).toBe(values.length);
    expect(examples[1].length).toBeLessThan(examples[0].length);
    expect(answer(null, ['信息不足时：先问我'])).toBe(answer(null));
  });

  it('keeps the chosen priority when applying supported tone and length choices', () => {
    const mode = '先说结论';
    expect(answer(mode, ['回复篇幅：详细展开']).length).toBeGreaterThan(answer(mode).length);
    expect(answer(mode, ['回复语气：正式专业', '\n', '回复篇幅：适中'])).not.toBe(answer(mode));
    expect(answer(null, ['回复模式：用问题引导我'])).toMatch(/？$/);
    expect(answer('尽量简短', ['回复篇幅：详细展开'])).toBe(answer('尽量简短'));
    expect(answer(null, ['人物：普通角色', '回复语气（自定义）：我自己的写法'])).toBe(answer(null));
  });

  it('asks about a missing meeting time or provides a draft with a placeholder', () => {
    expect(getReplyExample(null, ['信息不足时：先问我'], 'meeting')).toBe('改到几点了？');
    expect(getReplyExample(null, ['信息不足时：先给一版，标出待定内容'], 'meeting')).toContain('【新时间】');
  });

  it('writes to the client in the intended register regardless of the chat nickname', () => {
    expect(getReplyExample(null, ['怎么称呼我：同学', '回复语气：轻松俏皮'], 'client'))
      .toBe(getReplyExample(null, [], 'client'));
  });
});
