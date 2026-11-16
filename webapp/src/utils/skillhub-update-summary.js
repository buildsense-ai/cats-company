import {
  isPrivateSkillHubReference,
  isSkillHubUpdateAvailable,
  resolveSkillHubEntry,
} from './skillhub-entry';
import { normalizeOwnedBots } from './owned-bots';
import { readStorageValue, writeStorageValue } from './storage-access';

export const SKILLHUB_UPDATE_REFRESH_INTERVAL_MS = 3 * 60 * 1000;

export const skillHubSeenUpdatesKey = (userUid) => userUid ? `catsco_skillhub_seen_updates:v1:${userUid}` : '';

export function readSeenSkillHubUpdates(userUid) {
  if (!userUid) return [];
  try {
    const stored = JSON.parse(readStorageValue(skillHubSeenUpdatesKey(userUid)) || '[]');
    return Array.isArray(stored) ? stored.filter((key) => typeof key === 'string') : [];
  } catch {
    return [];
  }
}

export function writeSeenSkillHubUpdates(userUid, keys) {
  if (!userUid) return;
  writeStorageValue(skillHubSeenUpdatesKey(userUid), JSON.stringify(keys));
}

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
 * hide updates for the other Bots. A Bot whose definition cannot be read is
 * unavailable. If only some of its public SkillHub references cannot be
 * resolved (for example, a Skill was removed from the catalogue), the Bot is
 * partial: the resolvable references still contribute to the update count,
 * while the unresolved IDs are reported to the UI.
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
  const updateKeys = [];
  for (const entry of definitions) {
    if (!entry.ok) {
      byBot[entry.botUID] = { count: null, status: 'unavailable' };
      continue;
    }
    const unavailableSkillIds = [...new Set(entry.skills
      .filter((skill) => !details.get(skill.skillId))
      .map((skill) => skill.skillId))];
    const count = entry.skills.reduce((sum, skill) => {
      const latest = details.get(skill.skillId);
      if (!latest || !isSkillHubUpdateAvailable(skill, latest)) return sum;
      updateKeys.push(JSON.stringify([
        entry.botUID, skill.skillId, latest.latestVersion, String(latest.contentHash || '').toLowerCase(),
      ]));
      return sum + 1;
    }, 0);
    byBot[entry.botUID] = unavailableSkillIds.length > 0
      ? { count, status: 'partial', unavailableSkillIds }
      : { count, status: 'ready' };
    total += count;
  }

  return {
    userUid: String(userUid || ''),
    updateKeys,
    total,
    byBot,
    detailsBySkillID,
  };
}

export function createEmptySkillHubUpdateSummary() {
  return { total: 0, byBot: {}, detailsBySkillID: {}, userUid: '', updateKeys: [] };
}
