// Independent product identities prevent old Token orders from being relabelled
// or submitted at the new credit prices. Checkout still requires a server match.
export const creditPlanOffers = [
  { id: -5990, slug: 'catsco-credit-v1-monthly', name: '入门版', description: '适合提炼资料重点、起草邮件和文案。', price_fen: 5990, currency: 'CNY', duration_days: 30, plan_tier: 0 },
  { id: -9990, slug: 'catsco-credit-v2-monthly', name: '日常版', description: '适合每天整理文件、汇总数据和待办。', price_fen: 9990, currency: 'CNY', duration_days: 30, plan_tier: 0 },
  { id: -16990, slug: 'catsco-credit-v3-monthly', name: '重度版', description: '适合多项工作并行、持续推进项目。', price_fen: 16990, currency: 'CNY', duration_days: 30, plan_tier: 0 },
];

const details = [
  { code: 'V1', credits: '60', rate: '95%', focus: '基础模型体验', details: ['仅支持 DeepSeek Flash', '适合轻量调用与基础体验'], examples: ['短文阅读与重点摘要', '邮件回复与日常文案起草', '基础表格整理与格式转换', '简单问答与资料归纳'] },
  { code: 'V2', credits: '105', rate: '90%', focus: '全模型 · 日常使用', details: ['可切换所有当前支持模型', '较入门版每月多 45 credit'], examples: ['多份资料对比与综合归纳', '数据分析与图表解读', '代码编写、解释与问题排查', '项目方案与报告撰写'] },
  { code: 'V3', credits: '189', rate: '85%', focus: '全模型 · 高频使用', details: ['可切换所有当前支持模型', '较日常版每月多 84 credit'], examples: ['更频繁的长文与资料分析', '复杂代码与方案多轮迭代', '研究、写作任务持续推进', '多项工作交替处理与复盘'] },
];
export const creditPlanPresentation = Object.fromEntries(creditPlanOffers.map((offer, index) => {
  const plan = details[index];
  return [offer.slug, {
    creditPlan: true, kicker: `CATSCO ${plan.code}`, recommended: plan.code === 'V2', tagline: null,
    originalPriceFen: plan.code === 'V2' ? 11990 : undefined,
    actionLabel: `获取 CatsCo ${offer.name}`,
    highlights: [{ kind: 'capacity', label: '月额度', value: plan.credits, unit: 'credit' }, { kind: 'rate', label: '调用计费', value: `官方价 × ${plan.rate}` }],
    featuresLabel: false, features: [{ label: plan.focus, details: plan.details }, ...plan.examples],
    purchaseDescription: `¥${offer.price_fen / 100} / 月，每周期 ${plan.credits} credit。按自然月对日计算，月额度当期有效，不结转；环境、文件和历史对话暂不设置清理期限。`,
  }];
}));
export const creditPlanNote = '月额度当期有效，不结转；充值 1 元 = 1 credit。模型调用按官方价格、实际用量及套餐折扣扣费。';
