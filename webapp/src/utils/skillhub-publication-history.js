// Display-only local terminal projection. Never infer execution state or
// publication authority from a missing receipt or browser recovery hint.
const failureCodes = new Set(['unavailable', 'copy_changed', 'rejected', 'publisher_changed',
  'conflict', 'artifact_changed', 'invalid', 'lease_lost']
  .map(code => `skillhub.publication_description_release_commit_${code}`));
const exact = (value, keys) => value && typeof value === 'object' && !Array.isArray(value)
  && Object.keys(value).length === keys.length && keys.every(key => Object.hasOwn(value, key));
const date = value => typeof value === 'string' && value.length <= 64
  && /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d{1,9})?Z$/.test(value)
  && Number.isFinite(Date.parse(value));

export function normalizePublicationHistory(response, entryRequestId) {
  const v = response?.descriptionReleaseHistory;
  const bad = () => { throw new Error('发布结果格式不匹配。'); };
  if (!/^pubentry_[a-f0-9]{32}$/.test(entryRequestId || '')
    || !exact(response, ['schemaVersion', 'entryRequestId', 'descriptionReleaseHistory'])
    || response.schemaVersion !== 1 || response.entryRequestId !== entryRequestId) return bad();
  if (v?.stage === 'not_recorded' && exact(v, ['stage'])) return { stage: 'not_recorded' };
  if (!date(v?.recordedAt)) return bad();
  if (v.stage === 'publish_failed' && exact(v, ['stage', 'recordedAt', 'code']) && failureCodes.has(v.code)) {
    return { stage: v.stage, recordedAt: v.recordedAt, code: v.code };
  }
  const r = v?.result;
  if (v.stage !== 'published' || !exact(v, ['stage', 'recordedAt', 'result'])
    || !exact(r, ['skillId', 'version', 'publishedAt'])
    || typeof r.skillId !== 'string' || !r.skillId.trim() || r.skillId.length > 256
    || typeof r.version !== 'string' || !r.version.trim() || r.version.length > 128
    || !date(r.publishedAt)) return bad();
  return { stage: v.stage, recordedAt: v.recordedAt,
    result: { skillId: r.skillId, version: r.version, publishedAt: r.publishedAt } };
}
