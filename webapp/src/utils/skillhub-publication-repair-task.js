const exact = (value, keys) => value && typeof value === 'object' && !Array.isArray(value)
  && Object.keys(value).length === keys.length && keys.every(key => Object.hasOwn(value, key));
const positiveID = value => typeof value === 'string' && /^[1-9][0-9]{0,18}$/.test(value)
  && BigInt(value) <= 9223372036854775807n;
export const validRepairTopic = value => typeof value === 'string' && value.startsWith('grp_') && positiveID(value.slice(4));
export const validRepairScope = (entryRequestId, botUid, topicId) => typeof entryRequestId === 'string'
  && /^pubentry_[a-f0-9]{32}$/.test(entryRequestId) && positiveID(botUid) && validRepairTopic(topicId);

// Safe retained-ledger projection, not evidence of a live executor or permission
// to repair, reset rounds, change conversation, call a model or publish.
export function normalizePublicationRepairTask(response, entryRequestId, botUid) {
  const bad = () => { throw new Error('发布修复任务格式不匹配。'); };
  if (!exact(response, ['schemaVersion', 'entryRequestId', 'botUid', 'topicId', 'taskId', 'state',
    'roundsUsed', 'maxFormatRepairRounds', 'automaticRepairEnabled'])
    || !validRepairScope(entryRequestId, botUid, response.topicId)
    || response.schemaVersion !== 1 || response.entryRequestId !== entryRequestId || response.botUid !== botUid
    || typeof response.taskId !== 'string' || !/^pubrepair_[a-f0-9]{32}$/.test(response.taskId)
    || response.automaticRepairEnabled !== false || response.maxFormatRepairRounds !== 3
    || !Number.isInteger(response.roundsUsed) || response.roundsUsed < 0 || response.roundsUsed > 3) return bad();
  const rounds = response.roundsUsed;
  const coherent = response.state === 'queued' ? rounds < 3
    : response.state === 'stopped_budget' ? rounds === 3
      : ['running', 'waiting_confirmation', 'validated', 'stopped', 'stopped_no_progress'].includes(response.state) && rounds > 0;
  if (!coherent) return bad();
  return { ...response };
}

export const repairTaskStateLabel = state => ({
  queued: '待处理（尚未启动自动修复）',
  running: '账本记录为处理中，不代表执行器当前仍在运行',
  waiting_confirmation: '已停在需要确认的步骤',
  validated: '校验已通过，不代表已发布',
  stopped: '已停止，需要人工检查',
  stopped_no_progress: '因没有进展而停止',
  stopped_budget: '已用完三轮修复额度',
})[state];
