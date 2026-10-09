const EXACT_SKILL_ICONS = {
  'yii/artifact-publish': 'app',
  'zhy8882/humanizer': 'writing',
  'david/artifact-publish-test': 'test',
  'jk7534176/artifact': 'workspace',
  'ss92/selfhost-public-webapp': 'app',
  'yii/image-generation': 'image',
  'lin/web-search': 'search',
  'jk7534176/financial-sentiment-manager': 'finance',
  'jk7534176/manuscript-standards-check': 'review',
  'yii/deepseek-web-search': 'search',
  'arrowhaken/yunzhun-project-summary': 'workspace',
  'jk7534176/meeting-audio-minutes': 'audio',
  'arrowhaken/shimo-reader': 'document',
  'jk7534176/reviewer-editor-decision': 'review',
  'jk7534176/reviewer-quality-audit': 'review',
  'jk7534176/reviewer-novelty-position': 'search',
  'jk7534176/reviewer-reporting-checklist': 'review',
  'jk7534176/reviewer-report-writer': 'writing',
  'jk7534176/reviewer-data-consistency': 'data',
  'jk7534176/reviewer-methods-stats': 'data',
  'jk7534176/reviewer-triage-desk': 'review',
  'pi-dal/summarize-ai-request-logging-features': 'monitoring',
  'do/wechat-product-tips': 'test',
  'arrowhaken/test9-7': 'test',
  'arrowhaken/blackboard-realtime': 'collaboration',
  'jk7534176/research-proactive-pilot': 'workspace',
  'do/literature-deep-review-clinical': 'research',
  'atridaisuki/cloud-html-artifact': 'app',
  'jk7534176/ccc-product-doc-editor': 'document',
  'jk7534176/ccc-ethics-approval': 'review',
};

const CATEGORY_ICON_FALLBACKS = {
  development: 'development',
  design: 'design',
  writing: 'writing',
  office: 'office',
  data: 'data',
  business: 'business',
  finance: 'finance',
  search: 'search',
  education: 'education',
  life: 'life',
  other: 'other',
};

function searchableSkillText(skill) {
  const tags = Array.isArray(skill?.tags) ? skill.tags.join(' ') : '';
  return [
    skill?.skillId,
    skill?.cloudSkillId,
    skill?.localSkillId,
    skill?.displayName,
    skill?.display_name,
    skill?.name,
    skill?.title,
    tags,
    skill?.description,
  ].filter(Boolean).join(' ').replace(/[-_/]/g, ' ').toLocaleLowerCase();
}
export function skillIconKey(skill, category = '') {
  const skillId = String(skill?.skillId || skill?.cloudSkillId || '').trim().toLocaleLowerCase();
  if (EXACT_SKILL_ICONS[skillId]) return EXACT_SKILL_ICONS[skillId];

  const text = searchableSkillText(skill);
  if (/\b(agent browser|browser automation|playwright|puppeteer|selenium)\b|浏览器自动化/.test(text)) return 'browser';
  if (/\b(image generation|image generator|image edit|photo edit|ocr)\b|图像生成|图片生成|图像编辑|图片编辑|图像识别/.test(text)) return 'image';
  if (/\b(meeting audio|audio minutes|transcri(?:be|pt|ption)|speech to text)\b|会议录音|音频转写|语音转写|会议纪要/.test(text)) return 'audio';
  if (/\b(web search|deep search|search engine)\b|网页搜索|网络搜索|联网搜索/.test(text)) return 'search';
  if (/\b(artifact|webapp|web app|html app|application publish)\b|网页应用|应用发布|云端应用/.test(text)) return 'app';
  if (/\b(review|reviewer|audit|checklist|standards check|approval)\b|审稿|审核|审查|质检|规范检查|合规检查|审批/.test(text)) return 'review';
  if (/\b(document|manuscript|reader|editor|docs?)\b|文档|稿件|报告编辑|文件读取/.test(text)) return 'document';
  if (/\b(dashboard|workbench|blackboard|collaboration|project summary)\b|工作台|驾驶舱|看板|协作|项目总表/.test(text)) return 'workspace';
  if (/\b(finance|financial|stock|investment|accounting)\b|金融|股票|投资|财务/.test(text)) return 'finance';
  if (/\b(data|analytics|statistics|sql|database|spreadsheet)\b|数据|统计|数据库|报表/.test(text)) return 'data';
  if (/\b(research|literature|paper)\b|研究|科研|文献|论文/.test(text)) return 'research';
  if (/\b(test|deprecated|demo)\b|测试|已废弃|请勿使用/.test(text)) return 'test';
  if (/\b(write|writing|copy|humanizer|translate)\b|写作|文案|润色|翻译/.test(text)) return 'writing';

  return CATEGORY_ICON_FALLBACKS[category] || 'other';
}
