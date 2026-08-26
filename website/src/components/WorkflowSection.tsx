import { useEffect, useRef, useState } from 'react'

type WorkflowIconName = 'goalPath' | 'deviceHandoff' | 'reusableMemory'

function WorkflowIcon({ name }: { name: WorkflowIconName }) {
  return (
    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.4" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
      {name === 'goalPath' && (
        <>
          <circle className="workflow-icon-goal-start" cx="4.5" cy="18" r="1.5" />
          <path className="workflow-icon-goal-route" d="M6 18h4.25a3 3 0 0 0 3-3V9a3 3 0 0 1 3-3H20" />
          <path className="workflow-icon-goal-arrow" d="m17 3 3 3-3 3" />
        </>
      )}
      {name === 'deviceHandoff' && (
        <g className="workflow-icon-cycle">
          <path className="workflow-icon-cycle-arrow workflow-icon-cycle-arrow-one" d="M7.2 5.2A8.2 8.2 0 0 1 17 5.6l1.2.8m0 0-.2-2.1m.2 2.1-2 .6" />
          <path className="workflow-icon-cycle-arrow workflow-icon-cycle-arrow-two" d="M19.4 9.1a8.2 8.2 0 0 1-4.8 9.8l-1.4.5m0 0 1.9 1m-1.9-1 .7-2" />
          <path className="workflow-icon-cycle-arrow workflow-icon-cycle-arrow-three" d="M10 19.5a8.2 8.2 0 0 1-6.3-8l.1-1.4m0 0-1.7 1.3m1.7-1.3 1.8 1.1" />
        </g>
      )}
      {name === 'reusableMemory' && (
        <>
          <path className="workflow-icon-memory-line workflow-icon-memory-line-one" d="M3.5 5h17" pathLength="1" />
          <path className="workflow-icon-memory-line workflow-icon-memory-line-two" d="M3.5 9.7h13.5" pathLength="1" />
          <path className="workflow-icon-memory-line workflow-icon-memory-line-three" d="M3.5 14.3h17" pathLength="1" />
          <path className="workflow-icon-memory-line workflow-icon-memory-line-four" d="M3.5 19h11" pathLength="1" />
        </>
      )}
    </svg>
  )
}

const workflowCards: { title: string; body: string; icon: WorkflowIconName }[] = [
  {
    title: '目标驱动的任务执行',
    body: '你只需要告诉 CatsCo 想完成什么，它会理解目标、整理步骤并调用所需工具，持续推进任务，最终将成果带回当前会话。',
    icon: 'goalPath',
  },
  {
    title: '跨设备的工作连续性',
    body: '无论资料在电脑、服务器还是云端，CatsCo 都能在你授权且在线的环境中继续任务，减少切换设备和传递文件的麻烦。',
    icon: 'deviceHandoff',
  },
  {
    title: '可积累的工作能力',
    body: '过去任务中的重要信息、工具结果和有效方法可以被重新利用，减少重复说明，让相似工作完成得更稳定、更顺畅。',
    icon: 'reusableMemory',
  },
]

export function WorkflowSection() {
  const sectionRef = useRef<HTMLElement>(null)
  const [contentVisible, setContentVisible] = useState(false)

  useEffect(() => {
    const section = sectionRef.current

    if (!section || typeof IntersectionObserver === 'undefined') {
      setContentVisible(true)
      return undefined
    }

    const observer = new IntersectionObserver(
      ([entry]) => {
        if (!entry.isIntersecting) return

        setContentVisible(true)
        observer.disconnect()
      },
      { rootMargin: '0px 0px -34% 0px', threshold: 0.12 },
    )

    observer.observe(section)
    return () => observer.disconnect()
  }, [])

  return (
    <section
      ref={sectionRef}
      id="solutions"
      className="workflow-section"
      aria-labelledby="workflow-title"
      data-content-visible={contentVisible}
    >
      <div className="section-container">
        <div className="workflow-heading">
          <h2 id="workflow-title">让 AI 真正进入工作流程</h2>
          <p>从目标执行、跨设备连接，到工作经验复用，CatsCo 把一次对话变成可持续完成工作的能力。</p>
        </div>

        <div className="workflow-grid">
          {workflowCards.map((card) => (
            <div className="workflow-card-reveal" key={card.title}>
              <article className="workflow-card">
                <div className="workflow-card-head">
                  <span className="workflow-icon">
                    <WorkflowIcon name={card.icon} />
                  </span>
                  <h3>{card.title}</h3>
                </div>
                <p>{card.body}</p>
              </article>
            </div>
          ))}
        </div>
      </div>
    </section>
  )
}
