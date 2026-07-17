import React, { act } from 'react';
import { createRoot } from 'react-dom/client';
import { Simulate } from 'react-dom/test-utils';

vi.mock('../api', () => ({
  api: {
    getGroupInfo: vi.fn(),
    getFriends: vi.fn(),
    inviteToGroup: vi.fn(),
    resolveGroupInviteRequest: vi.fn(),
    updateGroup: vi.fn(),
    setGroupAnnouncement: vi.fn(),
  },
}));

import { api } from '../api';
import GroupSettings from './group-settings';

const group = {
  id: 1,
  name: 'Project Cats',
  avatar_url: '',
  announcement: '',
};

const invitee = {
  id: 9,
  username: 'new-member',
  display_name: 'New Member',
  avatar_url: '',
  account_type: 'user',
};

async function flushPromises() {
  await Promise.resolve();
  await Promise.resolve();
}

describe('GroupSettings invitations', () => {
  let container;
  let root;

  beforeEach(() => {
    global.IS_REACT_ACT_ENVIRONMENT = true;
    Object.values(api).forEach((mock) => mock.mockReset());
    api.getFriends.mockResolvedValue({ friends: [invitee] });
    api.updateGroup.mockResolvedValue({});
    api.setGroupAnnouncement.mockResolvedValue({});
    container = document.createElement('div');
    document.body.appendChild(container);
    root = createRoot(container);
  });

  afterEach(async () => {
    await act(async () => {
      root.unmount();
    });
    container.remove();
  });

  test('regular members submit an invitation for approval', async () => {
    const memberInfo = {
      group,
      members: [
        { user_id: 7, username: 'member', display_name: 'Member', role: 'member' },
      ],
    };
    api.getGroupInfo.mockResolvedValue(memberInfo);
    api.inviteToGroup.mockResolvedValue({ added: 0, requested: 1 });

    await act(async () => {
      root.render(<GroupSettings groupId={1} currentUser={{ uid: 7 }} onClose={vi.fn()} />);
      await flushPromises();
    });

    await act(async () => {
      Simulate.click(container.querySelector('.oc-invite-members-button'));
    });
    await act(async () => {
      Simulate.click(container.querySelector('.oc-settings-list-button'));
    });

    const submit = container.querySelector('.oc-settings-actions .oc-btn-primary');
    expect(submit).not.toBeNull();

    await act(async () => {
      Simulate.click(submit);
      await flushPromises();
    });

    expect(api.inviteToGroup).toHaveBeenCalledWith(1, [9]);
    expect(container.querySelector('.oc-form-notice')).not.toBeNull();
  });

  test('admins can approve a pending invitation', async () => {
    const pendingRequest = {
      id: 12,
      group_id: 1,
      inviter_id: 8,
      inviter_username: 'member',
      inviter_display_name: 'Member',
      invitee_id: 9,
      invitee_username: 'new-member',
      invitee_display_name: 'New Member',
      invitee_avatar_url: '',
      invitee_is_bot: false,
      status: 'pending',
    };
    api.getGroupInfo
      .mockResolvedValueOnce({
        group,
        members: [
          { user_id: 7, username: 'admin', display_name: 'Admin', role: 'admin' },
        ],
        invite_requests: [pendingRequest],
      })
      .mockResolvedValue({
        group,
        members: [
          { user_id: 7, username: 'admin', display_name: 'Admin', role: 'admin' },
          { user_id: 9, username: 'new-member', display_name: 'New Member', role: 'member' },
        ],
        invite_requests: [],
      });
    api.resolveGroupInviteRequest.mockResolvedValue({
      request: { ...pendingRequest, status: 'approved' },
    });

    await act(async () => {
      root.render(<GroupSettings groupId={1} currentUser={{ uid: 7 }} onClose={vi.fn()} />);
      await flushPromises();
    });

    const approve = container.querySelector('.oc-invite-requests-section .oc-btn-primary');
    expect(approve).not.toBeNull();

    await act(async () => {
      Simulate.click(approve);
      await flushPromises();
    });

    expect(api.resolveGroupInviteRequest).toHaveBeenCalledWith(1, 12, 'approve');
    expect(container.querySelector('.oc-invite-requests-section')).toBeNull();
  });
});
