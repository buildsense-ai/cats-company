import '../styles/pages/pricing.css';
import { appLoginUrl, appUrl } from '../site-links';
import { useState } from 'react';
import type { KeyboardEvent } from 'react';
import { Icon } from './Icons';
import type { IconName } from './Icons';
type PlanFeature = {
  label: string;
  details?: string[];
  inherited?: boolean;
};
type PricingPlan = {
  slug: string;
  name: string;
  kicker: string;
  price: number;
  tagline: string;
  description: string;
  capacity: string;
  features: PlanFeature[];
  note?: string;
};

// Draft prices mirror webapp/src/widgets/plans/plan-offers.js.
// The workspace catalogue confirms live prices and availability before purchase.
const cloudNote = '云端 AI 员工随套餐有效；到期后保留 15 天，期间续费可恢复。';
const plans: PricingPlan[] = [{
  slug: 'catsco-basic',
  name: '基础版',
  kicker: 'CATSCO BASIC',
  price: 59,
  tagline: '轻量日常协作',
  description: '适合提炼资料重点、起草邮件和文案。',
  capacity: '5 亿',
  features: [{
    label: 'Office 文件处理',
    details: ['文档阅读与写作', '表格整理与数据汇总']
  }, {
    label: '资料检索与信息整理'
  }, {
    label: '邮件、文案与报告写作'
  }, {
    label: '制作并发布网页与小应用'
  }, {
    label: '文件、图片与成果预览'
  }, {
    label: '任务进度与执行过程查看'
  }]
}, {
  slug: 'catsco-standard',
  name: '个人版',
  kicker: 'CATSCO PERSONAL',
  price: 99,
  tagline: '日常工作自动化',
  description: '适合每天整理文件、汇总数据和待办。',
  capacity: '约 10 亿',
  features: [{
    label: '基础版中的全部权益，以及：',
    inherited: true
  }, {
    label: '后台任务，持续执行'
  }, {
    label: '授权设备、文件与工具调用'
  }, {
    label: '个人 Skill 与工作偏好',
    details: ['安装与管理常用 Skill', '保存工作偏好与常用流程']
  }, {
    label: '持续会话，接着上次的工作'
  }, {
    label: '任务成果集中管理与分享'
  }],
  note: cloudNote
}, {
  slug: 'catsco-advanced',
  name: '专业版',
  kicker: 'CATSCO PRO',
  price: 159,
  tagline: '高频多任务协作',
  description: '适合多项工作并行、持续推进项目。',
  capacity: '约 20 亿',
  features: [{
    label: '个人版中的全部权益，以及：',
    inherited: true
  }, {
    label: '多任务并行',
    details: ['资料整理与报告撰写并行推进', '多个后台任务同时执行']
  }, {
    label: '复杂任务，分步持续推进'
  }, {
    label: '跨会话检索历史工作与决策'
  }, {
    label: '从已完成任务沉淀可复用 Skill'
  }, {
    label: '高峰期优先响应'
  }],
  note: cloudNote
}];
const teamPlan: PricingPlan = {
  slug: 'catsco-team',
  name: '团队版',
  kicker: 'CATSCO TEAM',
  price: 599,
  tagline: '团队协作',
  description: '适合团队共同使用与管理 AI 员工。',
  capacity: '最高 30 亿',
  features: []
};
const teamBenefits: Array<{
  icon: IconName;
  title: string;
  description: string;
}> = [{
  icon: 'computer',
  title: '独享服务器',
  description: '8 核 CPU · 16 GB 内存，团队独立使用'
}, {
  icon: 'layers',
  title: '团队经验与 Skill 共同沉淀',
  description: '把工作中积累的经验和 Skill 留给整个团队复用'
}, {
  icon: 'file',
  title: '共享知识库',
  description: '团队资料集中管理，成员共同查阅与使用'
}, {
  icon: 'shield',
  title: '团队权限管理',
  description: '管理成员权限与团队资源的访问范围'
}];
const businessServices: Array<{
  icon: IconName;
  title: string;
  description: string;
}> = [{
  icon: 'computer',
  title: '环境准备',
  description: '企业使用环境的基础配置、账号与运行检查'
}, {
  icon: 'users',
  title: '团队启动',
  description: '面向负责人和实际使用者的启动培训'
}, {
  icon: 'target',
  title: '场景梳理',
  description: '梳理一个边界清晰、可验证的首批业务场景'
}, {
  icon: 'spark',
  title: 'Skill 初始化',
  description: '初始化约定范围内的首批 Skill'
}, {
  icon: 'file',
  title: '上线支持',
  description: '上线初期反馈处理与约定范围内的小幅调整'
}];
const comparisonPlans = [...plans, teamPlan];
type ComparisonValue = string | boolean;
const comparisonSections: Array<{
  title: string;
  rows: Array<{ label: string; values: ComparisonValue[] }>;
}> = [{
  title: '方案与额度',
  rows: [
    { label: '方案价格', values: comparisonPlans.map(plan => '¥' + plan.price + ' / 期') },
    { label: '每期有效期', values: comparisonPlans.map(() => '30 天') },
    { label: '适合的工作方式', values: comparisonPlans.map(plan => plan.tagline) },
    { label: '参考用量额度', values: comparisonPlans.map(plan => plan.capacity + ' Token') }
  ]
}, {
  title: '日常工作与持续执行',
  rows: [
    { label: 'Office 文件处理与成果预览', values: [true, true, true, '待确认'] },
    { label: '后台任务与持续会话', values: [false, true, true, '待确认'] },
    { label: '多任务并行与复杂任务推进', values: [false, false, true, '待确认'] },
    { label: '经验与工作偏好沉淀', values: [false, '个人 Skill 与工作偏好', '任务经验与可复用 Skill', '团队经验与 Skill'] }
  ]
}, {
  title: '团队协作与资源',
  rows: [
    { label: '团队成员', values: ['个人使用', '个人使用', '个人使用', '最多 10 人'] },
    { label: '共享知识库', values: [false, false, false, true] },
    { label: '团队权限管理', values: [false, false, false, true] },
    { label: '团队独享服务器', values: [false, false, false, '8 核 CPU · 16 GB 内存'] }
  ]
}];
function ComparisonCell({ value }: { value: ComparisonValue }) {
  if (typeof value === 'boolean') {
    return <span className={value ? 'pricing-comparison-check' : 'pricing-comparison-empty'}>
      {value ? <Icon name="check" /> : <span aria-hidden="true">—</span>}
      <span className="pricing-sr-only">{value ? '列入方案' : '未列入本方案'}</span>
    </span>;
  }
  return <span className={value === '待确认' ? 'pricing-comparison-pending' : undefined}>{value}</span>;
}
const pricingFaqs = [{
  question: '基础版、个人版和专业版有什么区别？',
  answer: '基础版面向资料整理、Office 文件处理和日常写作；个人版增加持续执行的后台任务、授权设备调用和个人工作偏好；专业版进一步提供多任务并行、复杂任务推进、历史工作检索和经验沉淀。套餐详情以工作台购买确认页显示为准。'
}, {
  question: '价格按月还是按年？',
  answer: '当前展示基础版 ¥59、个人版 ¥99、专业版 ¥159、团队版 ¥599，每期 30 天，页面简写为“/ 月”。尚未公布年付价格或折扣，也不会在此页发起自动续费。'
}, {
  question: '页面上的额度如何理解？',
  answer: '额度是用于估算可用量的参考，实际能完成的工作随所选模型、任务内容和文件长度而变化，不代表固定的任务或对话次数，也不承诺无限使用。额度及计量规则以工作台套餐说明为准。'
}, {
  question: '现在可以正式购买或升级吗？',
  answer: '点击套餐按钮即可前往 CatsCo 工作台，查看对应套餐并确认购买或升级。尚未登录时会先进入登录流程，登录后继续所选套餐；实际价格、可售状态和升级规则以工作台显示为准。'
}, {
  question: '云端 AI 员工到期后会怎样？',
  answer: '个人版和专业版的套餐说明为：云端 AI 员工随套餐有效，到期后保留 15 天，期间续费可恢复。具体保留和恢复条件以工作台套餐说明为准。'
}, {
  question: '团队版和 Business Start 有什么区别？',
  answer: '团队版是最多 10 人共同使用和管理 AI 员工的套餐，包含独享服务器、共享知识库和权限管理。Business Start 是 ¥4,999 / 月起的企业落地服务，按约定范围提供环境准备、启动培训、首批场景和 Skill 初始化；复杂集成、私有化和长期支持需另行咨询。'
}, {
  question: '邀请码会改变套餐价格吗？',
  answer: '不会。邀请码用于内测准入和服务容量管理，不是折扣券。已有邀请码可通过页面下方入口前往 CatsCo 工作台登录。'
}];
function Price({
  amount
}: {
  amount: number;
}) {
  return <div className="pricing-price"><span>¥</span><strong>{amount.toLocaleString('zh-CN')}</strong><small>/ 月</small></div>;
}
function PaymentIcon() {
  return <svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8" aria-hidden="true"><rect x="2" y="4" width="20" height="16" rx="3" /><path d="M2 10h20M6 15h4" /></svg>;
}
const audienceTabs = [
  { value: 'personal', label: '个人' },
  { value: 'team', label: '团队' },
  { value: 'enterprise', label: '企业' }
] as const;
type PricingAudience = typeof audienceTabs[number]['value'];

export function PricingPage() {
  const [audience, setAudience] = useState<PricingAudience>('personal');
  const [openFaqs, setOpenFaqs] = useState<Set<number>>(() => new Set());
  const allFaqsOpen = openFaqs.size === pricingFaqs.length;
  function handleTabKeys(event: KeyboardEvent<HTMLDivElement>) {
    if (!['ArrowLeft', 'ArrowRight', 'Home', 'End'].includes(event.key)) return;
    event.preventDefault();
    const currentIndex = audienceTabs.findIndex(tab => tab.value === audience);
    const nextIndex = event.key === 'Home' ? 0
      : event.key === 'End' ? audienceTabs.length - 1
      : (currentIndex + (event.key === 'ArrowRight' ? 1 : -1) + audienceTabs.length) % audienceTabs.length;
    const next = audienceTabs[nextIndex].value;
    setAudience(next);
    document.getElementById(`pricing-${next}-tab`)?.focus();
  }
  function toggleFaq(index: number) {
    setOpenFaqs(current => {
      const next = new Set(current);
      if (next.has(index)) next.delete(index);else next.add(index);
      return next;
    });
  }
  return <main id="main-content" className="pricing-page">
      <section className="pricing-shell" aria-labelledby="pricing-title">
        <header className="pricing-hero"><h1 id="pricing-title">选择适合你的套餐</h1></header>
        <div className="pricing-audience-tabs" role="tablist" aria-label="选择方案类型" onKeyDown={handleTabKeys}>
          {audienceTabs.map(({ value, label }) => <button key={value} id={`pricing-${value}-tab`} type="button" role="tab" aria-selected={audience === value} aria-controls={`pricing-${value}-panel`} tabIndex={audience === value ? 0 : -1} onClick={() => setAudience(value)}>{label}</button>)}
        </div>
        <div id="pricing-personal-panel" role="tabpanel" aria-labelledby="pricing-personal-tab" hidden={audience !== 'personal'} tabIndex={0}>
          <div className="pricing-grid">
            {plans.map(plan => <article className="pricing-card" key={plan.slug} aria-labelledby={`${plan.slug}-title`}>
              <span className="pricing-kicker">{plan.kicker}</span><h2 id={`${plan.slug}-title`}>{plan.name}</h2>
              <Price amount={plan.price} /><span className="pricing-tagline">{plan.tagline}</span><p className="pricing-description">{plan.description}</p>
              <a className="pricing-plan-action" href={appUrl('/', { open: 'relay', plan: plan.slug, source: 'pricing' })}><PaymentIcon />获取 CatsCo {plan.name}</a>
              <dl className="pricing-allowances"><div><dt><Icon name="layers" />Token 额度</dt><dd>{plan.capacity}<span>Token</span></dd></div></dl>
              <div className="pricing-feature-divider" />
              <ul className="pricing-features">{plan.features.map(feature => <li key={feature.label} className={feature.inherited ? 'is-inherited' : undefined}>
                <Icon name="check" /><div><span>{feature.label}</span>{feature.details && <ul className="pricing-feature-details">{feature.details.map(detail => <li key={detail}>{detail}</li>)}</ul>}</div>
              </li>)}</ul>
              {plan.note && <p className="pricing-footnote">{plan.note}</p>}
            </article>)}
          </div>
        </div>
        <div id="pricing-team-panel" role="tabpanel" aria-labelledby="pricing-team-tab" hidden={audience !== 'team'} tabIndex={0}>
          <section className="pricing-team" aria-labelledby="pricing-team-title">
            <div className="pricing-team-offer">
              <span className="pricing-kicker">CATSCO TEAM</span><h2 id="pricing-team-title">团队版</h2><p>{teamPlan.description}</p><Price amount={teamPlan.price} />
              <a className="pricing-plan-action" href={appUrl('/', { open: 'relay', plan: teamPlan.slug, source: 'pricing' })}>获取 CatsCo 团队版</a>
              <dl className="pricing-allowances"><div><dt><Icon name="users" />团队成员</dt><dd>最多 10 人</dd></div><div><dt><Icon name="layers" />Token 额度</dt><dd>{teamPlan.capacity}<span>Token</span></dd></div></dl>
            </div>
            <div className="pricing-team-benefits"><h3>团队协作，从同一个工作空间开始</h3><ul>{teamBenefits.map(item => <li key={item.title}><span className="pricing-benefit-icon"><Icon name={item.icon} /></span><div><strong>{item.title}</strong><p>{item.description}</p></div></li>)}</ul></div>
          </section>
        </div>
        <div id="pricing-enterprise-panel" role="tabpanel" aria-labelledby="pricing-enterprise-tab" hidden={audience !== 'enterprise'} tabIndex={0}>
          <section className="pricing-business" aria-labelledby="business-title">
            <div className="pricing-business-offer"><span className="pricing-kicker">CATSCO FOR BUSINESS</span><h2 id="business-title">让 AI 员工<br />加入你的团队</h2><span className="pricing-business-plan">Business Start · 企业落地服务</span><p>帮助一个团队或一个明确业务场景真正开始使用 AI 员工，从环境准备到首个场景上线。</p>
              <div className="pricing-business-purchase"><div className="pricing-price"><span>¥</span><strong>4,999</strong><small>/ 月起</small></div><a className="pricing-plan-action" href="/contact?topic=enterprise&service=business-start&source=pricing">咨询企业落地<Icon name="arrowRight" /></a></div>
            </div>
            <div className="pricing-business-visual">
              <div className="pricing-business-relief" aria-hidden="true"><div className="pricing-business-paper"><i /><i /><i /></div><div className="pricing-business-flow"><i /><i /><i /></div></div>
              <div className="pricing-business-caption"><span>基础服务范围</span><h3>从准备到上线，一起落地。</h3></div>
              <div className="pricing-business-workspace"><header><span><Icon name="layers" />团队 AI 工作空间</span><span>Business Start</span></header><ul>{businessServices.map(item => <li key={item.title}><span className="pricing-business-service-icon"><Icon name={item.icon} /></span><div><strong>{item.title}</strong><p>{item.description}</p></div><Icon name="check" /></li>)}</ul></div>
              <div className="pricing-business-custom"><strong>高级企业服务需另行咨询</strong><p>多部门流程改造、复杂系统集成、专属数据治理、私有化、安全合规、长期驻场和大规模 Skill 开发，均按范围与交付结果单独确认。</p></div>
            </div>
          </section>
        </div>
        <p className="pricing-estimate-note">Token 数为估算值，实际可用量随所选模型、任务内容及模型价格变化。点击套餐按钮可前往工作台确认购买，实际价格、额度与权益以工作台显示为准。</p>
        <section className="pricing-access" aria-labelledby="pricing-access-title">
          <div><span className="pricing-section-eyebrow">EARLY ACCESS</span><h2 id="pricing-access-title">已有邀请码？</h2><p>邀请码用于内测准入和服务承载管理，不是折扣券，也不会改变正式价格。</p></div>
          <a href={appLoginUrl({ access: 'invite', source: 'pricing', next: '/?open=relay' })}>使用邀请码登录<Icon name="arrowRight" /></a>
        </section>
        <section className="pricing-comparison" aria-labelledby="pricing-comparison-title">
          <header className="pricing-comparison-heading"><span className="pricing-section-eyebrow">PLAN COMPARISON</span><h2 id="pricing-comparison-title">比较不同方案</h2><p>从日常个人工作到团队协作，按用量、持续执行能力和协作需求查看差异。</p></header>
          <p id="pricing-comparison-help" className="pricing-comparison-note">以下为套餐差异参考，实际权益以工作台显示为准。✓ 表示列入方案，— 表示未列入；“待确认”表示权益范围尚未确定。团队版的其他能力仍需单独确认。窄屏可左右滑动查看全部方案。</p>
          <div className="pricing-comparison-scroll" role="region" aria-label="四档方案对比表" aria-describedby="pricing-comparison-help" tabIndex={0}>
            <table className="pricing-comparison-table">
              <caption className="pricing-sr-only">基础版、个人版、专业版和团队版的价格、额度、工作能力与协作资源对比</caption>
              <colgroup><col />{comparisonPlans.map(plan => <col key={plan.slug} />)}</colgroup>
              <thead><tr><th scope="col">功能</th>{comparisonPlans.map(plan => <th key={plan.slug} scope="col"><span>{plan.name}</span></th>)}</tr></thead>
              {comparisonSections.map(section => <tbody key={section.title}>
                <tr className="pricing-comparison-group"><th scope="rowgroup" colSpan={comparisonPlans.length + 1}><span>{section.title}</span></th></tr>
                {section.rows.map(row => <tr key={row.label}><th scope="row">{row.label}</th>{row.values.map((value, index) => <td key={comparisonPlans[index].slug}><ComparisonCell value={value} /></td>)}</tr>)}
              </tbody>)}
            </table>
          </div>
          <p className="pricing-comparison-business">需要从环境准备到首个业务场景上线的支持？<a href="/contact?topic=enterprise&service=business-start&source=pricing">了解 Business Start 企业落地服务<Icon name="arrowRight" /></a><span>按服务范围另行确认，与团队版套餐分开。</span></p>
        </section>
        <section className="pricing-faq" aria-labelledby="pricing-faq-title">
          <header className="pricing-faq-heading"><h2 id="pricing-faq-title">常见问题</h2><button type="button" aria-controls="pricing-faq-list" aria-expanded={allFaqsOpen} onClick={() => setOpenFaqs(allFaqsOpen ? new Set() : new Set(pricingFaqs.map((_, index) => index)))}>{allFaqsOpen ? '全部收起' : '全部展开'}</button></header>
          <div className="pricing-faq-list" id="pricing-faq-list">{pricingFaqs.map((item, index) => <div className="pricing-faq-item" key={item.question}><h3><button id={`pricing-question-${index}`} type="button" aria-expanded={openFaqs.has(index)} aria-controls={`pricing-answer-${index}`} onClick={() => toggleFaq(index)}>{item.question}<span aria-hidden="true">{openFaqs.has(index) ? '−' : '+'}</span></button></h3><div id={`pricing-answer-${index}`} aria-labelledby={`pricing-question-${index}`} hidden={!openFaqs.has(index)}><p>{item.answer}</p></div></div>)}</div>
        </section>
      </section>
    </main>;
}
