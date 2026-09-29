import {
  isPrivateSkillHubReference,
  isSkillHubUpdateAvailable,
  resolveSkillHubEntry,
} from './skillhub-entry';
import { normalizeOwnedBots } from './owned-bots';

export const SKILLHUB_UPDATE_REFRESH_INTERVAL_MS = 3 * 60 * 1000;

function botUID(bot) {
  return String(bot?.uid ?? bot?.id ?? '').trim();
}

function isPublicSkillHubReference(skill) {
  const skillId = String(skill?.skillId || skill?.skill_id || '').trim();
  const source = String(skill?.source || 'skillhub').trim().toLowerCase();
  return Boolean(
    skillId
    && (source === '' || source === 'skillhub')
    && !isPrivateSkillHubReference(skillId),
  );
}

function normalizeDefinitionSkills(response) {
  const skills = Array.isArray(response?.skills) ? response.skills : [];
  return skills.map((skill) => ({
    ...skill,
    skillId: String(skill?.skillId || skill?.skill_id || '').trim(),
    version: String(skill?.version || '').trim(),
    contentHash: String(skill?.contentHash || skill?.content_hash || '').trim().toLowerCase(),
  })).filter(isPublicSkillHubReference);
}

/**
 * Fetch update counts for owner Bots without allowing one unavailable Bot to
 * hide updates for the other Bots. A Bot is included in the aggregate only
 * when its definition and every public SkillHub reference were checked
 * successfully; an unavailable/partial Bot is intentionally omitted.
 */
export async function collectSkillHubUpdateSummary({
  bots = [],
  userUid,
  getDefinition,
  getSkill,
  catalogueByID = new Map(),
}) {
  if (typeof getDefinition !== 'function' || typeof getSkill !== 'function') {
    throw new TypeError('SkillHub update summary requires definition and SkillHub readers.');
  }

  const ownerBots = (Array.isArray(bots) ? bots : [])
    .filter((bot) => normalizeOwnedBots([bot], userUid).length > 0)
    .map((bot) => ({ bot, botUID: botUID(bot) }))
    .filter(({ botUID: uid }) => uid);

  const byBot = {};
  const definitions = await Promise.all(ownerBots.map(async ({ bot, botUID: uid }) => {
    try {
      const response = await getDefinition(uid);
      return { bot, botUID: uid, skills: normalizeDefinitionSkills(response), ok: true };
    } catch {
      return { bot, botUID: uid, skills: [], ok: false };
    }
  }));

  const skillIDs = [...new Set(definitions
    .filter((entry) => entry.ok)
    .flatMap((entry) => entry.skills.map((skill) => skill.skillId)))];
  const details = new Map();
  await Promise.all(skillIDs.map(async (skillId) => {
    const catalogueEntry = catalogueByID instanceof Map ? catalogueByID.get(skillId) : null;
    const catalogueSkill = catalogueEntry?.skill || catalogueEntry;
    const fetchedAt = Number(catalogueEntry?.fetchedAt || 0);
    const catalogueIsFresh = !fetchedAt
      || (Date.now() - fetchedAt) <= SKILLHUB_UPDATE_REFRESH_INTERVAL_MS;
    if (
      catalogueIsFresh
      && String(catalogueSkill?.skillId || '').trim() === skillId
      && catalogueSkill?.isLocalSkill !== true
      && (!String(catalogueSkill?.source || '').trim()
        || String(catalogueSkill?.source || '').trim().toLowerCase() === 'skillhub')
      && catalogueSkill?.latestVersion
    ) {
      details.set(skillId, catalogueSkill);
      return;
    }
    try {
      const response = await getSkill(skillId);
      const resolved = resolveSkillHubEntry({ skillId }, response);
      if (String(resolved?.skillId || '').trim() !== skillId || !resolved?.latestVersion) {
        throw new Error('SkillHub returned an incomplete or mismatched Skill.');
      }
      details.set(skillId, resolved);
    } catch {
      details.set(skillId, null);
    }
  }));

  const detailsBySkillID = {};
  for (const [skillId, detail] of details) {
    if (detail) detailsBySkillID[skillId] = detail;
  }

  let total = 0;
  for (const entry of definitions) {
    if (!entry.ok || entry.skills.some((skill) => !details.get(skill.skillId))) {
      byBot[entry.botUID] = { count: null, status: 'unavailable' };
      continue;
    }
    const count = entry.skills.reduce((sum, skill) => (
      sum + (isSkillHubUpdateAvailable(skill, details.get(skill.skillId)) ? 1 : 0)
    ), 0);
    byBot[entry.botUID] = { count, status: 'ready' };
    total += count;
  }

  return {
    total,
    byBot,
    detailsBySkillID,
  };
}

export function createEmptySkillHubUpdateSummary() {
  return { total: 0, byBot: {}, detailsBySkillID: {} };
}
