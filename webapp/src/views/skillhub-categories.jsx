import React from 'react';
import { cataloguePresentation } from './skillhub-presentation';

export const SKILL_CATEGORIES = [
  ['development', '开发工具'], ['design', '设计创作'],
  ['writing', '内容写作'], ['office', '办公协作'],
  ['data', '数据分析'], ['business', '商业运营'],
  ['finance', '金融研究'], ['search', '搜索资讯'],
  ['education', '教育学习'], ['life', '生活服务'],
  ['other', '其他'],
];

const aliases = { web: 'development', media: 'design', documents: 'writing', automation: 'office', research: 'search' };
const categoryIDs = new Set(SKILL_CATEGORIES.map(([id]) => id));
export function skillCategory(skill) {
  const value = skill?.primaryCategory || skill?.primary_category || skill?.category || skill?.skillHub?.primaryCategory || '';
  const id = aliases[value] || value;
  return categoryIDs.has(id) ? id : '';
}

const categoryKeywords = [
  ['finance', /\b(finance|financial|stock|investment|accounting)\b|金融|股票|投资|财务/i],
  ['education', /\b(learn|learning|education|teaching|course|study)\b|学习|教育|教学|课程|教师/i],
  ['office', /\b(office|meeting|calendar|email|mail|task|workflow|document)\b|办公|会议|日程|邮件|协作|文档/i],
  ['data', /\b(data|analytics|analysis|chart|sql|database|spreadsheet|excel)\b|数据|分析|图表|数据库|报表|表格/i],
  ['development', /\b(code|coding|developer|development|api|cli|debug|git|github|program|html|webapp|artifact)\b|编程|开发|代码|接口|脚本|终端|网页应用/i],
  ['design', /\b(design|image|photo|video|audio|creative|visual|ui|ux)\b|图片|图像|设计|影音|视觉/i],
  ['writing', /\b(write|writing|writer|copy|article|blog|translate|humanizer|manuscript)\b|文案|写作|文章|翻译|稿件/i],
  ['business', /\b(business|sales|marketing|customer|crm)\b|运营|销售|营销|客户|商业/i],
  ['search', /\b(search|research|browse|news)\b|检索|搜索|研究|资讯|新闻/i],
  ['life', /\b(life|travel|food|health|home)\b|生活|旅行|美食|健康|家居/i],
];

export function inferSkillCategory(skill) {
  const explicit = skillCategory(skill);
  if (explicit) return explicit;
  const reviewed = cataloguePresentation(skill);
  if (reviewed) return reviewed.category;
  const text = [skill?.displayName, skill?.name, String(skill?.skillId || '').split('/').pop()].filter(Boolean).join(' ').replace(/[-_/]/g, ' ');
  if (/\b(agent browser|browser automation|playwright|puppeteer|selenium)\b|浏览器自动化/i.test(text)
    || /\bbrowser automation\b|浏览器自动化/i.test(skill?.description || '')) return 'development';
  const tagText = Array.isArray(skill?.tags) ? skill.tags.join(' ') : '';
  const match = categoryKeywords.find(([, pattern]) => pattern.test(text))
    || categoryKeywords.find(([, pattern]) => pattern.test(tagText))
    || categoryKeywords.find(([, pattern]) => pattern.test(skill?.description || ''));
  return match?.[0] || (Array.isArray(skill?.categories)
    ? skill.categories.map(id => aliases[id] || id).find(id => categoryIDs.has(id)) : '') || 'other';
}

export function skillUploadedTimestamp(skill) {
  const value = skill?.uploadedAt || skill?.uploaded_at || skill?.publishedAt || skill?.published_at
    || skill?.createdAt || skill?.created_at;
  const timestamp = Date.parse(String(value || ''));
  return Number.isFinite(timestamp) ? timestamp : 0;
}

export function localizedSkillText(skill, language = 'source') {
  const translations = skill?.translations || skill?.i18n || skill?.localized || {};
  const zh = translations?.['zh-CN'] || translations?.zh || translations?.['zh-cn'] || {};
  if (language === 'zh-CN') {
    const reviewed = cataloguePresentation(skill);
    const name = zh.name || zh.displayName || skill?.localizedName || skill?.localized_name || reviewed?.name;
    const description = zh.description || skill?.localizedDescription || skill?.localized_description || reviewed?.description;
    return {
      name: name || skill?.displayName || skill?.name || skill?.skillId || '',
      description: description || skill?.description || '',
      translated: Boolean(name || description),
    };
  }
  return { name: skill?.displayName || skill?.name || skill?.skillId || '', description: skill?.description || '', translated: false };
}

export function categoryKey(skill) {
  return String(skill?.cloudSkillId || skill?.skillId || skill?.localSkillId || skill?.name || '');
}

export function readCategoryDrafts(uid) {
  try {
    const value = JSON.parse(localStorage.getItem(`catsco.skillhub.categories.${uid || 'guest'}`) || '{}');
    return value && typeof value === 'object' && !Array.isArray(value)
      ? Object.fromEntries(Object.entries(value).filter(([, id]) => id === '' || categoryIDs.has(id))) : {};
  } catch { return {}; }
}

export function SkillCategoryFilters({ value, onChange, showOther }) {
  const categories = [['', '全部'], ...SKILL_CATEGORIES.filter(([id]) => id !== 'other' || showOther)];
  return <div className='cc-skillhub-category-filter' role='group' aria-label='按能力分类筛选'>
    {categories.map(([id, label]) => <button key={id} type='button' aria-pressed={value === id} onClick={() => onChange(id)}>{label}</button>)}
  </div>;
}

export function SkillCategoryField({ skill, value, onChange }) {
  const label = skill.displayName || skill.name || skill.skillId;
  return <label className='cc-skillhub-category-field'>
    <span>能力分类</span>
    <select aria-label={`${label}的能力分类`} value={value} onChange={event => onChange(skill, event.target.value)}>
      <option value=''>选择分类</option>
      {SKILL_CATEGORIES.map(([id, label]) => <option key={id} value={id}>{label}</option>)}
    </select>
  </label>;
}
