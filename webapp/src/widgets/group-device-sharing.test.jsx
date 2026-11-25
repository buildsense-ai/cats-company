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
beforeEach(() => {
  vi.clearAllMocks();
  container = document.createElement('div'); document.body.appendChild(container); root = createRoot(container);
  api.getDevices.mockResolvedValue({ devices: [{deviceId:'pc',ownerUserId:'usr8',displayName:'pcA',runtimeRole:'desktop'}] });
  api.getGroupDeviceShares.mockResolvedValue({actor_uid:8,shares:[],devices:[],members:[{user_id:8,username:'owner'},{user_id:7,username:'userB'}],share_operations:{pc:['read_file','write_file','execute_shell']}});
  api.setGroupDeviceShare.mockResolvedValue({status:'active'});
});
afterEach(async () => { await act(async () => root.unmount()); container.remove(); });
it('shows and submits the exact scope only for a staged new share', async () => {
  await act(async () => root.render(<GroupDeviceSharing topicId="grp_1" embedded />));
  expect(container.querySelector('[aria-label="共享授权范围"]')).toBeNull();
  await act(async () => Simulate.change(container.querySelector('input[type=checkbox]'), {target:{checked:true}}));
  expect(container.querySelector('[aria-label="共享授权范围"]').textContent).toContain('读取文件、写入文件、执行命令');
  expect(api.setGroupDeviceShare).not.toHaveBeenCalled();
  await act(async () => Simulate.click([...container.querySelectorAll('button')].find(button => button.textContent==='确认')));
  expect(api.setGroupDeviceShare).toHaveBeenCalledWith({action:'share',topic_id:'grp_1',grantee_uid:7,device_id:'pc',expected_revision:0,expected_operations:['read_file','write_file','execute_shell']});
});
it('blocks a new share when its operation scope cannot be confirmed', async () => {
  api.getGroupDeviceShares.mockResolvedValue({actor_uid:8,shares:[],devices:[],members:[{user_id:8},{user_id:7,username:'userB'}],share_operations:{pc:['unrecognized-new-operation']}});
  await act(async () => root.render(<GroupDeviceSharing topicId="grp_1" embedded />));
  await act(async () => Simulate.change(container.querySelector('input[type=checkbox]'),{target:{checked:true}}));
  expect([...container.querySelectorAll('button')].find(button => button.textContent==='确认').disabled).toBe(true);
  expect(api.setGroupDeviceShare).not.toHaveBeenCalled();
});
