import React, { act } from 'react';
import { createRoot } from 'react-dom/client';
import { Simulate } from 'react-dom/test-utils';

vi.mock('../api', () => ({
  api: {
    getFriends: vi.fn(),
    getAgents: vi.fn(),
    createGroup: vi.fn(),
  },
}));

import { api } from '../api';
import CreateGroup from './create-group';

async function flushPromises() {
  await Promise.resolve();
  await Promise.resolve();
}

describe('CreateGroup member candidates', () => {
  let container;
  let root;

  beforeEach(() => {
    global.IS_REACT_ACT_ENVIRONMENT = true;
    Object.values(api).forEach((mock) => mock.mockReset());
    api.getFriends.mockResolvedValue({
      friends: [
        { id: 8, username: 'alice', display_name: 'Alice' },
        { id: 42, username: 'virtual-catsco', display_name: 'Virtual Catsco' },
        { id: 44, username: 'legacy-agent', display_name: 'Legacy Agent', bot: true },
      ],
    });
    api.getAgents.mockResolvedValue({
      agents: [
        { id: 42, uid: 42, username: 'virtual-catsco', display_name: 'Virtual Catsco', relation: 'owner', is_bot: true },
        { id: 43, uid: 43, username: 'outside-agent', display_name: 'Owned Outside Agent', relation: 'owner', is_bot: true },
      ],
    });
    api.createGroup.mockResolvedValue({ group_id: 88, topic: 'grp_88' });
    container = document.createElement('div');
    document.body.appendChild(container);
    root = createRoot(container);
  });

  afterEach(async () => {
    await act(async () => root.unmount());
    container.remove();
  });

  async function mount(props = {}) {
    await act(async () => {
      root.render(<CreateGroup onClose={vi.fn()} {...props} />);
      await flushPromises();
    });
  }

  it('keeps unmarked Agent friends out of the friend tab and includes owned Agents outside friends', async () => {
    await mount();

    let names = Array.from(container.querySelectorAll('.oc-member-picker-item strong')).map((node) => node.textContent);
    expect(names).toEqual(['Alice']);

    const agentTab = Array.from(container.querySelectorAll('[role="tablist"] button'))
      .find((button) => button.textContent.trim() === 'Agent');
    await act(async () => Simulate.click(agentTab));

    names = Array.from(container.querySelectorAll('.oc-member-picker-item strong')).map((node) => node.textContent);
    expect(names).toEqual(['Virtual Catsco', 'Owned Outside Agent', 'Legacy Agent']);
    expect(names.filter((name) => name === 'Virtual Catsco')).toHaveLength(1);
  });

  it('submits the selected Agent canonical ID when creating the group', async () => {
    const onClose = vi.fn();
    const onCreated = vi.fn();
    await mount({ onClose, onCreated });

    const agentTab = Array.from(container.querySelectorAll('[role="tablist"] button'))
      .find((button) => button.textContent.trim() === 'Agent');
    await act(async () => Simulate.click(agentTab));

    const ownedAgentRow = Array.from(container.querySelectorAll('.oc-member-picker-item'))
      .find((row) => row.textContent.includes('Owned Outside Agent'));
    await act(async () => Simulate.change(ownedAgentRow.querySelector('input[type="checkbox"]')));

    const nameInput = container.querySelector('.oc-collaboration-input');
    await act(async () => Simulate.change(nameInput, { target: { value: 'Agent Review Group' } }));

    const createButton = Array.from(container.querySelectorAll('.oc-collaboration-modal-footer button'))
      .find((button) => button.textContent.trim() === '创建');
    await act(async () => {
      Simulate.click(createButton);
      await flushPromises();
    });

    expect(api.createGroup).toHaveBeenCalledWith('Agent Review Group', [43]);
    expect(onCreated).toHaveBeenCalledWith({ group_id: 88, topic: 'grp_88' });
    expect(onClose).toHaveBeenCalledTimes(1);
  });

  it('keeps the current Agent locked and requires a new member when upgrading a task', async () => {
    const onClose = vi.fn();
    const onCreated = vi.fn();
    const onCreate = vi.fn().mockResolvedValue({ group_id: 89, topic: 'grp_89' });
    await mount({
      mode: 'task_upgrade',
      initialName: 'Review Task',
      lockedMemberIds: [42],
      onClose,
      onCreated,
      onCreate,
    });

    expect(container.querySelector('.oc-collaboration-input').value).toBe('Review Task');
    const upgradeButton = Array.from(container.querySelectorAll('.oc-collaboration-modal-footer button'))
      .find((button) => button.textContent.trim() === '升级并添加');
    expect(upgradeButton.disabled).toBe(true);

    const friendRow = Array.from(container.querySelectorAll('.oc-member-picker-item'))
      .find((row) => row.textContent.includes('Alice'));
    await act(async () => Simulate.change(friendRow.querySelector('input[type="checkbox"]')));
    expect(upgradeButton.disabled).toBe(false);

    const agentTab = Array.from(container.querySelectorAll('[role="tablist"] button'))
      .find((button) => button.textContent.trim() === 'Agent');
    await act(async () => Simulate.click(agentTab));
    const currentAgentRow = Array.from(container.querySelectorAll('.oc-member-picker-item'))
      .find((row) => row.textContent.includes('Virtual Catsco'));
    expect(currentAgentRow.querySelector('input[type="checkbox"]').checked).toBe(true);
    expect(currentAgentRow.querySelector('input[type="checkbox"]').disabled).toBe(true);
    expect(currentAgentRow.textContent).toContain('当前任务成员');

    await act(async () => {
      Simulate.click(upgradeButton);
      await flushPromises();
    });

    expect(onCreate).toHaveBeenCalledWith('Review Task', [42, 8]);
    expect(onCreated).toHaveBeenCalledWith({ group_id: 89, topic: 'grp_89' });
    expect(onClose).toHaveBeenCalledTimes(1);
  });
});
