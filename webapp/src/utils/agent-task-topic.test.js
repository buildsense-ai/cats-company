vi.mock('../api', () => ({
  api: {
    createGroup: vi.fn(),
    assignProjectTopic: vi.fn(),
    disbandGroup: vi.fn(),
  },
}));

import { api } from '../api';
import { createAgentTaskTopicRecord } from './agent-task-topic';

describe('createAgentTaskTopicRecord', () => {
  const agent = { uid: 42, display_name: 'Quality Agent', avatar_url: '/agent.png' };

  beforeEach(() => {
    api.createGroup.mockResolvedValue({
      group_id: 77,
      topic: 'grp_77',
      name: 'Review release',
      member_count: 2,
    });
    api.assignProjectTopic.mockResolvedValue({ ok: true });
    api.disbandGroup.mockResolvedValue({ ok: true });
  });

  afterEach(() => {
    vi.clearAllMocks();
  });

  it('creates and assigns a task when a target project is provided', async () => {
    const topic = await createAgentTaskTopicRecord({
      agent,
      taskName: 'Review release',
      projectId: 12,
      projectName: 'Website',
    });

    expect(api.createGroup).toHaveBeenCalledWith('Review release', [42], { kind: 'agent_task' });
    expect(api.assignProjectTopic).toHaveBeenCalledWith(12, 'grp_77');
    expect(api.createGroup.mock.invocationCallOrder[0])
      .toBeLessThan(api.assignProjectTopic.mock.invocationCallOrder[0]);
    expect(topic).toEqual(expect.objectContaining({
      topicId: 'grp_77',
      groupId: 77,
      projectId: 12,
      projectName: 'Website',
    }));
  });

  it('does not assign an ordinary new task to a project', async () => {
    const topic = await createAgentTaskTopicRecord({ agent, taskName: 'Review release' });

    expect(api.assignProjectTopic).not.toHaveBeenCalled();
    expect(topic.projectId).toBe(0);
  });

  it('disbands the new task and rethrows when project assignment fails', async () => {
    const assignmentError = new Error('assignment unavailable');
    api.assignProjectTopic.mockRejectedValueOnce(assignmentError);

    await expect(createAgentTaskTopicRecord({
      agent,
      taskName: 'Review release',
      projectId: 12,
    })).rejects.toBe(assignmentError);
    expect(api.disbandGroup).toHaveBeenCalledWith(77);
  });

  it('preserves the assignment error when rollback also fails', async () => {
    const assignmentError = new Error('assignment unavailable');
    api.assignProjectTopic.mockRejectedValueOnce(assignmentError);
    api.disbandGroup.mockRejectedValueOnce(new Error('rollback unavailable'));
    const warn = vi.spyOn(console, 'warn').mockImplementation(() => {});

    await expect(createAgentTaskTopicRecord({
      agent,
      taskName: 'Review release',
      projectId: 12,
    })).rejects.toBe(assignmentError);
    expect(warn).toHaveBeenCalled();
    warn.mockRestore();
  });
});
