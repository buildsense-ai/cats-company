import React, { act } from 'react';
import { createRoot } from 'react-dom/client';
import { Simulate } from 'react-dom/test-utils';
import { afterEach, beforeEach, expect, it, vi } from 'vitest';
import GroupDeviceShareCard from './group-device-share-card';
import { api } from '../api';

vi.mock('../api', () => ({
  api: { getGroupDeviceShareRequest: vi.fn(), resolveGroupDeviceShareRequest: vi.fn() },
  getToken: () => 'local-test-token', getAuthRevision: () => 1,
  isCurrentAuthSession: (token, revision) => token === 'local-test-token' && revision === 1,
}));
let root, container;
const reference = { topic_id: 'grp_2', request_id: 'request-b' };
const pending = {
  can_resolve: true,
  request: { topic_id: 'grp_2', message_id: 100, status: 'pending', revision: 4, description: '申请使用 pcB' },
  devices: [{ deviceId: 'pcb', displayName: 'pcB' }],
  share_operations: { pcb: ['read_file', 'write_file', 'execute_shell'] },
};

beforeEach(() => {
  vi.clearAllMocks();
  container = document.createElement('div');
  document.body.appendChild(container);
  root = createRoot(container);
});
afterEach(async () => {
  await act(async () => root.unmount());
  container.remove();
});
const render = async () => { await act(async () => root.render(<GroupDeviceShareCard reference={reference} sourceTopicId="grp_2" sourceMessageId={100} />)); };

it('shows approval controls only when the server identifies the current owner', async () => {
  api.getGroupDeviceShareRequest.mockResolvedValue({ ...pending, can_resolve: false, devices: undefined });
  await render();
  expect(container.textContent).toContain('等待设备所有者处理');
  expect(container.querySelector('select')).toBeNull();
  expect(container.querySelector('button')).toBeNull();
});

it('requires an owner-selected device and submits the server revision', async () => {
  api.getGroupDeviceShareRequest.mockResolvedValue(pending);
  api.resolveGroupDeviceShareRequest.mockResolvedValue({ status: 'approved' });
  await render();
  const approve = [...container.querySelectorAll('button')].find(button => button.textContent === '授权所选设备');
  expect(approve.disabled).toBe(true);
  await act(async () => Simulate.change(container.querySelector('select'), { target: { value: 'pcb' } }));
  expect(approve.disabled).toBe(false);
  expect(container.querySelector('[aria-label="授权范围"]').textContent).toContain('读取文件、写入文件、执行命令');
  api.getGroupDeviceShareRequest.mockResolvedValue({
    request: { ...pending.request, status: 'approved', revision: 5 }, can_resolve: true,
  });
  await act(async () => Simulate.click(approve));
  expect(api.resolveGroupDeviceShareRequest).toHaveBeenCalledWith({
    action: 'approve', topic_id: 'grp_2', request_id: 'request-b', expected_revision: 4, device_id: 'pcb', expected_operations: ['read_file', 'write_file', 'execute_shell'],
  });
  expect(container.textContent).toContain('已授权');
});

it('rejects a reference copied from a different group before loading private state', async () => {
  await act(async () => root.render(<GroupDeviceShareCard reference={reference} sourceTopicId="grp_8" sourceMessageId={100} />));
  expect(api.getGroupDeviceShareRequest).not.toHaveBeenCalled();
  expect(container.querySelector('button')).toBeNull();
});

it('distinguishes a saved approval from a pending chat notice', async () => {
  api.getGroupDeviceShareRequest.mockResolvedValue(pending);
  api.resolveGroupDeviceShareRequest.mockResolvedValue({ status: 'approved', notice: { pending: true } });
  await render();
  await act(async () => Simulate.change(container.querySelector('select'), { target: { value: 'pcb' } }));
  api.getGroupDeviceShareRequest.mockResolvedValue({ request: { ...pending.request, status: 'approved', revision: 5 }, can_resolve: true });
  await act(async () => Simulate.click([...container.querySelectorAll('button')].find(button => button.textContent === '授权所选设备')));
  expect(container.textContent).toContain('授权已保存，对话提示暂未确认');
  expect(container.querySelector('select')).toBeNull();
});

it('keeps a copied card in the same group read-only', async () => {
  api.getGroupDeviceShareRequest.mockResolvedValue(pending);
  await act(async () => root.render(<GroupDeviceShareCard reference={reference} sourceTopicId="grp_2" sourceMessageId={101} />));
  expect(container.querySelector('select')).toBeNull();
  expect(container.querySelector('button')).toBeNull();
});

it('identifies a Bot-generated request and its originating human message', async () => {
  api.getGroupDeviceShareRequest.mockResolvedValue(pending);
  await act(async () => root.render(<GroupDeviceShareCard reference={{...reference, requested_by_bot: 'usr3', requested_by_name: 'botB', requester_name: 'userA', origin_message_id: 42}} sourceTopicId="grp_2" sourceMessageId={100} />));
  expect(container.textContent).toContain('botB 代 userA 申请 · 原消息 #42');
});

it('does not render arbitrary objects from untrusted attribution fields', async () => {
  api.getGroupDeviceShareRequest.mockResolvedValue(pending);
  await act(async () => root.render(<GroupDeviceShareCard reference={{...reference, requested_by_bot: 'usr3', requested_by_name: {}, requester_name: {}, origin_message_id: {}}} sourceTopicId="grp_2" sourceMessageId={101} />));
  expect(container.textContent).toContain('Bot 代 群成员 申请');
  expect(container.textContent).not.toContain('原消息 #');
  expect(container.querySelector('button')).toBeNull();
});

it('requires a known operation scope before approval', async () => {
  api.getGroupDeviceShareRequest.mockResolvedValue({ ...pending, share_operations: {} });
  await render();
  await act(async () => Simulate.change(container.querySelector('select'), { target: { value: 'pcb' } }));
  expect([...container.querySelectorAll('button')].find(button => button.textContent === '授权所选设备').disabled).toBe(true);
  expect(api.resolveGroupDeviceShareRequest).not.toHaveBeenCalled();
});

it('clears a stale scope after a failed confirmation and reads the current request', async () => {
  api.getGroupDeviceShareRequest.mockResolvedValue(pending);
  api.resolveGroupDeviceShareRequest.mockRejectedValue(new Error('409 scope changed'));
  await render();
  await act(async () => Simulate.change(container.querySelector('select'), { target: { value: 'pcb' } }));
  await act(async () => Simulate.click([...container.querySelectorAll('button')].find(button => button.textContent === '授权所选设备')));
  expect(container.querySelector('select').value).toBe('');
  expect([...container.querySelectorAll('button')].find(button => button.textContent === '授权所选设备').disabled).toBe(true);
  expect(api.resolveGroupDeviceShareRequest).toHaveBeenCalledTimes(1);
  expect(api.getGroupDeviceShareRequest).toHaveBeenCalledTimes(2);
});
