export const personalPlanOffers = [
  { id: -59, slug: 'catsco-basic', name: '基础版', description: '适合提炼资料重点、起草邮件和文案。', price_fen: 5900, currency: 'CNY', sale_state: 'public', duration_days: 30 },
  { id: 21, slug: 'catsco-personal', name: '个人版', description: '适合每天整理文件、汇总数据和处理待办。', price_fen: 9900, currency: 'CNY', sale_state: 'public', duration_days: 30 },
  { id: 22, slug: 'catsco-pro', name: '专业版', description: '适合多项工作并行、持续推进复杂项目。', price_fen: 15900, currency: 'CNY', sale_state: 'public', duration_days: 30 },
];

const cloudNote = '云端 AI 员工随套餐有效；到期后保留 15 天，期间续费可恢复。';
export const planPresentationOverrides = {
  'catsco-basic': {
    kicker: 'CATSCO BASIC', tagline: '轻量日常协作', actionLabel: '获取 CatsCo 基础版',
    highlights: [
      { kind: 'capacity', label: 'Token 额度', value: '5 亿', unit: 'Token' },
    ],
    featuresLabel: false,
    features: [
      { label: 'Office 文件处理', details: ['文档阅读与写作', '表格整理与数据汇总'] },
      '资料检索与信息整理',
      '邮件、文案与报告写作',
      '制作并发布网页与小应用',
      '文件、图片与成果预览',
      '任务进度与执行过程查看',
    ],
  },
  'catsco-personal': {
    kicker: 'CATSCO PERSONAL', tagline: '日常工作自动化', actionLabel: '获取 CatsCo 个人版',
    highlights: [
      { kind: 'capacity', label: 'Token 额度', value: '约 10 亿', unit: 'Token' },
    ],
    featuresLabel: false,
    features: [
      { label: '基础版中的全部权益，以及：', emphasis: true },
      'Agent 后台任务，持续执行',
      '授权设备、文件与工具调用',
      { label: '个人 Skill 与工作偏好', details: ['安装与管理常用 Skill', '保存工作偏好与常用流程'] },
      '持续会话，接着上次的工作',
      '任务成果集中管理与分享',
    ],
    note: cloudNote,
  },
  'catsco-pro': {
    kicker: 'CATSCO PRO', tagline: '高频多任务协作', actionLabel: '获取 CatsCo 专业版', pro: true,
    highlights: [
      { kind: 'capacity', label: 'Token 额度', value: '约 20 亿', unit: 'Token' },
    ],
    featuresLabel: false,
    features: [
      { label: '个人版中的全部权益，以及：', emphasis: true },
      { label: 'Agent 多任务并行', details: ['资料整理与报告撰写并行推进', '多个后台任务同时执行'] },
      '复杂任务，分步持续推进',
      '跨会话检索历史工作与决策',
      '从已完成任务沉淀可复用 Skill',
      '高峰期优先响应',
    ],
    note: cloudNote,
  },
};

export const teamPlanOffer = { id: -599, slug: 'catsco-team-monthly', name: '团队版', price_fen: 59900, currency: 'CNY', duration_days: 30 };
export const tokenEstimateNote = 'Token 数为估算值，实际可用量随所选模型、输入输出比例、缓存命中情况及模型价格变化。';

// Only exact catalogue matches can enter checkout. Display drafts never replace server prices.
export function resolvePlanOffers(offers, catalog, enabled = true) {
  return offers.map(offer => {
    const match = enabled && catalog.find(plan => Number(plan.id) > 0 && plan.slug === offer.slug
      && Number(plan.price_fen) === offer.price_fen
      && Number(plan.duration_days || 30) === offer.duration_days
      && (plan.currency || 'CNY') === offer.currency);
    return match ? { ...match, description: offer.description ?? match.description }
      : { ...offer, catalogUnavailable: true };
  });
}
