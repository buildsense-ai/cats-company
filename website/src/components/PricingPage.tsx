import { useState } from 'react'
import { appLoginUrl } from '../site-links'
import { Icon } from './Icons'

type PricingPlan = {
  name: string
  englishName: string
  description: string
  price: number
  capacity: string
  capacityDetail: string
  cta: string
  queryPlan: 'free' | 'personal' | 'pro'
  featureLabel: string
  featured?: boolean
  features: string[]
  additionalFeatures?: string[]
}

const personalFeatures = [
  '云端 CatsCo 与持续会话',
  '在授权设备、文件和工具间执行任务',
  '后台任务与主动协作',
  '个人 Skill 与使用偏好持续沉淀',
  '常规响应优先级',
]

const freeFeatures = [
  '云端 CatsCo 与基础会话',
  '在授权范围内尝试基础任务',
  '查看任务过程与交付结果',
]

const plans: PricingPlan[] = [
  {
    name: 'Free',
    englishName: 'CatsCo Free',
    description: '适合先体验 CatsCo 的基础工作方式。',
    price: 0,
    capacity: '基础体验范围',
    capacityDetail: '正式开放内容与容量仍待确认',
    cta: '选择 Free',
    queryPlan: 'free',
    featureLabel: '包含',
    features: freeFeatures,
  },
  {
    name: 'Pro',
    englishName: 'CatsCo Pro',
    description: '适合将 CatsCo 作为日常个人助手。',
    price: 399,
    capacity: '标准任务容量',
    capacityDetail: '覆盖稳定的日常工作与个人自动化',
    cta: '选择 Pro',
    queryPlan: 'personal',
    featureLabel: '包含',
    features: personalFeatures,
  },
  {
    name: 'Max',
    englishName: 'CatsCo Max',
    description: '适合高频、多任务并行或复杂工作。',
    price: 799,
    capacity: '约 3 倍任务容量',
    capacityDetail: '以 Pro 的正常个人使用强度为参考',
    cta: '选择 Max',
    queryPlan: 'pro',
    featureLabel: '包含',
    featured: true,
    features: personalFeatures,
    additionalFeatures: [
      '从标准容量提升至约 3 倍任务容量',
      '更高并发与后台任务容量',
      '复杂任务优先获得更强执行能力',
      '高峰期更高响应优先级',
      '更宽松的公平使用边界',
    ],
  },
]

const businessServices = [
  '企业使用环境的基础配置、账号与运行检查',
  '面向负责人和实际使用者的启动培训',
  '梳理一个边界清晰、可验证的首批业务场景',
  '初始化约定范围内的首批 Skill',
  '上线初期反馈处理与约定范围内的小幅调整',
]

type ComparisonPlanKey = 'free' | 'personal' | 'pro'
type ComparisonValue = string | boolean

const comparisonPlans: Array<{ key: ComparisonPlanKey; name: string; cta: string; href: string }> = [
  { key: 'free', name: 'Free', cta: '选择 Free', href: appLoginUrl({ plan: 'free', billing: 'monthly', source: 'pricing', next: '/?open=relay&plan=free' }) },
  { key: 'personal', name: 'Pro', cta: '选择 Pro', href: appLoginUrl({ plan: 'personal', billing: 'monthly', source: 'pricing', next: '/?open=relay&plan=personal' }) },
  { key: 'pro', name: 'Max', cta: '选择 Max', href: appLoginUrl({ plan: 'pro', billing: 'monthly', source: 'pricing', next: '/?open=relay&plan=pro' }) },
]

const comparisonSections: Array<{
  title: string
  rows: Array<{ label: string; values: Record<ComparisonPlanKey, ComparisonValue> }>
}> = [
  {
    title: '方案与容量',
    rows: [
      { label: '当前价格', values: { free: '免费', personal: '¥399 / 月', pro: '¥799 / 月' } },
      { label: '适合的工作强度', values: { free: '先体验基础工作方式', personal: '稳定的日常个人工作', pro: '高频、多任务与复杂工作' } },
      { label: '任务容量', values: { free: '基础体验范围（待确认）', personal: '标准任务容量', pro: '约 3 倍任务容量' } },
    ],
  },
  {
    title: '持续工作能力',
    rows: [
      { label: '云端 CatsCo 与持续会话', values: { free: '基础体验', personal: true, pro: true } },
      { label: '在授权设备、文件和工具间执行任务', values: { free: '基础体验', personal: true, pro: true } },
      { label: '后台任务与主动协作', values: { free: false, personal: true, pro: '更高容量' } },
      { label: '个人 Skill 与使用偏好持续沉淀', values: { free: false, personal: true, pro: true } },
    ],
  },
  {
    title: '优先级与扩展',
    rows: [
      { label: '任务并发与后台容量', values: { free: '基础体验', personal: '标准', pro: '更高' } },
      { label: '复杂任务执行能力', values: { free: '基础体验', personal: '标准', pro: '优先获得更强能力' } },
      { label: '高峰期响应优先级', values: { free: '基础体验', personal: '常规', pro: '更高' } },
    ],
  },
]

const pricingFaqs = [
  {
    question: 'Free、Pro 和 Max 的主要区别是什么？',
    answer: 'Free 用于体验 CatsCo 的基础工作方式；Pro 面向稳定的日常个人工作；Max 在 Pro 全部功能之上，提供约 3 倍任务容量、更高并发与更高任务优先级。',
  },
  {
    question: '价格按月还是按年？',
    answer: '当前公开页面只展示月费方案：Free 为免费体验，Pro 为 ¥399 / 月，Max 为 ¥799 / 月。页面尚未公布年付价格或折扣。',
  },
  {
    question: 'Free 包含多少任务容量？',
    answer: 'Free 的具体容量、开放内容和承载方式仍待确认。当前页面不展示数字配额，也不承诺无限使用。',
  },
  {
    question: '选择方案后会立即扣费吗？',
    answer: '不会。选择方案后会先前往 CatsCo 工作台完成登录；支付前会展示套餐、金额和有效期供你确认。',
  },
  {
    question: 'Max 的“约 3 倍任务容量”是什么意思？',
    answer: '它以 Pro 的正常个人使用强度为参照，表示 Max 更适合高频、多任务和复杂工作，不代表固定的对话次数或单项任务数量。',
  },
  {
    question: 'Business Start 与个人方案有什么不同？',
    answer: 'Business Start 面向团队或明确业务场景，包含环境准备、启动培训、首个场景梳理和约定范围内的初始 Skill。复杂集成、私有化、安全治理或长期驻场需要另行咨询，并根据实际范围确认服务内容。',
  },
  {
    question: '现在可以正式购买或升级吗？',
    answer: '可在 CatsCo 工作台查看当前可用套餐、续费和升级选项。具体展示范围以账号和当前销售状态为准。',
  },
]

function ComparisonCell({ value }: { value: ComparisonValue }) {
  if (typeof value === 'boolean') {
    return value
      ? <span className="pricing-comparison-status is-included" aria-label="包含"><Icon name="check" className="h-4 w-4" /></span>
      : <span className="pricing-comparison-status is-unavailable" aria-label="当前方案未承诺">—</span>
  }

  return <span>{value}</span>
}

export function PricingPage() {
  const [openFaqs, setOpenFaqs] = useState<Set<number>>(() => new Set())
  const allFaqsOpen = openFaqs.size === pricingFaqs.length

  function toggleAllFaqs() {
    setOpenFaqs(allFaqsOpen ? new Set() : new Set(pricingFaqs.map((_, index) => index)))
  }

  function handleFaqToggle(index: number, isOpen: boolean) {
    setOpenFaqs((current) => {
      if (current.has(index) === isOpen) return current

      const next = new Set(current)
      if (isOpen) next.add(index)
      else next.delete(index)
      return next
    })
  }

  function toggleFaq(index: number) {
    setOpenFaqs((current) => {
      const next = new Set(current)
      if (next.has(index)) next.delete(index)
      else next.add(index)
      return next
    })
  }

  return (
    <main id="main-content" className="pricing-page">
      <section className="pricing-shell" aria-labelledby="pricing-title">
        <header className="pricing-hero">
          <h1 id="pricing-title">定价</h1>
          <p>从第一次任务，到一支持续工作的 AI 团队。按需要开始，随时升级。</p>
        </header>

        <section className="pricing-consumer" aria-labelledby="consumer-plans-title">
          <div className="pricing-section-heading">
            <div>
              <p>PERSONAL PLANS</p>
              <h2 id="consumer-plans-title">选择适合你的工作强度</h2>
            </div>
          </div>

          <div className="pricing-grid">
            {plans.map((plan) => {
                const params = new URLSearchParams({
                plan: plan.queryPlan,
                billing: 'monthly',
                source: 'pricing',
              })

              return (
                <article className={`pricing-card is-${plan.queryPlan}${plan.featured ? ' is-featured' : ''}`} key={plan.name}>
                  {plan.featured && <span className="pricing-recommended">推荐</span>}

                  <div className="pricing-plan-heading">
                    <p>{plan.englishName}</p>
                    <h3>{plan.name}</h3>
                    <span>{plan.description}</span>
                  </div>

                  <div className="pricing-price">
                    <span className="pricing-currency">¥</span>
                    <strong>{plan.price}</strong>
                    <span className="pricing-period">/ 月</span>
                  </div>

                  <div className="pricing-capacity">
                    <div>
                      <strong>{plan.capacity}</strong>
                      <small>{plan.capacityDetail}</small>
                    </div>
                  </div>

                  <a
                    className="pricing-cta"
                    href={appLoginUrl({ ...Object.fromEntries(params.entries()), next: `/?open=relay&plan=${plan.queryPlan}` })}
                    aria-label={`${plan.cta}，每月 ${plan.price} 元`}
                  >
                    {plan.cta}
                    <Icon name="arrowRight" className="h-4 w-4" />
                  </a>

                  <div className="pricing-feature-divider" />
                  <div className="pricing-feature-groups">
                    <div className="pricing-feature-group is-included">
                      <p className="pricing-feature-label">{plan.featureLabel}</p>
                      <ul className="pricing-features is-included">
                        {plan.features.map((feature) => (
                          <li key={feature}>
                            <span><Icon name="check" className="h-4 w-4" /></span>
                            <p>{feature}</p>
                          </li>
                        ))}
                      </ul>
                    </div>

                    {plan.additionalFeatures && (
                      <div className="pricing-feature-group is-additional">
                        <ul className="pricing-features is-additional">
                          {plan.additionalFeatures.map((feature) => (
                            <li key={feature}>
                              <span><Icon name="check" className="h-4 w-4" /></span>
                              <p>{feature}</p>
                            </li>
                          ))}
                        </ul>
                      </div>
                    )}
                  </div>
                </article>
              )
            })}
          </div>

        </section>

        <section className="pricing-business" aria-labelledby="business-title">
          <div className="pricing-business-intro">
            <p>BUSINESS SERVICE</p>
            <h2 id="business-title">Business Start</h2>
            <p className="pricing-business-summary">帮助一个团队或一个明确业务场景真正开始使用 AI 员工，不是单纯出售企业账号。</p>
            <div className="pricing-business-price">
              <span>¥</span>
              <strong>4,999</strong>
              <small>/ 月起</small>
            </div>
            <a href="/contact?topic=enterprise&service=business-start&source=pricing">
              咨询企业落地
              <Icon name="arrowRight" className="h-4 w-4" />
            </a>
          </div>

          <div className="pricing-business-scope">
            <div>
              <p>CORE SERVICE SCOPE</p>
              <h3>从环境准备到首个场景上线</h3>
            </div>
            <ul>
              {businessServices.map((service) => (
                <li key={service}>
                  <Icon name="check" className="h-4 w-4" />
                  <span>{service}</span>
                </li>
              ))}
            </ul>
            <div className="pricing-business-custom">
              <strong>高级企业服务需另行咨询</strong>
              <p>多部门流程改造、复杂系统集成、专属数据治理、私有化、安全合规、长期驻场和大规模 Skill 开发，均按范围与交付结果单独确认。</p>
            </div>
          </div>
        </section>

        <section className="pricing-access" aria-labelledby="pricing-access-title">
          <div>
            <p>EARLY ACCESS</p>
            <h2 id="pricing-access-title">已有邀请码？</h2>
            <span>邀请码用于内测准入和服务承载管理，不是折扣券，也不会改变正式价格。</span>
          </div>
          <a href={appLoginUrl({ access: 'invite', source: 'pricing', next: '/?open=relay' })}>
            使用邀请码登录
            <Icon name="arrowRight" className="h-4 w-4" />
          </a>
        </section>

        <aside className="pricing-release-note" aria-label="购买说明">
          <Icon name="file" className="h-5 w-5" />
          <p><strong>选择方案后将在 CatsCo 工作台继续。</strong>请先登录，支付前会再次确认套餐、金额和有效期；公开官网不会接收账号密码或创建订单。</p>
        </aside>

        <section className="pricing-comparison" aria-labelledby="pricing-comparison-title">
          <header className="pricing-comparison-heading">
            <p>PLAN COMPARISON</p>
            <h2 id="pricing-comparison-title">比较不同方案</h2>
            <span>按工作强度、持续执行能力和优先级查看差异。Free 的具体开放范围仍待确认。</span>
          </header>

          <div
            className="pricing-comparison-scroll"
            role="region"
            aria-label="Free、Pro 和 Max 功能对比"
            aria-describedby="pricing-comparison-note"
            tabIndex={0}
          >
            <table className="pricing-comparison-table">
              <thead>
                <tr>
                  <th scope="col">功能</th>
                  {comparisonPlans.map((plan) => (
                    <th scope="col" key={plan.key}>
                      <span>{plan.name}</span>
                      <a href={plan.href}>{plan.cta}<Icon name="arrowRight" className="h-3.5 w-3.5" /></a>
                    </th>
                  ))}
                </tr>
              </thead>
              {comparisonSections.map((section) => (
                <tbody key={section.title}>
                  <tr className="pricing-comparison-group">
                    <th scope="rowgroup" colSpan={4}>{section.title}</th>
                  </tr>
                  {section.rows.map((row) => (
                    <tr key={row.label}>
                      <th scope="row">{row.label}</th>
                      {comparisonPlans.map((plan) => (
                        <td key={plan.key}><ComparisonCell value={row.values[plan.key]} /></td>
                      ))}
                    </tr>
                  ))}
                </tbody>
              ))}
            </table>
          </div>
          <p className="pricing-comparison-note" id="pricing-comparison-note">“—”表示当前方案没有作出对应承诺。窄屏设备可横向滑动查看全部方案。</p>
        </section>

        <section className="pricing-faq" aria-labelledby="pricing-faq-title">
          <header className="pricing-faq-heading">
            <h2 id="pricing-faq-title">常见问题</h2>
            <button
              className="pricing-faq-toggle-all"
              type="button"
              aria-controls="pricing-faq-list"
              aria-expanded={allFaqsOpen}
              onClick={toggleAllFaqs}
            >
              {allFaqsOpen ? '全部收起' : '全部展开'}
            </button>
          </header>
          <div className="pricing-faq-list" id="pricing-faq-list">
            {pricingFaqs.map((item, index) => (
              <details
                key={item.question}
                open={openFaqs.has(index)}
                onToggle={(event) => handleFaqToggle(index, event.currentTarget.open)}
              >
                <summary
                  onKeyDown={(event) => {
                    if (event.key !== 'Enter') return
                    event.preventDefault()
                    toggleFaq(index)
                  }}
                >
                  {item.question}
                </summary>
                <div><p>{item.answer}</p></div>
              </details>
            ))}
          </div>
        </section>
      </section>
    </main>
  )
}
