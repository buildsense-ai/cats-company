import { useEffect, useRef, useState, type CSSProperties } from 'react'
import { Reveal } from './Reveal'

type DemoScenario = {
  label: string
  title: string
  description: string
  prompt: string
  attachments: Array<{ name: string; type: string }>
  outcome: string
  surface: string
  accent: string
}

const demoScenarios: DemoScenario[] = [
  {
    label: '任务',
    title: '从目标到可编辑清单',
    description: 'CatsCo 从会议记录中提炼行动项，交回一份可以继续编辑的清单。',
    prompt: '整理这份会议记录，提取需要负责人确认的事项，并生成一份可以继续编辑的行动清单。',
    attachments: [{ name: '会议记录', type: 'DOCX' }, { name: '项目背景', type: 'PDF' }],
    outcome: '交付一份可继续编辑的行动清单。',
    surface: '#dcece6',
    accent: '#1a9d7a',
  },
  {
    label: '分析',
    title: '把变化、依据和边界一起交付',
    description: 'CatsCo 会从周报中找出变化明显的项目，说明判断依据，并把数据口径和需要复核的地方一起标出来。',
    prompt: '分析客户运营周报，找出变化明显的项目，说明判断依据，并标注数据口径和结论边界。',
    attachments: [{ name: '销售数据', type: 'XLSX' }, { name: '字段说明', type: 'CSV' }],
    outcome: '交付带有依据和边界的分析结果。',
    surface: '#e4ece9',
    accent: '#11745b',
  },
  {
    label: '文档',
    title: '交回一份可以继续编辑的文件',
    description: 'CatsCo 会把资料整理成结构清晰的文档，保留需要你确认的内容，并提供预览和下载。',
    prompt: '根据项目资料整理一份对外说明文档，结构清晰，并保留需要我确认的内容。',
    attachments: [{ name: '项目资料', type: 'ZIP' }, { name: '写作要求', type: 'DOCX' }],
    outcome: '交付一份结构清晰的 DOCX 文档。',
    surface: '#2a2a29',
    accent: '#9b9b96',
  },
  {
    label: '调研',
    title: '让每个结论都能找到来源',
    description: '围绕一个明确问题整理资料，区分事实和判断，并把来源、引用和结论放在一起交付。',
    prompt: '围绕这些问题整理公开资料，归纳主要观点，并标注每项结论对应的来源。',
    attachments: [{ name: '调研问题', type: 'MD' }, { name: '参考链接', type: 'TXT' }],
    outcome: '交付可核对来源的结论摘要。',
    surface: '#e7ecef',
    accent: '#526f7a',
  },
  {
    label: '演示',
    title: '把结果整理成可以直接讲述的页面',
    description: 'CatsCo 会把报告中的重点组织成演示页面，并将最终文件交回到当前任务中，方便预览和继续使用。',
    prompt: '根据分析报告制作一份项目汇报，突出关键发现、判断依据和下一步行动。',
    attachments: [{ name: '分析报告', type: 'PDF' }, { name: '演示模板', type: 'PPTX' }],
    outcome: '交付一份可以直接讲述的汇报页。',
    surface: '#292a29',
    accent: '#9b9b96',
  },
]

function DemoIcon({ index }: { index: number }) {
  const paths = [
    <path key="task" d="M5 6h14M5 12h9M5 18h12M3 6h.01M3 12h.01M3 18h.01" />,
    <path key="analyze" d="M5 19V9m5 10V5m5 14v-7m5 7V3" />,
    <path key="document" d="M6 3h8l4 4v14H6V3Zm8 0v5h5M9 12h6M9 16h6" />,
    <path key="research" d="m20 20-4.5-4.5M18 10a8 8 0 1 1-16 0 8 8 0 0 1 16 0Z" />,
    <path key="slides" d="M4 4h16v12H4V4Zm4 16 4-4 4 4" />,
  ]

  return <svg viewBox="0 0 24 24" aria-hidden="true">{paths[index]}</svg>
}

function AnalysisPreview() {
  return (
    <figure className="task-demo-analysis-preview" aria-label="销售数据变化分析报告预览">
      <div className="task-demo-analysis-head">
        <div>
          <span>分析结果</span>
          <strong>销售数据变化分析报告</strong>
        </div>
        <small>演示数据 · 8/12–8/18</small>
      </div>

      <div className="task-demo-analysis-metrics">
        <div>
          <span className="task-demo-analysis-metric-icon" aria-hidden="true"><svg viewBox="0 0 20 20"><path d="M4 14 8 10l3 2 5-6" /><path d="M12 6h4v4" /></svg></span>
          <strong>+18.9%</strong>
          <span>可识别 GMV 环比</span>
        </div>
        <div>
          <span className="task-demo-analysis-metric-icon" aria-hidden="true"><svg viewBox="0 0 20 20"><path d="M4 15V9m4 6V5m4 10v-3m4 3V3" /></svg></span>
          <strong>+88.0 万</strong>
          <span>本周增量</span>
        </div>
        <div>
          <span className="task-demo-analysis-metric-icon" aria-hidden="true"><svg viewBox="0 0 20 20"><path d="M4 5h12M4 10h8M4 15h5" /></svg></span>
          <strong>-5.5 分</strong>
          <span>经营分均值</span>
        </div>
      </div>

      <div className="task-demo-analysis-body">
        <div className="task-demo-analysis-list">
          <span className="task-demo-analysis-label">变化最明显的项目</span>
          <div><b>苏豪广州</b><span>+88.56 万</span><i style={{ '--analysis-bar': '100%' } as CSSProperties} /></div>
          <div><b>美发行业</b><span>+88.38 万</span><i style={{ '--analysis-bar': '76%' } as CSSProperties} /></div>
          <div><b>流行美</b><span>-0.75 万</span><i className="is-negative" style={{ '--analysis-bar': '18%' } as CSSProperties} /></div>
        </div>
        <div className="task-demo-analysis-boundary">
          <span className="task-demo-analysis-label">判断边界</span>
          <div className="task-demo-analysis-chart" aria-label="本周可识别 GMV 变化图，单位为万元">
            <div className="task-demo-analysis-chart-bars" aria-hidden="true">
              <i style={{ '--chart-height': '42%' } as CSSProperties} /><i style={{ '--chart-height': '58%' } as CSSProperties} /><i style={{ '--chart-height': '48%' } as CSSProperties} /><i style={{ '--chart-height': '78%' } as CSSProperties} /><i style={{ '--chart-height': '66%' } as CSSProperties} />
            </div>
            <div className="task-demo-analysis-chart-axis"><span>周一</span><span>可识别 GMV · 万元</span><span>周五</span></div>
          </div>
          <p>仅统计可识别 GMV，不等同于全量销售额。</p>
          <small>建议先核实主要客户的金额口径和来源。</small>
        </div>
      </div>

      <figcaption>
        <a className="task-demo-analysis-link" href="/sales-analysis-report.pdf" download>查看完整报告 <span aria-hidden="true">↗</span></a>
      </figcaption>
    </figure>
  )
}
function TaskPreview() {
  return (
    <figure className="task-demo-task-preview" aria-label="新任务准备预览">
      <div className="task-demo-task-head">
        <div><span>任务开始</span><strong>从一个清晰目标开始</strong></div>
        <small>目标已准备好</small>
      </div>
      <div className="task-demo-task-goal">
        <span>当前助手 · 官网演示 · 市场洞察助理</span>
        <p>整理会议记录，提取需要负责人确认的事项，并生成一份可以继续编辑的行动清单。</p>
      </div>
      <div className="task-demo-task-context" aria-label="任务概览">
        <div><strong>2</strong><span>份参考资料</span></div>
        <div><strong>3</strong><span>个执行步骤</span></div>
        <div><strong>DOCX</strong><span>交付格式</span></div>
      </div>
      <div className="task-demo-task-steps" aria-label="任务准备步骤">
        <div className="is-complete"><i>✓</i><span>选择 AI 助手</span><small>已完成</small></div>
        <div className="is-complete"><i>✓</i><span>交代目标与资料</span><small>已完成</small></div>
        <div className="is-active"><i>3</i><span>开始推进任务</span><small>下一步</small></div>
      </div>
      <div className="task-demo-task-output" aria-label="交付预览">
        <div className="task-demo-task-output-head"><span>交付预览</span><small>DOCX · 待确认 1 项</small></div>
        <strong>可继续编辑的行动清单</strong>
        <div className="task-demo-task-output-items"><span>确认主要客户金额口径</span><span>补充负责人和截止时间</span></div>
      </div>
    </figure>
  )
}

function DocumentPreview() {
  return (
    <figure className="task-demo-document-preview" aria-label="可继续编辑的文档预览">
      <div className="task-demo-document-toolbar"><strong>项目对外说明文档</strong><span>DOCX</span></div>
      <div className="task-demo-document-metrics" aria-label="文档概览">
        <div><strong>3</strong><span>个章节</span></div>
        <div><strong>1</strong><span>待确认项</span></div>
        <div><strong>DOCX</strong><span>交付格式</span></div>
      </div>
      <div className="task-demo-document-paper" tabIndex={0} aria-label="文档正文，可滚动查看章节">
        <div className="task-demo-document-kicker-row"><span className="task-demo-document-kicker">项目说明</span><small>3 个章节 · v1.0</small></div>
        <h3>让复杂工作，变得清晰可交付</h3>
        <p>这份说明介绍项目背景、工作方式和交付范围，帮助团队在开始协作前快速建立共同理解。</p>
        <div className="task-demo-document-note"><b>需要确认</b><span>正式上线时间与对外联系人</span></div>
        <div className="task-demo-document-outline" aria-label="文档结构">
          <span><b>01</b>背景</span><span><b>02</b>工作方式</span><span><b>03</b>交付范围</span>
        </div>
        <div className="task-demo-document-table" aria-label="章节交付状态">
          <div><span>章节</span><span>内容摘要</span><span>状态</span></div>
          <div><b>工作方式</b><span>目标、输入资料与确认点</span><em>已补充</em></div>
          <div><b>交付范围</b><span>对外说明、执行边界与联系人</span><em>待确认</em></div>
        </div>
        <div className="task-demo-document-more" aria-label="文档章节预览">
          <article><b>工作方式</b><p>把目标、输入资料和确认点整理成团队可以继续接手的步骤。</p></article>
          <article><b>交付范围</b><p>包含对外说明、执行边界和上线前需要确认的联系人信息。</p></article>
        </div>
      </div>
      <div className="task-demo-document-extras" aria-label="文档交付状态">
        <div><span>最近更新</span><strong>已补充 3 个章节</strong></div>
        <div><span>待确认</span><strong>上线时间 · 联系人</strong></div>
        <div><span>版本</span><strong>v1.0 · DOCX</strong></div>
      </div>
      <div className="task-demo-document-file"><span>项目对外说明文档.docx</span><b>预览</b><b>下载</b></div>
    </figure>
  )
}

function ResearchPreview() {
  return (
    <figure className="task-demo-research-preview" aria-label="研究来源与结论预览">
      <div className="task-demo-research-head"><div><span>研究结果</span><strong>可核对的结论摘要</strong></div><small>6 个来源 · 向下查看</small></div>
      <div className="task-demo-research-scroll" tabIndex={0} aria-label="调研来源与结论详情">
        <div className="task-demo-research-question"><span>研究问题</span><p>不同渠道的客户反馈，分别说明了哪些共性问题？</p></div>
        <div className="task-demo-research-sources">
          <div><i>01</i><strong>行业报告</strong><span>趋势与背景</span><b>支持结论</b></div>
          <div><i>02</i><strong>客户访谈</strong><span>一手反馈</span><b>支持结论</b></div>
          <div><i>03</i><strong>产品文档</strong><span>功能边界</span><b>参考</b></div>
          <div><i>04</i><strong>客服记录</strong><span>常见诉求</span><b>支持结论</b></div>
          <div><i>05</i><strong>帮助中心</strong><span>上手路径</span><b>支持结论</b></div>
          <div><i>06</i><strong>同类产品</strong><span>体验对照</span><b>补充对照</b></div>
        </div>
      </div>
      <div className="task-demo-research-conclusion">
        <div className="task-demo-research-conclusion-copy"><span>结论</span><p>反馈集中在上手路径和交付预期，4 个来源提到同一项改进建议。</p></div>
        <div className="task-demo-research-signal" aria-label="来源共同提及情况">
          <div><span>来源共同提及</span><strong>4 / 6</strong></div>
          <i style={{ '--signal-width': '67%' } as CSSProperties} />
          <small>上手路径 · 交付预期</small>
        </div>
      </div>
    </figure>
  )
}

type PresentationSlide = {
  kicker: string
  title: string
  description: string
  metrics?: Array<{ value: string; label: string }>
  bullets?: string[]
  actions?: Array<{ number: string; title: string; meta: string; status: string }>
}

const presentationSlides: PresentationSlide[] = [
  {
    kicker: '关键发现',
    title: '增长正在发生，但集中在单一来源',
    description: '把数据变化、判断依据和下一步行动放在同一页，方便团队快速对齐。',
    metrics: [{ value: '+18.9%', label: '可识别 GMV 环比' }, { value: '3', label: '下一步行动' }],
  },
  {
    kicker: '判断依据',
    title: '先确认增量来自哪里，再决定是否扩大投入',
    description: '报告将变化拆到客户与行业两个维度，避免把单一来源的增长误判为整体趋势。',
    bullets: ['苏豪广州贡献本周主要增量', '美发行业增量与客户变化重合', '需补充全量销售额口径'],
  },
  {
    kicker: '下一步行动',
    title: '把结论变成团队可以接手的动作',
    description: '每个动作都保留负责人和确认点，汇报结束后可以直接进入执行。',
    actions: [
      { number: '01', title: '核实主要客户金额口径', meta: '销售负责人 · 今天', status: '待确认' },
      { number: '02', title: '补充第二来源的同期数据', meta: '研究团队 · 本周', status: '待补充' },
      { number: '03', title: '下周复盘投入与转化变化', meta: '项目负责人 · 下周一', status: '已排期' },
    ],
  },
]

function PresentationPreview() {
  const [activeSlide, setActiveSlide] = useState(0)
  const slide = presentationSlides[activeSlide]

  return (
    <figure className="task-demo-presentation-preview" aria-label="项目汇报演示预览">
      <div className="task-demo-presentation-rail" role="tablist" aria-label="演示页码">
        {presentationSlides.map((item, index) => (
          <button
            key={item.kicker}
            type="button"
            className={activeSlide === index ? 'is-active' : undefined}
            aria-label={`查看第 ${index + 1} 页：${item.kicker}`}
            aria-selected={activeSlide === index}
            role="tab"
            onClick={() => setActiveSlide(index)}
          >
            <span className="task-demo-presentation-thumb"><b>0{index + 1}</b><small>{item.kicker}</small></span>
          </button>
        ))}
      </div>
      <div className="task-demo-presentation-slide" data-slide={activeSlide}>
        <div className="task-demo-presentation-slide-head"><span>项目汇报 · 0{activeSlide + 1} / 03</span><small>已整理</small></div>
        <div className="task-demo-presentation-slide-content">
          <span>{slide.kicker}</span>
          <h3>{slide.title}</h3>
          <p>{slide.description}</p>
          {slide.metrics ? (
            <div className="task-demo-presentation-metrics">
              {slide.metrics.map(metric => <div key={metric.label}><b>{metric.value}</b><span>{metric.label}</span></div>)}
            </div>
          ) : slide.actions ? (
            <div className="task-demo-presentation-actions" aria-label="下一步行动清单">
              {slide.actions.map(action => <div key={action.number}><b>{action.number}</b><strong>{action.title}</strong><span>{action.meta}</span><em>{action.status}</em></div>)}
            </div>
          ) : (
            <ul className="task-demo-presentation-bullets">
              {slide.bullets?.map(item => <li key={item}>{item}</li>)}
            </ul>
          )}
        </div>
        <div className="task-demo-presentation-slide-footer"><span>依据：销售数据变化分析报告</span><span>{activeSlide === 2 ? '→ 可交接执行' : '→ 下一页'}</span></div>
      </div>
      <div className="task-demo-presentation-file"><span>项目汇报.pptx</span><b>预览</b><b>下载</b></div>
    </figure>
  )
}

export function TaskDemoSection() {
  const headingRef = useRef<HTMLDivElement>(null)
  const [headingVisible, setHeadingVisible] = useState(false)
  const [activeIndex, setActiveIndex] = useState(0)
  const activeScenario = demoScenarios[activeIndex]

  useEffect(() => {
    const heading = headingRef.current
    if (!heading || typeof IntersectionObserver === 'undefined') {
      setHeadingVisible(true)
      return undefined
    }

    const observer = new IntersectionObserver(
      ([entry]) => {
        if (!entry.isIntersecting) return
        setHeadingVisible(true)
        observer.disconnect()
      },
      { rootMargin: '0px 0px -34% 0px', threshold: 0.12 },
    )

    observer.observe(heading)
    return () => observer.disconnect()
  }, [])

  const stageStyle = {
    '--task-demo-surface': activeScenario.surface,
    '--task-demo-accent': activeScenario.accent,
  } as CSSProperties

  return (
    <section id="task-demo" className="task-demo-section" aria-labelledby="task-demo-title">
      <div className="section-container">
        <div ref={headingRef} className="task-demo-heading" data-title-visible={headingVisible}>
          <p>REAL WORKBENCH</p>
          <h2 id="task-demo-title">从目标到结果</h2>
        </div>

        <Reveal delay={90}>
          <div className="task-demo-explorer">
            <div className="task-demo-tabs" role="tablist" aria-label="选择工作类型">
              {demoScenarios.map((scenario, index) => (
                <button
                  key={scenario.label}
                  id={`task-demo-tab-${index}`}
                  type="button"
                  role="tab"
                  aria-selected={activeIndex === index}
                  aria-controls="task-demo-panel"
                  tabIndex={activeIndex === index ? 0 : -1}
                  onClick={() => setActiveIndex(index)}
                  onKeyDown={(event) => {
                    if (event.key !== 'ArrowLeft' && event.key !== 'ArrowRight') return
                    event.preventDefault()
                    const direction = event.key === 'ArrowRight' ? 1 : -1
                    const nextIndex = (activeIndex + direction + demoScenarios.length) % demoScenarios.length
                    setActiveIndex(nextIndex)
                    document.getElementById(`task-demo-tab-${nextIndex}`)?.focus()
                  }}
                >
                  <DemoIcon index={index} />
                  <span>{scenario.label}</span>
                </button>
              ))}
            </div>

            <div
              id="task-demo-panel"
              className="task-demo-stage"
              role="tabpanel"
              aria-labelledby={`task-demo-tab-${activeIndex}`}
              data-scenario={activeScenario.label}
              style={stageStyle}
            >
              {activeIndex === 0 && <TaskPreview />}
              {activeIndex === 1 && <AnalysisPreview />}
              {activeIndex === 2 && <DocumentPreview />}
              {activeIndex === 3 && <ResearchPreview />}
              {activeIndex === 4 && <PresentationPreview />}

              <aside className="task-demo-brief" aria-label={`${activeScenario.label}示例任务`}>
                <div className="task-demo-brief-card task-demo-prompt-card">
                  <span>任务上下文</span>
                  <p>{activeScenario.prompt}</p>
                </div>
                <div className="task-demo-brief-card">
                  <span>输入资料</span>
                  <div className="task-demo-attachments">
                    {activeScenario.attachments.map((attachment) => (
                      <div key={attachment.name}>
                        <strong>{attachment.name}</strong>
                        <small>{attachment.type}</small>
                      </div>
                    ))}
                  </div>
                </div>
                <div className="task-demo-brief-outcome"><span>交付重点</span><p>{activeScenario.outcome}</p></div>
              </aside>
            </div>

            <div className="task-demo-summary" aria-live="polite">
              <h3>{activeScenario.title}</h3>
              <p>{activeScenario.description}</p>
            </div>
          </div>
        </Reveal>
      </div>
    </section>
  )
}
