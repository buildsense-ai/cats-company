// Reviewed labels for the current public catalogue. Future entries use their
// published translations; package IDs and executable content stay untouched.
export const CATALOGUE_PRESENTATIONS = Object.fromEntries([
  ['yii/artifact-publish', '1.0.6', '应用发布', 'development'],
  ['zhy8882/humanizer', '1.0.0', '自然表达润色', 'writing', '在不改变原意的前提下改写带有 AI 痕迹的文字，使其符合作者的表达方式。用于编辑或审阅文章中的机械对比、单句收尾、刻意开场、固定三项列举、过多破折号、夸大表述、推销措辞、套话、加粗标签和赘语。依据维基百科的“AI 写作迹象”整理。'],
  ['david/artifact-publish-test', '1.0.0', '应用发布（测试）', 'development'],
  ['jk7534176/artifact', '1.0.1', '高校教师个人工作平台', 'education'],
  ['ss92/selfhost-public-webapp', '1.0.3', '自建共享网页应用', 'development'],
  ['yii/image-generation', '1.0.2', '图像生成与编辑', 'design', '通过当前 XiaoBa Agent 的 CatsCo Relay 账号生成或编辑位图。沿用 Codex 内置图像工具的生成、编辑、批量处理接口与工作规则，通过 Relay 传输，无需 OpenAI 密钥。适用于图像创作、局部修改和生成变体；适合用 SVG、HTML、CSS 或代码绘制的视觉内容不使用此能力。'],
  ['lin/web-search', '1.0.2', '网页搜索', 'search', '通过用户的 CatsCo Relay 账号搜索最新网页信息并研究来源链接。用于查找外部证据与引用来源，不用于搜索本地文件。'],
  ['jk7534176/financial-sentiment-manager', '1.0.0', '金融舆情管理', 'finance'],
  ['jk7534176/manuscript-standards-check', '1.0.0', '稿件国标规范检查', 'writing'],
  ['yii/deepseek-web-search', '1.0.1', 'DeepSeek 网页搜索', 'search', '通过当前 XiaoBa Agent 的 CatsCo Relay DeepSeek 搜索获取实时网页信息，再读取公开来源页面作为证据。搜索返回来源标题和链接，打开页面可获得引用片段，部分页面可能无法读取。用于最新资讯、引用和来源核查，不用于本地文件。'],
  ['arrowhaken/yunzhun-project-summary', '1.0.2', '云准项目总表', 'business'],
  ['jk7534176/meeting-audio-minutes', '1.0.0', '会议录音转纪要', 'office'],
  ['arrowhaken/shimo-reader', '1.0.6', '石墨文档读取', 'office'],
  ['jk7534176/reviewer-editor-decision', '1.0.0', '编辑审稿决策', 'writing'],
  ['jk7534176/reviewer-quality-audit', '1.0.0', '审稿意见质检', 'writing'],
  ['jk7534176/reviewer-novelty-position', '1.0.0', '稿件创新性定位', 'search'],
  ['jk7534176/reviewer-reporting-checklist', '1.0.0', '研究报告规范核查', 'writing'],
  ['jk7534176/reviewer-report-writer', '1.0.0', '审稿报告撰写', 'writing'],
  ['jk7534176/reviewer-data-consistency', '1.0.0', '论文数据一致性核查', 'data'],
  ['jk7534176/reviewer-methods-stats', '1.0.0', '研究方法与统计审查', 'data'],
  ['jk7534176/reviewer-triage-desk', '1.0.0', '期刊来稿初筛', 'writing'],
  ['pi-dal/summarize-ai-request-logging-features', '1.0.0', 'AI 请求日志功能概览', 'development', '当用户需要简要回顾功能时，列出并概括已实现的 AI 请求日志与可观测性功能，包括已报告的待办事项。'],
  ['do/wechat-product-tips', '1.0.2', '微信产品提示（已废弃）', 'other'],
  ['arrowhaken/test9-7', '1.0.0', '测试能力（请勿使用）', 'other'],
  ['arrowhaken/blackboard-realtime', '1.0.0', '实时协作任务黑板', 'office'],
  ['jk7534176/research-proactive-pilot', '1.0.0', '科研流程驾驶舱', 'education'],
  ['do/literature-deep-review-clinical', '1.0.0', '临床文献精读', 'search'],
  ['atridaisuki/cloud-html-artifact', '1.0.2', '云端网页与应用', 'development'],
  ['jk7534176/ccc-product-doc-editor', '1.0.0', '产品介绍文档编辑', 'writing'],
  ['jk7534176/ccc-ethics-approval', '1.0.1', '科研伦理审批审查', 'education'],
].map(([id, version, name, category, description]) => [id, { version, name, category, description }]));

export function cataloguePresentation(skill) {
  const entry = CATALOGUE_PRESENTATIONS[skill?.skillId];
  const version = String(skill?.latestVersion || skill?.version || '').replace(/^v/i, '');
  return entry && version === entry.version ? entry : null;
}

export function skillSourceQuery(query) {
  const value = String(query || '').trim().toLocaleLowerCase();
  if (!/[\u3400-\u9fff]/.test(value)) return query;
  const matches = Object.entries(CATALOGUE_PRESENTATIONS).filter(([, entry]) => entry.name.toLocaleLowerCase().includes(value));
  return matches.length === 1 ? matches[0][0].split('/').pop() : matches.length ? '' : query;
}
