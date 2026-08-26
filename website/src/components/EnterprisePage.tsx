import { Icon, type IconName } from './Icons'
import { Reveal } from './Reveal'

const startingPoints: { icon: IconName; title: string; description: string; input: string; output: string }[] = [
  {
    icon: 'file',
    title: '形成一份可复核的报告草稿',
    description: '适合从资料来源明确、已有固定模板的周期性汇总工作开始验证。',
    input: '目标、已有资料、报告模板',
    output: '报告草稿、引用资料与待确认项',
  },
  {
    icon: 'layers',
    title: '推进一项重复性办公流程',
    description: '把步骤、责任人与确认点说清楚，再观察 CatsCo 如何持续推进任务。',
    input: '流程步骤、工作边界、负责人',
    output: '过程记录、阶段结果与异常提醒',
  },
  {
    icon: 'search',
    title: '整理分散资料并交付清单',
    description: '在指定资料范围内完成梳理，让团队更快进入判断和复核。',
    input: '已授权资料、分类要求、交付格式',
    output: '资料清单、摘要与需要补充的信息',
  },
]

const controls: { icon: IconName; label: string; title: string; text: string }[] = [
  {
    icon: 'shield',
    label: '授权',
    title: '先确认边界，再开始工作',
    text: '为每类任务明确可使用的工作环境、资料和动作；未被允许的部分不进入执行范围。',
  },
  {
    icon: 'users',
    label: '协作',
    title: '目标、责任人与复核点一致',
    text: '团队围绕同一项任务工作，清楚谁提出目标、谁提供资料、谁确认最终成果。',
  },
  {
    icon: 'file',
    label: '记录',
    title: '过程与交付放在一起看',
    text: '将任务进度、使用资料和最终交付放在同一条工作链路中，便于复盘与交接。',
  },
  {
    icon: 'layers',
    label: '落地',
    title: '先验证一个流程，再决定扩展',
    text: '从边界清楚、价值明确的场景开始，根据真实使用反馈完善后续安排。',
  },
]

const steps = [
  {
    number: '01',
    title: '选择首个工作流程',
    text: '梳理任务目标、现有做法、参与人员，以及希望 CatsCo 交付的具体成果。',
    outcome: '形成试点任务清单',
  },
  {
    number: '02',
    title: '确认授权与责任边界',
    text: '明确可使用的工作环境和资料、不能执行的动作，以及负责复核成果的人。',
    outcome: '形成授权与复核约定',
  },
  {
    number: '03',
    title: '在真实任务中验证',
    text: '用日常工作检查交付质量、协作方式和管理要求，不要求团队一次性改变全部流程。',
    outcome: '获得质量与协作反馈',
  },
  {
    number: '04',
    title: '决定下一阶段范围',
    text: '根据验证结果确认是否扩展到更多成员和场景，并把支持与服务边界写入正式方案。',
    outcome: '确认下一阶段正式方案',
  },
]

const planQuestions = [
  'CatsCo 可以进入哪些工作环境与资料范围？',
  '每类任务由谁授权，哪些动作必须等待人工确认？',
  '团队如何查看任务进度、使用资料和最终交付？',
  '成果由谁复核，确认后才能继续哪些动作？',
  '数据处理、安全能力、部署方式和服务范围如何约定？',
  '试点结束后，依据什么决定继续、调整或扩展？',
]

export function EnterprisePage() {
  return (
    <main id="main-content" className="enterprise-page">
      <section className="enterprise-hero" aria-labelledby="enterprise-title">
        <div className="enterprise-shell enterprise-hero-grid">
          <Reveal>
            <div className="enterprise-hero-copy">
              <span className="page-kicker">CatsCo 企业版</span>
              <h1 id="enterprise-title">
                <span className="enterprise-hero-title-main">让企业任务持续向前推进</span>
                <span className="enterprise-hero-title-control">组织决定它能看什么、做什么，以及由谁确认成果。</span>
              </h1>
              <p>给 CatsCo 一项明确的工作，授权相关环境与资料。它持续推进、记录过程，并把完成的成果交给负责人复核。</p>
              <ol className="enterprise-hero-chain" aria-label="企业任务控制链路">
                <li><span>01</span><div><strong>给出目标</strong><small>明确要完成的工作</small></div></li>
                <li><span>02</span><div><strong>授权执行</strong><small>只进入允许的范围</small></div></li>
                <li><span>03</span><div><strong>收到成果</strong><small>负责人确认下一步</small></div></li>
              </ol>
              <div className="enterprise-actions" aria-label="企业版相关操作">
                <a className="button-primary" href="/contact?topic=enterprise">
                  讨论一个企业场景
                  <Icon name="arrowRight" className="h-4 w-4" />
                </a>
                <a className="button-secondary" href="/#task-demo">查看工作演示</a>
              </div>
              <p className="enterprise-hero-note"><Icon name="check" className="h-4 w-4" />从一个真实场景开始，再根据验证结果决定下一步。</p>
            </div>
          </Reveal>

          <Reveal delay={100}>
            <figure className="enterprise-control-panel" aria-labelledby="enterprise-panel-title">
              <figcaption className="enterprise-control-head">
                <span id="enterprise-panel-title"><Icon name="briefcase" className="h-5 w-5" />企业任务控制示意</span>
                <strong>授权内执行</strong>
              </figcaption>

              <div className="enterprise-control-task">
                <div>
                  <span>试点任务</span>
                  <strong>整理本周经营报告</strong>
                </div>
                <small>工作中</small>
              </div>

              <div className="enterprise-boundary">
                <p><Icon name="lock" className="h-4 w-4" />本次任务的工作边界</p>
                <dl>
                  <div>
                    <dt>可使用</dt>
                    <dd>已选经营数据、报告模板</dd>
                  </div>
                  <div>
                    <dt>可交付</dt>
                    <dd>报告草稿、引用资料清单</dd>
                  </div>
                  <div className="is-restricted">
                    <dt>暂不执行</dt>
                    <dd>对外发送，等待负责人确认</dd>
                  </div>
                </dl>
              </div>

              <ol className="enterprise-control-flow" aria-label="任务当前进度">
                <li className="is-complete">
                  <span><Icon name="check" className="h-4 w-4" /></span>
                  <div><strong>边界已确认</strong><small>负责人：业务运营</small></div>
                </li>
                <li aria-current="step">
                  <span><Icon name="pen" className="h-4 w-4" /></span>
                  <div><strong>正在完成任务</strong><small>仅使用已选资料</small></div>
                </li>
                <li>
                  <span><Icon name="shield" className="h-4 w-4" /></span>
                  <div><strong>等待成果复核</strong><small>确认后再进入下一步</small></div>
                </li>
              </ol>
            </figure>
          </Reveal>
        </div>
      </section>

      <section className="enterprise-section enterprise-starting-points" aria-labelledby="enterprise-starting-title">
        <div className="enterprise-shell">
          <Reveal>
            <div className="enterprise-heading enterprise-heading-left">
              <span className="page-kicker">首个验证方向</span>
              <h2 id="enterprise-starting-title">先选一项结果清楚、能够复核的真实工作。</h2>
              <p>以下内容是讨论试点时可以考虑的起点，不代表未经确认的标准功能或行业方案。</p>
            </div>
          </Reveal>
          <div className="enterprise-starting-grid">
            {startingPoints.map((item, index) => (
              <Reveal key={item.title} delay={index * 60}>
                <article>
                  <div className="enterprise-starting-icon"><Icon name={item.icon} className="h-5 w-5" /></div>
                  <h3>{item.title}</h3>
                  <p>{item.description}</p>
                  <dl>
                    <div><dt>你提供</dt><dd>{item.input}</dd></div>
                    <div><dt>可讨论的交付</dt><dd>{item.output}</dd></div>
                  </dl>
                </article>
              </Reveal>
            ))}
          </div>
        </div>
      </section>

      <section className="enterprise-section enterprise-controls" aria-labelledby="enterprise-control-title">
        <div className="enterprise-shell">
          <Reveal>
            <div className="enterprise-heading">
              <span className="page-kicker">为组织而设计</span>
              <h2 id="enterprise-control-title">企业需要看清的，不只是“能做什么”。</h2>
              <p>更重要的是：谁授权、在哪个范围工作、如何查看进展，以及由谁确认最终成果。</p>
            </div>
          </Reveal>
          <div className="enterprise-control-grid">
            {controls.map((item, index) => (
              <Reveal key={item.title} delay={index * 50}>
                <article>
                  <div className="enterprise-control-card-head">
                    <span><Icon name={item.icon} className="h-5 w-5" /></span>
                    <small>{item.label}</small>
                  </div>
                  <h3>{item.title}</h3>
                  <p>{item.text}</p>
                </article>
              </Reveal>
            ))}
          </div>
        </div>
      </section>

      <section className="enterprise-section enterprise-rollout" aria-labelledby="enterprise-rollout-title">
        <div className="enterprise-shell enterprise-rollout-grid">
          <Reveal>
            <div className="enterprise-heading enterprise-heading-left">
              <span className="page-kicker">落地方式</span>
              <h2 id="enterprise-rollout-title">从一个真实流程开始，把每一步说清楚。</h2>
              <p>先选定范围清楚、结果可判断的任务。团队在真实工作中验证价值与边界，再共同决定是否扩展。</p>
            </div>
          </Reveal>
          <ol className="enterprise-steps">
            {steps.map((step) => (
              <li key={step.number}>
                <span>{step.number}</span>
                <div>
                  <h3>{step.title}</h3>
                  <p>{step.text}</p>
                  <small><Icon name="check" className="h-4 w-4" />{step.outcome}</small>
                </div>
              </li>
            ))}
          </ol>
        </div>
      </section>

      <section className="enterprise-section enterprise-plan" aria-labelledby="enterprise-plan-title">
        <div className="enterprise-shell">
          <div className="enterprise-plan-grid">
            <Reveal>
              <div className="enterprise-heading enterprise-heading-left">
                <span className="page-kicker">正式方案前需要确认</span>
                <h2 id="enterprise-plan-title">把企业真正关心的问题，在开始前逐项说清楚。</h2>
                <p>我们不会用笼统的“企业级”代替具体约定。未确认的安全、数据与服务能力，应在正式方案中明确。</p>
              </div>
            </Reveal>
            <ul className="enterprise-plan-list">
              {planQuestions.map((question, index) => (
                <li key={question}><span>{String(index + 1).padStart(2, '0')}</span><p>{question}</p></li>
              ))}
            </ul>
          </div>
          <p className="enterprise-plan-note"><Icon name="shield" className="h-4 w-4" />最终的数据处理、部署方式、安全能力和服务范围，以双方确认的正式方案为准。</p>
        </div>
      </section>

      <section className="enterprise-cta" aria-labelledby="enterprise-cta-title">
        <div className="enterprise-shell">
          <div className="enterprise-cta-card">
            <div className="enterprise-cta-copy">
              <span className="page-kicker">讨论首个流程</span>
              <h2 id="enterprise-cta-title">带着一项真实工作，和我们一起确认合适的起点。</h2>
              <p>我们会根据实际场景讨论目标、授权边界与复核方式，不用先准备一套完整的改造计划。</p>
            </div>
            <div className="enterprise-cta-action">
              <p>沟通前，可以先准备：</p>
              <ul>
                <li><Icon name="target" className="h-4 w-4" />一项优先工作</li>
                <li><Icon name="lock" className="h-4 w-4" />需要遵守的授权边界</li>
                <li><Icon name="users" className="h-4 w-4" />负责提供资料与复核的人</li>
              </ul>
              <a className="button-primary" href="/contact?topic=enterprise">
                讨论一个企业场景
                <Icon name="arrowRight" className="h-4 w-4" />
              </a>
              <small>当前页面不代表对特定部署、安全认证、服务等级或交付周期的承诺。</small>
            </div>
          </div>
        </div>
      </section>
    </main>
  )
}
