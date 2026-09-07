export function normalizeSkillHubSkills(response) {
  const values = Array.isArray(response)
    ? response
    : (response?.skills || response?.items || response?.results || []);
  return values.map((skill) => {
    const author = skill?.author;
    const publisherDisplayName = String(
      (author && typeof author === 'object' ? author.displayName || author.display_name : '')
      || '',
    ).trim();
    const publisherUid = String(
      (author && typeof author === 'object' ? author.catsCoUid || author.catscoUid || author.cats_co_uid : '')
      || '',
    ).trim();
    return {
      ...skill,
      skillId: String(skill?.skillId || skill?.skill_id || skill?.id || '').trim(),
      displayName: String(
        skill?.displayName
        || skill?.display_name
        || skill?.name
        || skill?.skillId
        || skill?.id
        || '',
      ).trim(),
      description: String(skill?.description || '').trim(),
      author: String(
        publisherDisplayName
        || (author && typeof author === 'object' ? author.name : author)
        || skill?.publisher
        || '',
      ).trim(),
      publisherDisplayName,
      publisherUid,
      latestVersion: String(
        skill?.latestVersion || skill?.latest_version || skill?.version || '',
      ).trim(),
      publishedAt: String(skill?.publishedAt || skill?.published_at || '').trim(),
      contentHash: String(
        skill?.contentHash || skill?.content_hash || skill?.sha256 || '',
      ).trim().toLowerCase(),
    };
  }).filter((skill) => skill.skillId);
}

export function formatSkillHubPublisher(skill, fallback = '发布者待确认') {
  const displayName = String(skill?.publisherDisplayName || skill?.author || '').trim();
  const uid = String(skill?.publisherUid || '').trim();
  if (displayName && uid) return `${displayName} · UID ${uid}`;
  if (displayName) return displayName;
  if (uid) return `UID ${uid}`;
  return fallback;
}

export function normalizeLocalSkillHubSkills(response) {
  const candidate = Array.isArray(response)
    ? response
    : (response?.skills || response?.items || response?.installed || response?.packages || response?.store || []);
  const values = Array.isArray(candidate) ? candidate : [];
  return values.map((skill) => {
    const skillHub = skill?.skillHub || skill?.skill_hub || {};
    const reference = skillHub?.reference || skill?.reference || {};
    const localSkillId = String(
      skill?.localSkillId || skill?.local_skill_id || skill?.folder || skill?.name || '',
    ).trim();
    const displayName = String(
      skill?.displayName || skill?.display_name || skill?.name || skill?.folder || localSkillId,
    ).trim();
    const cloudSkillId = String(
      reference?.skillId
      || reference?.skill_id
      || skill?.cloudSkillId
      || skill?.skillId
      || skill?.skill_id
      || skillHub?.skillId
      || skillHub?.skill_id
      || '',
    ).trim();
    const source = String(skill?.source || 'local').trim().toLowerCase();
    return {
      ...skill,
      skillId: cloudSkillId || `local:${localSkillId || displayName}`,
      cloudSkillId,
      localSkillId,
      displayName,
      description: String(skill?.description || '').trim(),
      author: String(
        skillHub?.author
        || skill?.author
        || (source === 'user' ? '我的 Skill' : '本地 Skill'),
      ).trim(),
      latestVersion: String(
        reference?.version || skillHub?.version || skill?.version || '',
      ).trim(),
      contentHash: String(
        reference?.contentHash
        || reference?.content_hash
        || skillHub?.contentHash
        || skillHub?.content_hash
        || skill?.contentHash
        || skill?.content_hash
        || '',
      ).trim().toLowerCase(),
      isLocalSkill: true,
      canBind: Boolean(cloudSkillId),
      canShare: skill?.canShare ?? skill?.can_share ?? source !== 'system',
      localSource: source,
    };
  }).filter((skill) => skill.displayName && skill.skillId !== 'local:');
}

export function resolveSkillHubEntry(skill, detail) {
  const nested = detail?.skill || detail?.version || detail || {};
  const base = normalizeSkillHubSkills([{
    ...skill,
    ...nested,
    skillId: nested?.skillId || nested?.skill_id || nested?.id || skill?.skillId,
    latestVersion: nested?.latestVersion
      || nested?.latest_version
      || nested?.version
      || detail?.latestVersion
      || detail?.latest_version
      || skill?.latestVersion,
    contentHash: nested?.contentHash
      || nested?.content_hash
      || nested?.sha256
      || detail?.contentHash
      || detail?.content_hash
      || skill?.contentHash,
  }])[0] || skill;
  if (base?.latestVersion && /^[0-9a-f]{64}$/.test(String(base?.contentHash || ''))) return base;
  const versions = normalizeSkillHubSkills(detail?.versions || []);
  const versionEntry = versions.find((entry) => (
    base?.latestVersion && entry.latestVersion === base.latestVersion
  )) || versions.find((entry) => entry.isLatest === true || entry.is_latest === true)
    || (versions.length === 1 ? versions[0] : null);
  return versionEntry ? { ...base, ...versionEntry, skillId: base.skillId || skill.skillId } : base;
}
