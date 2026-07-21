import { api } from '../api';

export async function createAgentTaskTopicRecord({
  agent,
  taskName,
  projectId = 0,
  projectName = '',
}) {
  const agentUid = agent?.uid || agent?.id;
  if (!agentUid) throw new Error('请选择一个可用的 Agent');

  const created = await api.createGroup(taskName, [agentUid], { kind: 'agent_task' });
  const rawGroup = created?.group || {};
  const groupId = rawGroup.id || created?.group_id;
  const topicId = created?.topic || created?.topic_id || (groupId ? `grp_${groupId}` : '');
  if (!topicId || !groupId) throw new Error('暂时无法创建任务，请稍后重试。');

  const normalizedProjectId = Number(projectId || 0);
  if (normalizedProjectId > 0) {
    try {
      await api.assignProjectTopic(normalizedProjectId, topicId);
    } catch (error) {
      try {
        await api.disbandGroup(groupId);
      } catch (rollbackError) {
        console.warn('Failed to roll back the unassigned project task:', rollbackError);
      }
      throw error;
    }
  }

  return {
    topicId,
    name: rawGroup.name || created?.name || taskName,
    isGroup: true,
    groupId,
    avatar_url: rawGroup.avatar_url || created?.avatar_url || agent.avatar_url || '',
    hasBot: true,
    isAgentTask: true,
    memberCount: Number(rawGroup.member_count || created?.member_count || 2),
    projectId: normalizedProjectId,
    projectName: String(projectName || ''),
  };
}
