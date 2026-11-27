import { afterEach, describe, expect, it, vi } from 'vitest';
import {
  chooseReplyPreference,
  chooseMissingInfo,
  readMissingInfo,
  personaInspiration,
  readPersona,
  readReplyPriority,
  replyDetailFields,
  replyPreferences,
  writePersona,
} from './agent-persona';

const partsOf = text => text.split(/(\r\n|\n|\r)/);
const preference = value => replyPreferences.find(item => item.value === value);
const legacyLines = replyPreferences.map(item => item.field + '：' + item.value);

afterEach(() => vi.restoreAllMocks());

describe('assistant reply priorities', () => {
  it('reads a unique legacy preference without choosing for an old combination', () => {
    expect(readReplyPriority(partsOf('回复篇幅：尽量简短'))).toBe(preference('尽量简短'));
    expect(readReplyPriority(partsOf('回复篇幅：尽量简短\n回答结构：先说结论'))).toBeNull();
    expect(readReplyPriority(partsOf('请优先给出判断依据。'))).toBeNull();
    const legacy = partsOf('回答结构：先说结论\n回复优先项：有疑问先问我');
    expect(readReplyPriority(legacy)).toBe(preference('先说结论'));
    expect(readMissingInfo(legacy)).toBe('先问我');
    const changed = partsOf(chooseReplyPreference(legacy, preference('尽量简短')));
    expect(readMissingInfo(changed)).toBe('先问我');
    expect(readReplyPriority(partsOf(chooseMissingInfo(changed, '先给一版，标出待定内容')))).toBe(preference('尽量简短'));
    for (const field of replyDetailFields) {
      expect(field.options.some(value => legacyLines.includes(field.title + '：' + value))).toBe(false);
    }
  });

  it('replaces a legacy combination only on selection, preserving unrelated details and rules', () => {
    const saved = {
      description: '性格：耐心\r\n\r\n回答结构：先说结论\r\n回复篇幅：详细展开\r\n互动习惯：有疑问先问我\r\n回复语气（自定义）：像老朋友\r\n手写回复篇幅：希望灵活处理。\r\n',
      rules: [{ occasion: '学习', instruction: '给我一个例子' }],
      boundaries: '发送前让我确认',
    };
    const form = readPersona(writePersona(saved));
    expect(form).toEqual(saved);
    const brief = chooseReplyPreference(partsOf(form.description), preference('尽量简短'));
    expect(brief).toBe('性格：耐心\r\n\r\n回复优先项：尽量简短\r\n互动习惯：有疑问先问我\r\n回复语气（自定义）：像老朋友\r\n手写回复篇幅：希望灵活处理。\r\n');
    expect(chooseReplyPreference(partsOf(brief), preference('尽量简短'))).toBe(brief);
    expect(readPersona(writePersona({ ...form, description: brief }))).toEqual({ ...saved, description: brief });
    expect(chooseReplyPreference(partsOf(saved.description), preference('给出判断依据'))).toContain('回复篇幅：详细展开');
  });

  it.each([0, 0.3, 0.6, 0.9])('drafts and rerolls a single priority (%s)', random => {
    vi.spyOn(Math, 'random').mockReturnValue(random);
    let previous = '';
    for (let i = 0; i < 3; i++) {
      const draft = personaInspiration(previous);
      const lines = draft.split('\n');
      const selected = readReplyPriority(lines);
      expect(lines.filter(line => line.startsWith('回复优先项：'))).toHaveLength(1);
      expect(lines.some(line => legacyLines.includes(line))).toBe(false);
      expect(selected).toBeTruthy();
      expect(selected).not.toBe(readReplyPriority(partsOf(previous)));
      if (selected.value === '尽量简短') expect(lines.some(line => line.startsWith('回复篇幅：'))).toBe(false);
      expect(lines.some(line => line.startsWith('性格：'))).toBe(true);
      previous = draft;
    }
  });

  it('suggests only unconfigured details when a short reply priority and custom traits already exist', () => {
    const existing = '回复优先项：尽量简短\n性格（自定义）：外冷内热\n回答结构：举例解释';
    for (const random of [0, 0.4, 0.7, 0.99]) {
      vi.spyOn(Math, 'random').mockReturnValue(random);
      const addition = personaInspiration('', existing);
      expect(addition).toBeTruthy();
      expect(addition).not.toMatch(/回复优先项|回复篇幅|性格|回答结构|回复模式/);
      expect(readReplyPriority(partsOf(existing + '\n' + addition))).toBe(preference('尽量简短'));
    }
  });
});
