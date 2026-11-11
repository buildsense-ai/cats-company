// Independent product identities prevent old Token orders from being relabelled
// or submitted at the new credit prices. Checkout still requires a server match.
export const creditPlanOffers = [
  { id: -3900, slug: 'catsco-credit-v1-monthly', name: '入门版', description: '适合提炼资料重点、起草邮件和文案。', price_fen: 3900, currency: 'CNY', duration_days: 30, plan_tier: 0 },
  { id: -7900, slug: 'catsco-credit-v2-monthly', name: '日常版', description: '适合每天整理文件、汇总数据和待办。', price_fen: 7900, currency: 'CNY', duration_days: 30, plan_tier: 0 },
  { id: -15900, slug: 'catsco-credit-v3-monthly', name: '专业版', description: '适合多项工作并行、持续推进项目。', price_fen: 15900, currency: 'CNY', duration_days: 30, plan_tier: 0 },
];

const details = [
  { code: 'V1', credits: 4000, rate: '无优惠', focus: '基础模型体验', details: ['支持 DeepSeek Flash', '适合轻量调用与基础体验'], examples: ['短文阅读与重点摘要', '邮件回复与日常文案起草', '基础表格整理与格式转换', '简单问答与资料归纳'] },
  { code: 'V2', credits: 12000, rate: '无优惠', focus: '全模型 · 日常使用', details: ['可切换所有当前支持模型', '3x 入门版额度'], examples: ['多份资料对比与综合归纳', '数据分析与图表解读', '代码编写、解释与问题排查', '项目方案与报告撰写'] },
  { code: 'V3', credits: 22500, rate: '9 折', focus: '全模型 · 高频使用', details: ['可切换所有当前支持模型', '5x 入门版额度 + 2,500 credit'], examples: ['更频繁的长文与资料分析', '复杂代码与方案多轮迭代', '研究、写作任务持续推进', '多项工作交替处理与复盘'] },
];
export const creditPlanPresentation = Object.fromEntries(creditPlanOffers.map((offer, index) => {
  const plan = details[index];
  return [offer.slug, {
    creditPlan: true, kicker: `CATSCO ${plan.code}`, recommended: plan.code === 'V2', tagline: null,
    originalPriceFen: plan.code === 'V2' ? 10900 : plan.code === 'V3' ? 18900 : undefined,
    actionLabel: `获取 CatsCo ${offer.name}`,
    highlights: [{ kind: 'capacity', label: '月额度', value: String(plan.credits), unit: 'credit' }, { kind: 'discount', label: '模型调用优惠', value: plan.rate }],
    featuresLabel: false, features: [{ label: plan.focus, details: plan.details }, ...plan.examples],
    purchaseDescription: `¥${offer.price_fen / 100} / 月，每周期 ${plan.credits} credit。按自然月对日计算，月额度当期有效，不结转；环境、文件和历史对话暂不设置清理期限。`,
  }];
}));
export const creditPlanNote = '月额度当期有效，不结转；充值 1 元 = 100 credit。模型调用按官方价格、实际用量及套餐折扣扣费。';
