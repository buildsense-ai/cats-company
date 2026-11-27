import React, { act } from 'react';
import { createRoot } from 'react-dom/client';
import { Simulate } from 'react-dom/test-utils';
import { expect, it, vi } from 'vitest';
import { api } from '../api';
import AgentPersonaPortrait from './agent-persona-portrait';
import * as history from '../utils/persona-portrait-history';

it('keeps the candidate private until adopted and sends the actual image on refinement', async () => {
  global.IS_REACT_ACT_ENVIRONMENT = true;
  const container = document.createElement('div');
  document.body.append(container);
  const root = createRoot(container);
  const records = new Map();
  vi.spyOn(history, 'readPortraitHistory').mockImplementation(async scope => [...records.values()].filter(item => item.scope === scope));
  vi.spyOn(history, 'storePortrait').mockImplementation(async (scope, item) => { records.set(item.id, { ...item, scope }); });
  const generate = vi.spyOn(api, 'generateAssistantPortrait').mockResolvedValue({ data: [{ b64_json: 'eA==' }] });
  const upload = vi.spyOn(api, 'uploadFile').mockResolvedValue({ url: '/uploads/portrait.jpg' });
  const update = vi.spyOn(api, 'updateBot').mockResolvedValue({});
  const onApplied = vi.fn();
  vi.stubGlobal('Image', class { naturalWidth = 1; naturalHeight = 1; async decode() {} });
  vi.spyOn(HTMLCanvasElement.prototype, 'getContext').mockReturnValue({ fillRect() {}, drawImage() {} });
  vi.spyOn(HTMLCanvasElement.prototype, 'toDataURL').mockReturnValue('data:image/jpeg;base64,eA==');
  const click = async label => {
    const button = [...container.querySelectorAll('button')].find(element => element.textContent === label);
    await act(async () => button.click());
  };
  try {
    await act(async () => root.render(<AgentPersonaPortrait ownerUID={7} agent={{ uid: 42, display_name: '测试助手' }}
      description={'日常穿搭（自定义）：蓝色工装\n怎么称呼我：小王\n发送邮件前先确认'} onApplied={onApplied} />));
    await click('生成形象');
    expect(generate).not.toHaveBeenCalled();
    await click('按设定生成');
    const [prompt, reference] = generate.mock.calls[0];
    expect(prompt).toContain('蓝色工装');
    expect(prompt).not.toMatch(/小王|邮件/);
    expect(reference).toBeNull();
    expect(update).not.toHaveBeenCalled();
    expect(records.size).toBe(1);
    await act(async () => Simulate.change(container.querySelector('textarea'), { target: { value: '微微笑' } }));
    await click('按描述微调');
    expect(generate.mock.calls[1][0]).toContain('微微笑');
    expect(generate.mock.calls[1][1]).toBe('data:image/jpeg;base64,eA==');
    expect(upload).not.toHaveBeenCalled();
    expect(records.size).toBe(2);
    await click('用作头像');
    expect(upload).toHaveBeenCalledTimes(1);
    expect(update).toHaveBeenCalledWith(42, { avatar_url: '/uploads/portrait.jpg' });
    expect(onApplied).toHaveBeenCalledWith(42, '/uploads/portrait.jpg');
    const reopen = async ownerUID => {
      await act(async () => root.render(<AgentPersonaPortrait key={ownerUID} ownerUID={ownerUID}
        agent={{ uid: 42, avatar_url: '/uploads/portrait.jpg' }} description="" />));
      await click('生成形象');
    };
    await reopen(99);
    expect(container.querySelector('summary')).toBeNull();
    await reopen(7);
    expect(container.querySelector('summary').textContent).toBe('历史形象（2）');
    expect(container.querySelectorAll('.cc-portrait-history button')).toHaveLength(2);
    expect(container.querySelector('.cc-portrait-history button[aria-label*="使用中"]')).toBeTruthy();
    await act(async () => container.querySelector('.cc-portrait-history button:not([aria-label*="使用中"])').click());
    expect(container.textContent).toContain('用作头像');
    expect(update).toHaveBeenCalledTimes(1);
  } finally {
    await act(async () => root.unmount());
    container.remove();
    vi.restoreAllMocks();
    vi.unstubAllGlobals();
  }
});
