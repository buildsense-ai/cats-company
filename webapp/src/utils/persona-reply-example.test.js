import { describe, expect, it } from 'vitest';
import { getReplyExample, replyExampleScenarios } from './persona-reply-example';

describe.each(replyExampleScenarios)('reply example for $id', ({ id }) => {
  const answer = (value, parts = []) => getReplyExample(value ? { value } : null, parts, id);

  it('distinguishes all four priorities on the same question', () => {
    const values = ['先说结论', '尽量简短', '给出判断依据', '有疑问先问我'];
    const examples = values.map(value => answer(value));
    expect(new Set(examples).size).toBe(values.length);
    expect(examples[1].length).toBeLessThan(examples[0].length);
    expect(examples[3]).toMatch(/？$/);
  });

  it('keeps the chosen priority when applying supported tone and length choices', () => {
    const mode = '先说结论';
    expect(answer(mode, ['回复篇幅：详细展开']).length).toBeGreaterThan(answer(mode).length);
    expect(answer(mode, ['回复语气：正式专业', '\n', '回复篇幅：适中'])).not.toBe(answer(mode));
    expect(answer('有疑问先问我', ['回复篇幅：详细展开'])).toMatch(/？$/);
    expect(answer('尽量简短', ['回复篇幅：详细展开'])).toBe(answer('尽量简短'));
    expect(answer(null, ['人物：普通角色', '回复语气（自定义）：我自己的写法'])).toBe(answer(null));
  });
});
