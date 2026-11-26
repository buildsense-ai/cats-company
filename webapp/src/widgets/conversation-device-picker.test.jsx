import React, { act } from 'react';
import { createRoot } from 'react-dom/client';
import { Simulate } from 'react-dom/test-utils';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import ConversationDevicePicker from './conversation-device-picker';
import { api } from '../api';

const auth = vi.hoisted(() => ({ token: 'fake-user-session', revision: 1 }));
vi.mock('../api', () => ({
  api: { getConversationDevice: vi.fn(), confirmConversationDevice: vi.fn(), getGroupDeviceShares: vi.fn(), getDevices: vi.fn(), setGroupDeviceShare: vi.fn() },
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
    api.setGroupDeviceShare.mockResolvedValue({ notice: { message_id: 100 } });
    api.getDevices.mockResolvedValue({ devices: directory.devices.map(device => ({ ...device, ownerUserId: 'usr7' })) });
    api.getGroupDeviceShares.mockResolvedValue({
      actor_uid: 7, shares: [], devices: [], share_operations: { a: ['read_file', 'execute_shell'] },
      members: [
        { user_id: 7, display_name: 'Alice' }, { user_id: 8, display_name: 'Bob' },
        { user_id: 42, display_name: 'Cloud Bot', is_bot: true },
        { user_id: 43, display_name: 'Other Bot', is_bot: true },
      ],
    });
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
  const selectMemberPermission = async () => {
    await click(button('成员授权'));
    await act(async () => Simulate.change(document.querySelector('input[type=checkbox]'), { target: { checked: true } }));
  };
  const selectGroupComputer = async () => {
    await click(button('助手设备'));
    await act(async () => Simulate.change(document.querySelector('input[type=radio][aria-label^="为 Cloud Bot 选择"]')));
  };

  it('shows same names with IDs and offline devices, only commits after confirmation', async () => {
    await render(); await open();
    expect(document.querySelectorAll('input[type=radio]')).toHaveLength(4);
    expect(document.querySelector('input[value=offline]').disabled).toBe(true);
    expect(document.querySelector('[role=dialog]').textContent).toContain('Cloud Bot');
    await act(async () => Simulate.change(document.querySelector('input[value=b]')));
    expect(api.confirmConversationDevice).not.toHaveBeenCalled();
    await click(button('确认'));
    expect(api.confirmConversationDevice).toHaveBeenCalledExactlyOnceWith('p2p_7_42', 42, 'b', 'r1');
    expect(document.querySelector('[role=dialog]')).toBeNull();
    expect(container.querySelector('.cc-device-trigger').title).toContain('同名电脑');
  });

  it('shows the cloud default with one online device and no confirmed choice', async () => {
    api.getConversationDevice.mockResolvedValue({ ...directory, status: 'unspecified', devices: directory.devices.slice(0, 1) });
    await render(); await open();
    const trigger = container.querySelector('.cc-device-trigger');
    expect(trigger.title).toContain('：云端');
    expect(trigger.getAttribute('aria-label')).toBe(trigger.title);
    const cloud = document.querySelector('input[value=""]');
    expect(cloud.closest('label').textContent.trim()).toBe('云端');
    expect(cloud.checked).toBe(true);
    expect(document.querySelector('input[value=a]').checked).toBe(false);
    expect(button('确认').disabled).toBe(true);
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
    expect(button('确认').disabled).toBe(true);
    await act(async () => Simulate.change(document.querySelector('input[value=b]')));
    expect(api.confirmConversationDevice).not.toHaveBeenCalled();
    await click(button('确认'));
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
    await click(button('确认'));
    expect(api.getConversationDevice).toHaveBeenCalledTimes(3);
    expect(container.querySelector('.cc-device-selection input[value=b]').checked).toBe(true);
    expect(container.querySelector('.cc-device-trigger').title).toContain('同名电脑');
    expect(button('确认').disabled).toBe(true);
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
    expect(container.querySelectorAll('input[type=radio]')).toHaveLength(4);
    const refresh = deferred();
    api.getConversationDevice.mockReturnValueOnce(refresh.promise);
    await click(container.querySelector('[aria-label="刷新设备列表"]'));
    expect(container.querySelectorAll('input[type=radio]')).toHaveLength(4);
    expect(container.querySelector('[data-loading-kind]')).toBeNull();
    await act(async () => refresh.resolve(directory));
  });

  it('does not silently retry a conflicted or timed-out confirmation', async () => {
    api.confirmConversationDevice.mockRejectedValue(new Error('conflict'));
    await render(); await open();
    await act(async () => Simulate.change(document.querySelector('input[value=b]')));
    await click(button('确认'));
    expect(document.querySelector('[role=alert]').textContent).toContain('不会自动重试');
    expect(button('确认').disabled).toBe(true);
    expect(api.confirmConversationDevice).toHaveBeenCalledTimes(1);
    await click(document.querySelector('[aria-label="刷新设备列表"]'));
    expect(api.confirmConversationDevice).toHaveBeenCalledTimes(1);
  });

  it('discards a previous conversation response', async () => {
    const old = deferred();
    api.getConversationDevice.mockReturnValueOnce(old.promise);
    await render('p2p_7_42');
    await render('grp_1');
    await act(async () => old.resolve({ ...directory, status: 'selected', selected_device_id: 'old-private', devices: [{ deviceId: 'old-private', displayName: 'PRIVATE' }] }));
    expect(container.textContent).not.toContain('PRIVATE');
    expect(api.getConversationDevice.mock.calls.map(([topic, bot]) => [topic, bot])).toContainEqual(['grp_1', 'usr42']);
  });

  it('clears data and ignores an in-flight confirmation after logout', async () => {
    const pending = deferred(); api.confirmConversationDevice.mockReturnValueOnce(pending.promise);
    await render(); await open();
    await act(async () => Simulate.change(document.querySelector('input[value=b]')));
    await click(button('确认'));
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
    expect(button('确认').disabled).toBe(true);
  });

  it('switches assistants and member permissions in separate keyboard-accessible tabs', async () => {
    api.getConversationDevice.mockImplementation(async (_topic, agent) => agent ? directory : { status: 'choose_agent', agents: [{ uid: 'usr42', display_name: 'Cloud Bot' }, { uid: 'usr43', display_name: 'Other Bot' }] });
    await render('grp_1', null); await open();
    const requested = api.getConversationDevice.mock.calls.map(([topic, bot]) => [topic, bot]);
    expect(requested).toContainEqual(['grp_1', 'usr42']);
    expect(requested).toContainEqual(['grp_1', 'usr43']);
    const devicesTab = button('助手设备'), membersTab = button('成员授权');
    expect(devicesTab.getAttribute('aria-selected')).toBe('true');
    expect(membersTab.getAttribute('aria-selected')).toBe('false');
    devicesTab.focus();
    await act(async () => devicesTab.dispatchEvent(new KeyboardEvent('keydown', {key:'ArrowRight', bubbles:true, cancelable:true})));
    expect(document.activeElement).toBe(membersTab);
    expect(membersTab.getAttribute('aria-selected')).toBe('true');
    expect(document.querySelectorAll('input[type=checkbox]')).toHaveLength(3);
    await act(async () => membersTab.dispatchEvent(new KeyboardEvent('keydown', {key:'ArrowLeft', bubbles:true, cancelable:true})));
    expect(document.activeElement).toBe(devicesTab);
    expect(devicesTab.getAttribute('aria-selected')).toBe('true');
    expect(document.querySelectorAll('input[type=radio]')).toHaveLength(4);
    await click(document.querySelector('[aria-label="选择群内助手"]'));
    await click([...document.querySelectorAll('[role=option]')].find(option => option.textContent === 'Other Bot'));
    expect(document.querySelector('input[aria-label="为 Other Bot 切换到云端"]')).not.toBeNull();
    expect(api.confirmConversationDevice).not.toHaveBeenCalled();
    expect(document.querySelector('[role=dialog]')).not.toBeNull();
  });

  it('disables shared selection for an old Bot while keeping the new Bot available', async () => {
    const shared = { deviceId: 'pcB', displayName: 'pcB', ownerUserId: 'usr8', routable: true };
    api.getGroupDeviceShares.mockResolvedValue({
      actor_uid: 7, shares: [], devices: [shared],
      members: [{ user_id: 7 }, { user_id: 8, display_name: 'Bob' }, { user_id: 9, display_name: 'Charlie' }, { user_id: 42, display_name: 'New', is_bot: true }, { user_id: 43, display_name: 'Old', is_bot: true }],
    });
    api.getConversationDevice.mockImplementation(async (_topic, bot) => ({
      ...directory, devices: [{ ...shared, routable: bot === 'usr42', unavailableReason: bot === 'usr43' ? 'group_device_share_upgrade_required' : '' }],
    }));
    await render('grp_1'); await open();
    const sharedRadio = () => document.querySelector('input[type=radio][aria-label*="pcB"]');
    expect(sharedRadio().disabled).toBe(false);
    expect(sharedRadio().closest('label').textContent).toContain('由 Bob 授权');
    expect(document.querySelector('input[type=checkbox][aria-label*="pcB"]')).toBeNull();
    await act(async () => Simulate.change(sharedRadio()));
    await click(document.querySelector('[aria-label="选择群内助手"]'));
    await click([...document.querySelectorAll('[role=option]')].find(option => option.textContent === 'Old'));
    expect(sharedRadio().disabled).toBe(true);
    expect(sharedRadio().title).toContain('升级');
    expect(button('确认').disabled).toBe(false);
    expect(api.confirmConversationDevice).not.toHaveBeenCalled();
    await click(button('确认'));
    expect(api.confirmConversationDevice).toHaveBeenCalledExactlyOnceWith('grp_1', 'usr42', 'pcB', 'r1', 8);
    expect(api.setGroupDeviceShare).not.toHaveBeenCalled();
  });

  it('supports Escape and does not request a device without a conversation', async () => {
    await render('', null);
    expect(container.textContent).toBe(''); expect(api.getConversationDevice).not.toHaveBeenCalled();
    await render(); await open();
    await act(async () => document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true })));
    expect(document.querySelector('[role=dialog]')).toBeNull();
  });
  it('keeps group sharing and default edits local until confirmation', async () => {
    await render('grp_1'); await open();
    await selectMemberPermission();
    await selectGroupComputer();
    await click(button('成员授权'));
    expect(document.querySelector('input[type=checkbox]').checked).toBe(true);
    expect(api.setGroupDeviceShare).not.toHaveBeenCalled();
    expect(api.confirmConversationDevice).not.toHaveBeenCalled();
    expect(document.querySelector('[role=dialog]').textContent).not.toContain('项待确认');
    await click(button('确认'));
    expect(api.setGroupDeviceShare).toHaveBeenCalledExactlyOnceWith({ action: 'share', topic_id: 'grp_1', grantee_uid: 8, device_id: 'a', expected_revision: 0, expected_operations: ['read_file', 'execute_shell'] });
    expect(api.confirmConversationDevice).toHaveBeenCalledExactlyOnceWith('grp_1', 'usr42', 'a', 'r1', 7);
  });

  it('keeps own devices available when one group Bot cannot be queried', async () => {
    api.getConversationDevice.mockImplementation(async (_topic, bot) => {
      if (bot === 'usr43') throw new Error('403 bot unavailable');
      return directory;
    });
    await render('grp_1'); await open();
    expect(document.querySelector('[role=dialog] [aria-label="Cloud Bot 的设备"]')).not.toBeNull();
    const radios = [...document.querySelectorAll('input[type=radio]')];
    expect(radios.some(input => !input.disabled)).toBe(true);
  });

  it('cancels draft group edits without saving', async () => {
    await render('grp_1'); await open();
    expect(button('取消更改').disabled).toBe(true);
    expect(button('确认').disabled).toBe(true);
    await selectMemberPermission();
    expect(button('确认').disabled).toBe(false);
    await click(button('取消更改'));
    expect(document.querySelector('input[type=checkbox]').checked).toBe(false);
    expect(button('取消更改').disabled).toBe(true);
    expect(button('确认').disabled).toBe(true);
    expect(api.setGroupDeviceShare).not.toHaveBeenCalled();
  });

  it('clears a private default only after confirmation', async () => {
    api.getConversationDevice.mockResolvedValue({ ...directory, status: 'selected', selected_device_id: 'a' });
    api.confirmConversationDevice.mockResolvedValue({ ...directory, status: 'unspecified', selected_device_id: '', revision: 'r2' });
    await render(); await open();
    await act(async () => Simulate.change(document.querySelector('input[value=""]')));
    expect(api.confirmConversationDevice).not.toHaveBeenCalled();
    expect(button('确认').disabled).toBe(false);
    await click(button('确认'));
    expect(api.confirmConversationDevice).toHaveBeenCalledExactlyOnceWith('p2p_7_42', 42, '', 'r1');
    expect(container.querySelector('.cc-device-trigger').title).toContain('：云端');
  });

  it('clears one group Bot default without changing device sharing', async () => {
    api.getConversationDevice.mockResolvedValue({ ...directory, status: 'selected', selected_device_id: 'a', selected_owner_uid: 7 });
    await render('grp_1'); await open();
    const cloud = document.querySelector('input[aria-label="为 Cloud Bot 切换到云端"]');
    expect(cloud.closest('label').textContent.trim()).toBe('云端');
    await act(async () => Simulate.change(cloud));
    expect(api.confirmConversationDevice).not.toHaveBeenCalled();
    await click(button('确认'));
    expect(api.confirmConversationDevice).toHaveBeenCalledExactlyOnceWith('grp_1', 'usr42', '', 'r1', undefined);
    expect(api.setGroupDeviceShare).not.toHaveBeenCalled();
  });

  it('allows clearing the only offline selected device', async () => {
    api.getConversationDevice.mockResolvedValue({ ...directory, status: 'unavailable', selected_device_id: 'offline', devices: directory.devices.slice(2) });
    await render(); await open();
    await act(async () => Simulate.change(document.querySelector('input[value=""]')));
    expect(button('确认').disabled).toBe(false);
    await click(button('确认'));
    expect(api.confirmConversationDevice).toHaveBeenCalledExactlyOnceWith('p2p_7_42', 42, '', 'r1');
  });

  it('keeps a persisted private selection while warning that its notice is pending', async () => {
    api.confirmConversationDevice.mockResolvedValue({ ...directory, status: 'selected', selected_device_id: 'b', notice: { pending: true } });
    await render(); await open();
    await act(async () => Simulate.change(document.querySelector('input[value=b]')));
    await click(button('确认'));
    expect(document.querySelector('[role=alert]').textContent).toContain('设备已保存');
    expect(document.querySelector('input[value=b]').checked).toBe(true);
    expect(button('确认').disabled).toBe(true);
    expect(api.confirmConversationDevice).toHaveBeenCalledTimes(1);
  });

  it('does not retry a partially committed group confirmation', async () => {
    api.confirmConversationDevice.mockRejectedValue(new Error('timeout'));
    await render('grp_1'); await open();
    await selectMemberPermission();
    await selectGroupComputer();
    await click(button('确认'));
    expect(document.querySelector('[role=alert]').textContent).toContain('已确认 1 项');
    expect(button('确认').disabled).toBe(true);
    await act(async () => window.dispatchEvent(new Event('focus')));
    expect(api.setGroupDeviceShare).toHaveBeenCalledTimes(1);
    expect(api.confirmConversationDevice).toHaveBeenCalledTimes(1);
  });

  it('stops the rest of a confirmation batch after account switch', async () => {
    const pending = deferred();
    api.setGroupDeviceShare.mockReturnValue(pending.promise);
    await render('grp_1'); await open();
    await selectMemberPermission();
    await selectGroupComputer();
    await click(button('确认'));
    await act(async () => {
      auth.token = 'next-user'; auth.revision++;
      window.dispatchEvent(new Event('cc:auth-changed'));
      pending.resolve({ notice: { message_id: 101 } });
    });
    expect(api.confirmConversationDevice).not.toHaveBeenCalled();
    expect(document.querySelector('[role=dialog]')).toBeNull();
  });

});
