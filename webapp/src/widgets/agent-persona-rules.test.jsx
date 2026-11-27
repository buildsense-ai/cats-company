import React, { act, useState } from 'react';
import { createRoot } from 'react-dom/client';
import { Simulate } from 'react-dom/test-utils';
import AgentPersonaRules from './agent-persona-rules';

function Harness() {
  const [form, setForm] = useState({ rules: [], boundaries: '周末先问我。' });
  return <AgentPersonaRules {...form} onChange={patch => setForm(current => ({ ...current, ...patch }))} />;
}

describe('persona rule suggestions', () => {
  let host;
  let root;
  beforeEach(async () => {
    global.IS_REACT_ACT_ENVIRONMENT = true;
    host = document.body.appendChild(document.createElement('div'));
    root = createRoot(host);
    await act(async () => root.render(<Harness />));
  });
  afterEach(async () => {
    await act(async () => root.unmount());
    host.remove();
  });
  const button = label => [...host.querySelectorAll('button')].find(item => item.textContent === label);
  const click = element => act(async () => element.click());
  const change = (element, value) => act(async () => Simulate.change(element, { target: { value } }));

  test('prefills an editable rule, prevents duplicates and restores focus on removal', async () => {
    const suggestion = button('帮我做选择时');
    await click(suggestion);
    const instruction = host.querySelector('.cc-persona-rule textarea');
    expect(document.activeElement).toBe(instruction);
    expect(instruction.value).toContain('理由');
    expect(suggestion.disabled).toBe(true);
    await click(suggestion);
    expect(host.querySelectorAll('.cc-persona-rule')).toHaveLength(1);
    await change(instruction, '只推荐一个。');
    await click(button('自定义约定'));
    const rows = host.querySelectorAll('.cc-persona-rule');
    expect(document.activeElement).toBe(rows[1].querySelector('input'));
    await change(rows[1].querySelector('input'), '写作时');
    expect(rows[1].querySelector('[data-incomplete="true"]')).not.toBeNull();
    await change(rows[1].querySelector('textarea'), '保留原来的称呼。');
    expect(rows[1].querySelector('[data-incomplete]')).toBeNull();
    await click(rows[0].querySelector('.cc-persona-remove'));
    expect(suggestion.disabled).toBe(false);
    expect(document.activeElement.value).toBe('写作时');
  });

  test('toggling a confirmation boundary preserves existing wording and the other choice', async () => {
    const boundaries = host.querySelector('#cc-persona-boundaries');
    await click(button('替我发消息'));
    await click(button('修改原文件'));
    expect(boundaries.value.split('\n')).toEqual(['周末先问我。', '替我发消息前，先跟我确认。', '修改原文件前，先跟我确认。']);
    await click(button('替我发消息'));
    expect(button('替我发消息').getAttribute('aria-pressed')).toBe('false');
    expect(boundaries.value).toBe('周末先问我。\n修改原文件前，先跟我确认。');
    await change(boundaries, '替我改任何文件前，先留个备份。');
    expect(button('修改原文件').getAttribute('aria-pressed')).toBe('false');
  });
});
