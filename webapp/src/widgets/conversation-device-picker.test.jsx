import React, { act } from 'react';
import { createRoot } from 'react-dom/client';
import { Simulate } from 'react-dom/test-utils';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import ConversationDevicePicker from './conversation-device-picker';
import { api } from '../api';

const auth = vi.hoisted(() => ({ token: 'fake-user-session', revision: 1 }));
vi.mock('../api', () => ({
  api: { getConversationDevice: vi.fn(), confirmConversationDevice: vi.fn() },
  getToken: () => auth.token,
  getAuthRevision: () => auth.revision,
  isCurrentAuthSession: (token, revision) => token === auth.token && revision === auth.revision,
}));

const directory = {
  status: 'needs_selection', revision: 'r1', selected_device_id: '', agent_name: 'Cloud Bot',
  devices: [
    { deviceId: 'a', displayName: '同名电脑', routable: true },
    { deviceId: 'b', displayName: '同名电脑', routable: true },
    { deviceId: 'offline', displayName: '离线电脑', routable: false },
  ],
};
const deferred = () => { let resolve; const promise = new Promise((done) => { resolve = done; }); return { promise, resolve }; };

describe('ConversationDevicePicker', () => {
  let container, root;
  beforeEach(() => {
    vi.clearAllMocks(); auth.token = 'fake-user-session'; auth.revision = 1;
    api.getConversationDevice.mockResolvedValue(directory);
    api.confirmConversationDevice.mockResolvedValue({ ...directory, revision: 'r2', status: 'selected', selected_device_id: 'b' });
    container = document.createElement('div'); document.body.appendChild(container); root = createRoot(container);
  });
  afterEach(async () => { await act(async () => root.unmount()); container.remove(); });
  const render = async (topicId = 'p2p_7_42', activeAgent = { uid: 42 }) => {
    await act(async () => root.render(<ConversationDevicePicker topicId={topicId} activeAgent={activeAgent} />));
  };
  const click = async (element) => { await act(async () => Simulate.click(element)); };
  const button = (text) => [...document.querySelectorAll('button')].find((element) => element.textContent === text);
  const open = async () => click(container.querySelector('.cc-device-trigger'));

  it('shows same names with IDs and offline devices, only commits after confirmation', async () => {
    await render(); await open();
    expect(document.querySelectorAll('input[type=radio]')).toHaveLength(3);
    expect(document.querySelector('input[value=offline]').disabled).toBe(true);
    expect(document.querySelector('[role=dialog]').textContent).toContain('Cloud Bot');
    await act(async () => Simulate.change(document.querySelector('input[value=b]')));
    expect(api.confirmConversationDevice).not.toHaveBeenCalled();
    await click(button('使用这台电脑'));
    expect(api.confirmConversationDevice).toHaveBeenCalledExactlyOnceWith('p2p_7_42', 42, 'b', 'r1');
    expect(document.querySelector('[role=dialog]')).toBeNull();
    expect(container.querySelector('.cc-device-trigger').title).toContain('同名电脑');
  });

  it('single-device default needs no confirmation to use', async () => {
    api.getConversationDevice.mockResolvedValue({ ...directory, status: 'automatic', selected_device_id: 'a', devices: directory.devices.slice(0, 1) });
    await render();
    const trigger = container.querySelector('.cc-device-trigger');
    expect(trigger.textContent).toBe('');
    expect(trigger.querySelectorAll('svg')).toHaveLength(1);
    expect(trigger.title).toContain('同名电脑');
    expect(trigger.getAttribute('aria-label')).toBe(trigger.title);
    expect(api.confirmConversationDevice).not.toHaveBeenCalled();
  });

  it('delegates to the shared computer panel instead of opening a second dialog', async () => {
    const onOpen = vi.fn();
    await act(async () => root.render(<ConversationDevicePicker topicId="p2p_7_42" activeAgent={{ uid: 42 }} onOpen={onOpen} />));
    await open();
    expect(onOpen).toHaveBeenCalledTimes(1);
    expect(document.querySelector('[role=dialog]')).toBeNull();
  });

  it('embeds selection without a second header, trigger, or repeated IDs', async () => {
    api.getConversationDevice.mockResolvedValue({ ...directory, status: 'selected', selected_device_id: 'a', devices: [
      { deviceId: 'a', displayName: '办公电脑', routable: true },
      { deviceId: 'b', displayName: '家里电脑', routable: true },
    ] });
    await act(async () => root.render(<ConversationDevicePicker topicId="p2p_7_42" activeAgent={{ uid: 42 }} embedded />));
    expect(container.querySelector('.cc-device-trigger')).toBeNull();
    expect(container.querySelector('h2')).toBeNull();
    expect(container.querySelector('.cc-device-option small')).toBeNull();
    expect(button('使用这台电脑')).toBeUndefined();
    await act(async () => Simulate.change(document.querySelector('input[value=b]')));
    expect(api.confirmConversationDevice).not.toHaveBeenCalled();
    await click(button('使用这台电脑'));
    expect(api.confirmConversationDevice).toHaveBeenCalledExactlyOnceWith('p2p_7_42', 42, 'b', 'r1');
    expect(container.querySelector('.cc-device-selection')).not.toBeNull();
  });

  it('refreshes other pickers after confirmation without reloading its own snapshot', async () => {
    await act(async () => root.render(<>
      <ConversationDevicePicker topicId="p2p_7_42" activeAgent={{ uid: 42 }} />
      <ConversationDevicePicker topicId="p2p_7_42" activeAgent={{ uid: 42 }} embedded />
    </>));
    expect(api.getConversationDevice).toHaveBeenCalledTimes(2);
    await act(async () => Simulate.change(container.querySelector('.cc-device-selection input[value=b]')));
    const confirmed = { ...directory, revision: 'r2', status: 'selected', selected_device_id: 'b' };
    api.getConversationDevice.mockResolvedValue(confirmed);
    await click(button('使用这台电脑'));
    expect(api.getConversationDevice).toHaveBeenCalledTimes(3);
    expect(container.querySelector('.cc-device-selection input[value=b]').checked).toBe(true);
    expect(container.querySelector('.cc-device-trigger').title).toContain('同名电脑');
    expect(button('使用这台电脑')).toBeUndefined();
    // Device updates from outside this instance still refresh both surfaces.
    await act(async () => window.dispatchEvent(new Event('cc:devices-changed')));
    expect(api.getConversationDevice).toHaveBeenCalledTimes(5);
  });

  it('shows motion while reading an embedded list and retains the list during refresh', async () => {
    const pending = deferred();
    api.getConversationDevice.mockReturnValueOnce(pending.promise);
    await act(async () => root.render(<ConversationDevicePicker topicId="p2p_7_42" activeAgent={{ uid: 42 }} embedded />));
    expect(container.querySelector('[data-loading-kind="connect"]')).not.toBeNull();
    expect(container.textContent).not.toContain('还没有连接电脑');
    await act(async () => pending.resolve(directory));
    expect(container.querySelector('[data-loading-kind]')).toBeNull();
    expect(container.querySelectorAll('input[type=radio]')).toHaveLength(3);
    const refresh = deferred();
    api.getConversationDevice.mockReturnValueOnce(refresh.promise);
    await click(container.querySelector('[aria-label="刷新电脑列表"]'));
    expect(container.querySelectorAll('input[type=radio]')).toHaveLength(3);
    expect(container.querySelector('[data-loading-kind]')).toBeNull();
    await act(async () => refresh.resolve(directory));
  });

  it('does not silently retry a conflicted or timed-out confirmation', async () => {
    api.confirmConversationDevice.mockRejectedValue(new Error('conflict'));
    await render(); await open();
    await act(async () => Simulate.change(document.querySelector('input[value=b]')));
    await click(button('使用这台电脑'));
    expect(document.querySelector('[role=alert]').textContent).toContain('不会自动重试');
    expect(button('使用这台电脑').disabled).toBe(true);
    expect(api.confirmConversationDevice).toHaveBeenCalledTimes(1);
    await click(document.querySelector('[aria-label="刷新电脑列表"]'));
    expect(api.confirmConversationDevice).toHaveBeenCalledTimes(1);
  });

  it('discards a previous conversation response', async () => {
    const old = deferred();
    api.getConversationDevice.mockReturnValueOnce(old.promise);
    await render('p2p_7_42');
    await render('grp_1');
    await act(async () => old.resolve({ ...directory, status: 'selected', selected_device_id: 'old-private', devices: [{ deviceId: 'old-private', displayName: 'PRIVATE' }] }));
    expect(container.textContent).not.toContain('PRIVATE');
    expect(api.getConversationDevice).toHaveBeenLastCalledWith('grp_1', 42);
  });

  it('clears data and ignores an in-flight confirmation after logout', async () => {
    const pending = deferred(); api.confirmConversationDevice.mockReturnValueOnce(pending.promise);
    await render(); await open();
    await act(async () => Simulate.change(document.querySelector('input[value=b]')));
    await click(button('使用这台电脑'));
    await act(async () => { auth.token = ''; auth.revision++; window.dispatchEvent(new Event('cc:auth-changed')); });
    await act(async () => pending.resolve({ ...directory, status: 'selected', selected_device_id: 'b' }));
    expect(document.querySelector('[role=dialog]')).toBeNull();
    expect(container.textContent).not.toContain('同名电脑');
  });

  it('keeps offline selection instead of showing online B as selected', async () => {
    api.getConversationDevice.mockResolvedValue({ ...directory, selected_device_id: 'offline', status: 'unavailable' });
    await render(); await open();
    expect(container.querySelector('.cc-device-trigger').title).toContain('离线');
    expect(document.querySelector('input[value=offline]').checked).toBe(true);
    expect(document.querySelector('input[value=b]').checked).toBe(false);
    expect(button('使用这台电脑').disabled).toBe(true);
  });

  it('lets group members select a Bot scope without committing a device', async () => {
    api.getConversationDevice.mockImplementation(async (_topic, agent) => agent ? directory : { status: 'choose_agent', agents: [{ uid: 'usr42', display_name: 'Cloud Bot' }, { uid: 'usr43', display_name: 'Other Bot' }] });
    await render('grp_1', null); await open();
    await click(button('Other Bot · usr43'));
    expect(api.getConversationDevice).toHaveBeenLastCalledWith('grp_1', 'usr43');
    expect(api.confirmConversationDevice).not.toHaveBeenCalled();
    expect(document.querySelector('[role=dialog]')).not.toBeNull();
  });

  it('supports Escape and does not request a device without a conversation', async () => {
    await render('', null);
    expect(container.textContent).toBe(''); expect(api.getConversationDevice).not.toHaveBeenCalled();
    await render(); await open();
    await act(async () => document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true })));
    expect(document.querySelector('[role=dialog]')).toBeNull();
  });
});
