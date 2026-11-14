// Independent product identities prevent old Token orders from being relabelled
// or submitted at the new credit prices. Checkout still requires a server match.
export const creditPlanOffers = [
  { id: -3900, slug: 'catsco-credit-v1-monthly', name: '入门版', description: '适合提炼资料重点、起草邮件和文案。', price_fen: 3900, currency: 'CNY', duration_days: 30, plan_tier: 1, monthlyCredits: 4000, discountBps: 10000 },
  { id: -7900, slug: 'catsco-credit-v2-monthly', name: '日常版', description: '适合每天整理文件、汇总数据和待办。', price_fen: 7900, currency: 'CNY', duration_days: 30, plan_tier: 2, monthlyCredits: 12000, discountBps: 10000 },
  { id: -15900, slug: 'catsco-credit-v3-monthly', name: '专业版', description: '适合多项工作并行、持续推进项目。', price_fen: 15900, currency: 'CNY', duration_days: 30, plan_tier: 3, monthlyCredits: 22500, discountBps: 9000 },
];

const details = [
  { code: 'V1', focus: '基础模型体验', details: ['支持 DeepSeek Flash', '适合轻量调用与基础体验'], examples: ['短文阅读与重点摘要', '邮件回复与日常文案起草', '基础表格整理与格式转换', '简单问答与资料归纳'] },
  { code: 'V2', focus: '日常使用', details: ['可切换已开通 credit 计费的模型，当前支持 DeepSeek Flash', '3x 入门版额度'], examples: ['多份资料对比与综合归纳', '数据分析与图表解读', '代码编写、解释与问题排查', '项目方案与报告撰写'] },
  { code: 'V3', focus: '高频使用', details: ['可切换已开通 credit 计费的模型，当前支持 DeepSeek Flash', '5x 入门版额度 + 2,500 credit'], examples: ['更频繁的长文与资料分析', '复杂代码与方案多轮迭代', '研究、写作任务持续推进', '多项工作交替处理与复盘'] },
];
export const creditPlanPresentation = Object.fromEntries(creditPlanOffers.map((offer, index) => {
  const plan = details[index];
  return [offer.slug, {
    creditPlan: true, kicker: `CATSCO ${plan.code}`, recommended: plan.code === 'V2', tagline: null,
    actionLabel: `获取 CatsCo ${offer.name}`,
    highlights: [{ kind: 'capacity', label: '月额度', value: String(offer.monthlyCredits), unit: 'credit' }, { kind: 'discount', label: '模型调用优惠', value: offer.discountBps === 10000 ? '无优惠' : `${offer.discountBps / 1000} 折` }],
    featuresLabel: false, features: [{ label: plan.focus, details: plan.details }, ...plan.examples],
    purchaseDescription: `¥${offer.price_fen / 100} / 月，每周期 ${offer.monthlyCredits} credit。按自然月对日计算，月额度当期有效，不结转；提前续费不提前补额。机器期限单独显示，已保留环境到期暂停、暂不自动清理，显式删除仍有效。`,
  }];
}));
export const creditPlanNote = '月额度当期有效，不结转；1 元对应 100 credit，独立余额充值暂未开放。模型调用按官方适用价格 × 2（含平台服务费）× 实际用量 × 套餐折扣，再换算为 credit。';
