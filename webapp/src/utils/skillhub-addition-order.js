import { getStorage } from './storage-access';

function storageKey(userUID, botUID) {
  return `catsco.skillhub.added-at.${userUID}.${botUID}`;
}

export function readSkillAdditionTimes(userUID, botUID) {
  if (!userUID || !botUID) return {};
  try {
    const value = JSON.parse(getStorage()?.getItem(storageKey(userUID, botUID)) || '{}');
    if (!value || typeof value !== 'object' || Array.isArray(value)) return {};
    return Object.fromEntries(Object.entries(value).filter(([, date]) => (
      typeof date === 'string' && Number.isFinite(Date.parse(date))
    )));
  } catch { return {}; }
}

export function recordSkillAdditions(userUID, botUID, previousSkills, nextSkills) {
  if (!userUID || !botUID) return;
  const previousIDs = new Set(previousSkills.map(skill => skill.skillId));
  const saved = readSkillAdditionTimes(userUID, botUID);
  const now = new Date().toISOString();
  const next = Object.fromEntries(nextSkills.flatMap(skill => {
    const addedAt = previousIDs.has(skill.skillId) ? saved[skill.skillId] : now;
    return addedAt ? [[skill.skillId, addedAt]] : [];
  }));
  try { getStorage()?.setItem(storageKey(userUID, botUID), JSON.stringify(next)); } catch {}
}
