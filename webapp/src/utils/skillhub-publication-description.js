const exact = (value, keys) => value && typeof value === 'object' && !Array.isArray(value)
  && Object.keys(value).length === keys.length && keys.every(key => Object.hasOwn(value, key));

// Keep the complete exact text, not HTML/Markdown, a file path or model input.
export function normalizePublicationDescription(response, entryRequestId) {
  const bad = () => { throw new Error('发布草稿格式不匹配。'); };
  const d = response?.draft;
  const l = d?.location;
  if (!/^pubentry_[a-f0-9]{32}$/.test(entryRequestId || '')
    || !exact(response, ['schemaVersion', 'entryRequestId', 'botUid', 'topicId', 'taskId', 'automaticRepairEnabled', 'draft'])
    || response.schemaVersion !== 1 || response.entryRequestId !== entryRequestId
    || typeof response.botUid !== 'string' || !/^[1-9][0-9]*$/.test(response.botUid)
    || typeof response.topicId !== 'string' || !/^grp_[1-9][0-9]*$/.test(response.topicId)
    || typeof response.taskId !== 'string' || !/^pubrepair_[a-f0-9]{32}$/.test(response.taskId) || response.automaticRepairEnabled !== false
    || !exact(d, ['confirmationKey', 'round', 'location', 'text', 'state'])
    || typeof d.confirmationKey !== 'string' || !/^[a-f0-9]{64}$/.test(d.confirmationKey)
    || !Number.isInteger(d.round) || d.round < 1 || d.round > 3
    || !['awaiting_confirmation', 'confirmed'].includes(d.state)
    || typeof d.text !== 'string' || !d.text.trim() || new TextEncoder().encode(d.text).length > 8192
    || /[\u0000-\u0008\u000b\u000c\u000e-\u001f\u007f]/.test(d.text)
    || Array.from(d.text).some(c => c.length === 1 && /[\ud800-\udfff]/.test(c))) return bad();
  if (l?.field !== 'description' || !(l.relativePath === 'SKILL.md'
    ? exact(l, ['relativePath', 'field', 'entryIndex']) && Number.isSafeInteger(l.entryIndex) && l.entryIndex >= 0
    : l.relativePath === 'skill.json' && exact(l, ['relativePath', 'field']))) return bad();
  return { ...response, draft: { ...d, location: { ...l } } };
}

export function samePublicationDescription(left, right) {
  return left.entryRequestId === right.entryRequestId && left.botUid === right.botUid
    && left.topicId === right.topicId && left.taskId === right.taskId
    && left.draft.confirmationKey === right.draft.confirmationKey && left.draft.round === right.draft.round
    && left.draft.text === right.draft.text && left.draft.location.relativePath === right.draft.location.relativePath
    && left.draft.location.field === right.draft.location.field
    && left.draft.location.entryIndex === right.draft.location.entryIndex;
}
