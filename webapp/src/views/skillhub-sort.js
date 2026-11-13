import { readStorageValue, writeStorageValue } from '../utils/storage-access';
import { localizedSkillText } from './skillhub-categories';

const orders = new Set(['time-desc', 'time-asc', 'name-asc', 'name-desc']);
const names = new Intl.Collator('zh-CN-u-co-pinyin', { numeric: true, sensitivity: 'base' });

export function skillSortOptions(section) {
  return [
    ['time-desc', section === 'added' ? '最近添加' : '最近发布'],
    ['time-asc', section === 'added' ? '最早添加' : '最早发布'],
    ['name-asc', '名称 A → Z'],
    ['name-desc', '名称 Z → A'],
  ];
}

function preferenceKey(owner, section) {
  return `catsco.skillhub.sort.${owner}.${section}`;
}

export function readSkillSort(owner, section) {
  const value = readStorageValue(preferenceKey(owner, section));
  return orders.has(value) ? value : 'time-desc';
}

export function writeSkillSort(owner, section, value) {
  if (orders.has(value)) writeStorageValue(preferenceKey(owner, section), value);
}

export function skillAddedTimestamp(skill) {
  const value = skill.addedAt || skill.added_at || skill.installedAt || skill.installed_at;
  const timestamp = typeof value === 'number' ? value : Date.parse(String(value || ''));
  return Number.isFinite(timestamp) ? timestamp : 0;
}

export function sortSkills(skills, { order = 'time-desc', language = 'source', timeOf, presentationOf = skill => skill, isPriority = () => false }) {
  const byName = (left, right) => names.compare(left.name, right.name);
  const byID = (left, right) => {
    const a = String(left.skill.skillId || '');
    const b = String(right.skill.skillId || '');
    return a < b ? -1 : a > b ? 1 : 0;
  };
  return skills.map(skill => ({
    skill,
    name: localizedSkillText(presentationOf(skill), language).name,
    time: timeOf(skill),
    priority: Number(isPriority(skill)),
  })).sort((left, right) => {
    if (left.priority !== right.priority) return right.priority - left.priority;
    if (order === 'name-asc' || order === 'name-desc') {
      return byName(left, right) * (order === 'name-desc' ? -1 : 1) || byID(left, right);
    }
    // Unknown dates belong at the end in both directions, never at the start
    // of "oldest first". Equal or absent dates have a stable name/ID order.
    const leftKnown = Number.isFinite(left.time) && left.time > 0;
    const rightKnown = Number.isFinite(right.time) && right.time > 0;
    if (leftKnown !== rightKnown) return leftKnown ? -1 : 1;
    return (leftKnown ? (left.time - right.time) * (order === 'time-asc' ? 1 : -1) : 0)
      || byName(left, right) || byID(left, right);
  }).map(({ skill }) => skill);
}
