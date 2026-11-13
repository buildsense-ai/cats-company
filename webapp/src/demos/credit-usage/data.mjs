import { micros, calculateCall, consume } from './billing.mjs';
import { creditPlanOffers } from '../../widgets/plans/credit-plan-offers.js';
const creditPlans = Object.fromEntries(creditPlanOffers.map((offer, index) => [['V1', 'V2', 'V3'][index], offer]));
export const people = [
  { id: '1001', name: '林悦', plan: 'V2' }, { id: '1002', name: '陈墨', plan: 'V3' },
  { id: '1003', name: '周禾', plan: 'V1' }, { id: '1004', name: '许青', plan: 'V2' },
  { id: '1005', name: '沈宁', plan: '体验' },
];
export const agents = [
  { id: 'agent-research', name: '资料研究助手' }, { id: 'agent-writing', name: '内容写作助手' }, { id: 'agent-code', name: '代码助手' },
];
// All names, prices, token counts and receipts below are synthetic fixtures.
// They do not claim current official model prices or actual user consumption.
export const priceVersions = [
  { id: 'DEMO-FLASH-20261101', model: 'DeepSeek Flash', provider: '演示模型网关', source: '演示价格表 / Flash', input: micros(2), cached: micros(0.2), output: micros(3) },
  { id: 'DEMO-REASONING-20261101', model: '推理模型（示例）', provider: '演示模型网关', source: '演示价格表 / 推理模型', input: micros(18), cached: micros(1.8), output: micros(55) },
  { id: 'DEMO-CODING-20261101', model: '代码模型（示例）', provider: '演示模型网关', source: '演示价格表 / 代码模型', input: micros(14), cached: micros(1.4), output: micros(42) },
];
const tasks = ['市场资料对比与分析', '产品周报与总结', '报表清洗与可视化', '接口问题排查', '长文要点与内容起草', '技术方案迭代'];
export const opening = Object.fromEntries(people.map(person => [person.id, {
  monthly: micros(creditPlans[person.plan]?.monthlyCredits || 0),
  gift: micros(person.plan === '体验' ? 0.05 : 20), recharge: micros(person.plan === '体验' ? 0.05 : 30), debt: 0,
}]));
const balances = structuredClone(opening);
const records = [];
for (let i = 0; i < 45; i++) {
  const person = people[i % people.length];
  const price = priceVersions[person.plan === 'V1' || person.plan === '体验' ? 0 : i % 3];
  const agent = agents[i % agents.length];
  const usage = { input: 42000 + (i * 9317 % 160000), cached: 12000 + (i * 641 % 30000), output: 2200 + (i * 953 % 18000) };
  const amount = calculateCall(usage, { input: price.input, cached: price.cached, output: price.output }, creditPlans[person.plan]?.discountBps || creditPlans.V1.discountBps);
  const before = { ...balances[person.id] };
  const { after, allocation } = consume(before, amount, true);
  balances[person.id] = after;
  const day = String(7 + Math.floor(i / 15)).padStart(2, '0'), hour = String(9 + Math.floor((i % 15) / 2)).padStart(2, '0'), minute = i % 2 ? 38 : 12;
  records.push({
    id: `DEMO-LDG-${String(i + 1).padStart(4, '0')}`, requestId: `demo-req-${String(i + 101).padStart(6, '0')}`,
    taskId: person.plan === '体验' ? 'demo-task-retained-05' : `demo-task-${person.id}-${String(i % 17 + 1).padStart(3, '0')}`,
    task: person.plan === '体验' ? '基础资料归纳（保留运行任务）' : tasks[i % tasks.length],
    person, actorId: person.id, payerId: person.id, agent, price, usage, rate: creditPlans[person.plan]?.discountBps || creditPlans.V1.discountBps,
    at: `2026-11-${day}T${hour}:${minute}:00+08:00`, settledAt: `2026-11-${day}T${hour}:${minute}:03+08:00`,
    amount, before, after, allocation, status: 'settled', sequence: i + 1,
    providerReceiptId: `DEMO-RECEIPT-${i + 1}`, provenance: '本地演示数据集 / data.mjs',
  });
}
const pending = records.at(-1);
records.push({ ...pending, id: 'DEMO-LDG-0046', requestId: 'demo-req-pending', at: '2026-11-09T17:02:00+08:00', settledAt: null, amount: null, allocation: null, before: null, after: null, status: 'pending', usage: null, sequence: 46, providerReceiptId: null, task: '调用回执等待同步' });
export const calls = records;
export const accountBalances = balances;
export const labels = { monthly: '月额度', gift: '邀请赠送', recharge: '付费充值', debt: '新增欠款' };
