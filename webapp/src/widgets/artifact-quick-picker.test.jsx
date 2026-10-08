import React, { act } from 'react';
import { createRoot } from 'react-dom/client';
import { Simulate } from 'react-dom/test-utils';
import ArtifactQuickPicker from './artifact-quick-picker';
import { api } from '../api';

vi.mock('../api', () => ({ api: { getTopicFiles: vi.fn(), listArtifactApps: vi.fn() } }));

describe('ArtifactQuickPicker', () => {
  let container;
  let root;
  let onSelect;
  const panel = () => document.querySelector('[aria-label="选择产物"]');
  const trigger = () => container.querySelector('button');
  const render = async props => {
    await act(async () => root.render(<ArtifactQuickPicker agentUid={43} topicId="p2p_1_43" onSelect={onSelect} {...props} />));
  };
  const openSection = async index => {
    await act(async () => Simulate.keyDown(trigger(), { key: 'ArrowDown' }));
    await act(async () => panel().querySelectorAll('.cc-artifact-picker-heading')[index].click());
  };
  beforeEach(async () => {
    global.IS_REACT_ACT_ENVIRONMENT = true;
    vi.clearAllMocks();
    onSelect = vi.fn();
    api.getTopicFiles.mockResolvedValue({ files: [] });
    api.listArtifactApps.mockResolvedValue({ apps: [] });
    container = document.createElement('div'); document.body.appendChild(container); root = createRoot(container);
    await render();
  });
  afterEach(async () => { await act(async () => root.unmount()); container.remove(); vi.useRealTimers(); });

  it('opens on mouse hover and lets the pointer enter the panel before the close delay', async () => {
    vi.useFakeTimers();
    await act(async () => Simulate.pointerEnter(trigger(), { pointerType: 'mouse' }));
    expect(panel()).toBeNull();
    await act(async () => vi.advanceTimersByTime(140));
    expect(panel()).toBeTruthy();
    expect(api.getTopicFiles).not.toHaveBeenCalled();
    await act(async () => { Simulate.pointerLeave(trigger()); Simulate.pointerEnter(panel()); vi.advanceTimersByTime(300); });
    expect(panel()).toBeTruthy();
    await act(async () => { Simulate.pointerLeave(panel()); vi.advanceTimersByTime(220); });
    expect(panel()).toBeNull();
  });

  it('sorts current conversation files and selects a file directly', async () => {
    const old = { id: '1:0', name: '旧报告.pdf', url: '/old.pdf', created_at: '2026-10-01' };
    const latest = { id: '2:0', name: '最新报告.pdf', url: '/latest.pdf', created_at: '2026-10-08' };
    api.getTopicFiles.mockResolvedValue({ files: [old, latest], has_more: true });
    await openSection(0);
    expect(api.getTopicFiles).toHaveBeenCalledWith('p2p_1_43', { limit: 12 });
    expect(panel().querySelector('.cc-artifact-picker-item').textContent).toContain('最新报告');
    await act(async () => panel().querySelector('.cc-artifact-picker-item').click());
    expect(onSelect).toHaveBeenCalledWith({ initialTab: 'files', file: latest });
    expect(panel()).toBeNull();
  });

  it('uses the selected Agent catalog and opens an application directly', async () => {
    const app = { id: 'workbench', title: '工作台', url: 'https://artifact.catsco.cc/workbench/' };
    api.listArtifactApps.mockResolvedValue({ apps: [app] });
    await openSection(1);
    expect(api.listArtifactApps).toHaveBeenCalledWith(43);
    await act(async () => panel().querySelector('.cc-artifact-picker-item').click());
    expect(onSelect).toHaveBeenCalledWith({ initialTab: 'gateway', app });
  });

  it('does not fetch an unscoped application catalog when a conversation has no Agent', async () => {
    await render({ agentUid: 0 });
    await openSection(1);
    expect(panel().textContent).toContain('当前会话没有关联 Agent');
    expect(api.listArtifactApps).not.toHaveBeenCalled();
  });

  it('keeps a keyboard-opened picker open after pointer leave and closes on Escape with focus restored', async () => {
    vi.useFakeTimers();
    await openSection(0);
    await act(async () => { Simulate.pointerLeave(trigger()); vi.advanceTimersByTime(500); });
    expect(panel()).toBeTruthy();
    await act(async () => Simulate.keyDown(panel(), { key: 'Escape' }));
    expect(panel()).toBeNull();
    expect(document.activeElement).toBe(trigger());
  });

  it('opens the original full panel on click, including after hovering', async () => {
    vi.useFakeTimers();
    await act(async () => Simulate.pointerEnter(trigger(), { pointerType: 'mouse' }));
    await act(async () => vi.advanceTimersByTime(140));
    expect(panel()).toBeTruthy();
    await act(async () => trigger().click());
    expect(panel()).toBeNull();
    expect(onSelect).toHaveBeenCalledWith({ initialTab: 'files' });
    expect(api.getTopicFiles).not.toHaveBeenCalled();
    await render({ topicId: '' });
    await act(async () => trigger().click());
    expect(onSelect).toHaveBeenLastCalledWith({ initialTab: 'active' });
  });

  it('discards late responses when the conversation and Agent change', async () => {
    let resolve;
    api.getTopicFiles.mockReturnValue(new Promise(done => { resolve = done; }));
    await openSection(0);
    await render({ agentUid: 44, topicId: 'p2p_1_44' });
    expect(panel()).toBeNull();
    api.getTopicFiles.mockResolvedValue({ files: [{ id: '44', name: '新会话.pdf', url: '/new.pdf' }] });
    await openSection(0);
    await act(async () => resolve({ files: [{ id: '43', name: '旧会话.pdf', url: '/old.pdf' }] }));
    expect(panel().textContent).toContain('新会话.pdf');
    expect(panel().textContent).not.toContain('旧会话.pdf');
  });

  it('offers retry after a failed request without closing the picker', async () => {
    api.listArtifactApps.mockRejectedValueOnce(new Error('offline'));
    await openSection(1);
    expect(panel().querySelector('[role="alert"]').textContent).toContain('应用加载失败');
    await act(async () => panel().querySelector('.cc-artifact-picker-error button').click());
    expect(panel().textContent).toContain('这个 Agent 还没有共享应用');
    expect(api.listArtifactApps).toHaveBeenCalledTimes(2);
  });

  it('ends a stalled load and lets users retry without a late response replacing the new list', async () => {
    vi.useFakeTimers();
    let resolve;
    api.getTopicFiles.mockReturnValueOnce(new Promise(done => { resolve = done; }));
    await openSection(0);
    await act(async () => vi.advanceTimersByTime(12000));
    expect(panel().querySelector('[role="alert"]').textContent).toContain('加载超时');
    api.getTopicFiles.mockResolvedValue({ files: [{ id: 'new', name: '新文件.pdf', url: '/new.pdf' }] });
    await act(async () => panel().querySelector('.cc-artifact-picker-error button').click());
    await act(async () => resolve({ files: [{ id: 'old', name: '旧文件.pdf', url: '/old.pdf' }] }));
    expect(panel().textContent).toContain('新文件.pdf');
    expect(panel().textContent).not.toContain('旧文件.pdf');
  });
});
