import React, { act } from 'react';
import { createRoot } from 'react-dom/client';
import { Simulate } from 'react-dom/test-utils';
import { afterEach, beforeEach, expect, it, vi } from 'vitest';
import GroupDeviceSharing from './group-device-sharing';
import { api } from '../api';

vi.mock('../api', () => ({
  api: { getGroupDeviceShares: vi.fn(), getDevices: vi.fn(), getConversationDevice: vi.fn(), setGroupDeviceShare: vi.fn() },
  getToken: () => 'test-token', getAuthRevision: () => 1, isCurrentAuthSession: () => true,
}));
let root, container;
const roster = {
  actor_uid: 8, shares: [], devices: [],
  members: [{ user_id: 8 }, { user_id: 42, display_name: '工作助手', is_bot: true }],
};
beforeEach(() => {
  vi.clearAllMocks();
  container = document.createElement('div'); document.body.appendChild(container); root = createRoot(container);
  api.getDevices.mockResolvedValue({ devices: [{deviceId:'pc',ownerUserId:'usr8',displayName:'pcA',runtimeRole:'desktop'}] });
  api.getGroupDeviceShares.mockResolvedValue({actor_uid:8,shares:[],devices:[],members:[{user_id:8,username:'owner'},{user_id:7,username:'userB'}],share_operations:{pc:['read_file','write_file','execute_shell']}});
  api.setGroupDeviceShare.mockResolvedValue({status:'active'});
});
afterEach(async () => { await act(async () => root.unmount()); container.remove(); });
it('completes a slow first snapshot despite overlapping poll, focus and device notifications', async () => {
  vi.useFakeTimers();
  try {
    let resolveSnapshot;
    api.getGroupDeviceShares.mockResolvedValue(roster);
    api.getConversationDevice.mockReturnValueOnce(new Promise(resolve => { resolveSnapshot = resolve; }));
    await act(async () => root.render(<GroupDeviceSharing topicId="grp_1" embedded />));
    await act(async () => {
      await vi.advanceTimersByTimeAsync(9000);
      window.dispatchEvent(new Event('focus'));
      window.dispatchEvent(new Event('cc:devices-changed'));
    });
    expect(api.getGroupDeviceShares).toHaveBeenCalledTimes(1);
    expect(api.getConversationDevice).toHaveBeenCalledTimes(1);
    await act(async () => resolveSnapshot({ status: 'unspecified', devices: [] }));
    expect(container.querySelector('[aria-label="为 工作助手 切换到云端"]')).not.toBeNull();
    api.getConversationDevice.mockResolvedValue({ status: 'unspecified', devices: [] });
    await act(async () => vi.advanceTimersByTimeAsync(3000));
    expect(api.getGroupDeviceShares).toHaveBeenCalledTimes(2);
  } finally { vi.useRealTimers(); }
});

it('removes an initial read failure when a later background read succeeds', async () => {
  api.getDevices.mockRejectedValueOnce(new Error('offline'));
  await act(async () => root.render(<GroupDeviceSharing topicId="grp_1" embedded />));
  expect(container.querySelector('[role=alert]')).not.toBeNull();
  await act(async () => window.dispatchEvent(new Event('focus')));
  expect(container.querySelector('[role=alert]')).toBeNull();
  expect(container.textContent).toContain('群内暂无助手');
});

it('aborts an obsolete load before fetching the previous group’s assistant snapshots', async () => {
  let resolveOld;
  api.getGroupDeviceShares.mockReturnValueOnce(new Promise(resolve => { resolveOld = resolve; }));
  await act(async () => root.render(<GroupDeviceSharing topicId="grp_1" embedded />));
  const [{ signal, timeoutMs }] = api.getDevices.mock.calls[0];
  expect(timeoutMs).toBeGreaterThan(3000);
  await act(async () => root.render(<GroupDeviceSharing topicId="grp_2" embedded />));
  expect(signal.aborted).toBe(true);
  await act(async () => resolveOld(roster));
  expect(api.getConversationDevice).not.toHaveBeenCalled();
});
function switchTab(label) {
  const tab = [...container.querySelectorAll('[role=tab]')].find(element => element.textContent === label);
  return act(async () => tab.click());
}
it('submits the staged scope on confirmation without listing permission details', async () => {
  await act(async () => root.render(<GroupDeviceSharing topicId="grp_1" embedded />));
  await switchTab('成员授权');
  expect(container.querySelector('[aria-label="共享授权范围"]')).toBeNull();
  await act(async () => Simulate.change(container.querySelector('input[type=checkbox]'), {target:{checked:true}}));
  await switchTab('助手设备');
  expect(container.textContent).not.toContain('读取文件、写入文件、执行命令');
  expect(api.setGroupDeviceShare).not.toHaveBeenCalled();
  await act(async () => Simulate.click([...container.querySelectorAll('button')].find(button => button.textContent==='确认')));
  expect(api.setGroupDeviceShare).toHaveBeenCalledWith({action:'share',topic_id:'grp_1',grantee_uid:7,device_id:'pc',expected_revision:0,expected_operations:['read_file','write_file','execute_shell']});
});
it('blocks a new share when its operation scope cannot be confirmed', async () => {
  api.getGroupDeviceShares.mockResolvedValue({actor_uid:8,shares:[],devices:[],members:[{user_id:8},{user_id:7,username:'userB'}],share_operations:{pc:['unrecognized-new-operation']}});
  await act(async () => root.render(<GroupDeviceSharing topicId="grp_1" embedded />));
  await switchTab('成员授权');
  await act(async () => Simulate.change(container.querySelector('input[type=checkbox]'),{target:{checked:true}}));
  expect([...container.querySelectorAll('button')].find(button => button.textContent==='确认').disabled).toBe(true);
  expect(api.setGroupDeviceShare).not.toHaveBeenCalled();
});
