import { useEffect, useMemo, useState } from 'react'
import { BrandMark, Icon } from './Icons'

const steps = ['理解任务', '查找资料', '分析数据', '生成报告']

export function TaskWorkbench() {
  const [stage, setStage] = useState(0)
  const [reduceMotion, setReduceMotion] = useState(false)

  useEffect(() => {
    const media = window.matchMedia('(prefers-reduced-motion: reduce)')
    const updatePreference = () => {
      setReduceMotion(media.matches)
      if (media.matches) setStage(4)
    }
    updatePreference()
    media.addEventListener('change', updatePreference)
    return () => media.removeEventListener('change', updatePreference)
  }, [])

  useEffect(() => {
    if (reduceMotion) return
    const timeout = window.setTimeout(
      () => setStage((current) => (current >= 4 ? 0 : current + 1)),
      stage >= 4 ? 2700 : 1050,
    )
    return () => window.clearTimeout(timeout)
  }, [stage, reduceMotion])

  const progress = useMemo(() => (stage >= 4 ? 100 : Math.round(((stage + 0.4) / 4) * 100)), [stage])

  return (
    <div id="task-demo" className="task-shell" aria-label="CatsCo 完成任务演示">
      <div className="task-topbar">
        <div className="flex items-center gap-2.5">
          <BrandMark className="!h-6 !w-10" />
          <span className="text-sm font-semibold text-ink">CatsCo 工作台</span>
        </div>
        <div className="flex items-center gap-2 text-xs font-medium text-[#66736E]">
          <span className="h-2 w-2 rounded-full bg-cats-500" />
          已获授权
        </div>
      </div>

      <div className="grid lg:grid-cols-[1.13fr_0.87fr]">
        <div className="border-b border-[#E3EAE7] p-5 sm:p-8 lg:border-b-0 lg:border-r">
          <div className="mb-7">
            <div className="mb-3 flex items-center justify-between gap-4">
              <span className="utility-label">工作目标</span>
              <span className="rounded-full bg-[#EDF7F3] px-2.5 py-1 text-[11px] font-semibold text-cats-700">执行中</span>
            </div>
            <p className="max-w-xl text-[17px] font-medium leading-7 text-ink sm:text-lg">
              “整理这周销售数据，并生成汇报报告”
            </p>
          </div>

          <div className="relative space-y-1" aria-live="polite">
            <div className="task-progress-track" aria-hidden="true">
              <span style={{ transform: `scaleY(${progress / 100})` }} />
            </div>
            {steps.map((step, index) => {
              const complete = stage > index || stage >= 4
              const active = stage === index && stage < 4
              return (
                <div key={step} className={`task-step ${complete ? 'is-complete' : ''} ${active ? 'is-active' : ''}`}>
                  <span className="task-step-icon">
                    {complete ? <Icon name="check" className="h-4 w-4" /> : <span className="h-1.5 w-1.5 rounded-full bg-current" />}
                  </span>
                  <span className="font-medium">{step}</span>
                  <span className="ml-auto text-xs font-medium">
                    {complete ? '完成' : active ? '正在进行' : '等待'}
                  </span>
                </div>
              )
            })}
          </div>
        </div>

        <div className={`delivery-panel ${stage >= 3 ? 'is-preparing' : ''} ${stage >= 4 ? 'is-delivered' : ''}`}>
          <div className="flex items-center justify-between">
            <span className="utility-label">交付结果</span>
            {stage >= 4 && <span className="delivery-status"><Icon name="check" className="h-3.5 w-3.5" />任务完成</span>}
          </div>

          <div className="delivery-document">
            <div className="document-icon"><Icon name="file" className="h-6 w-6" /></div>
            <div className="min-w-0">
              <p className="truncate text-sm font-semibold text-ink">本周销售汇报.pdf</p>
              <p className="mt-1 text-xs text-[#6A7772]">报告 · 已整理</p>
            </div>
            <span className="ml-auto flex h-7 w-7 items-center justify-center rounded-full bg-[#E7F6F0] text-cats-700">
              <Icon name="check" className="h-4 w-4" />
            </span>
          </div>

          <div className="report-preview" aria-hidden="true">
            <div className="h-2 w-1/2 rounded-full bg-[#B9C8C2]" />
            <div className="mt-5 grid grid-cols-3 items-end gap-2">
              <span className="h-10 rounded-sm bg-[#D3E9E0]" />
              <span className="h-16 rounded-sm bg-cats-500" />
              <span className="h-12 rounded-sm bg-[#9DD5C1]" />
            </div>
            <div className="mt-5 space-y-2">
              <span className="block h-1.5 w-full rounded-full bg-[#E1E8E5]" />
              <span className="block h-1.5 w-4/5 rounded-full bg-[#E1E8E5]" />
            </div>
          </div>

          <button className="replay-button" type="button" onClick={() => setStage(0)} aria-label="重新播放任务演示">
            <Icon name="refresh" className="h-3.5 w-3.5" />
            重新演示
          </button>
        </div>
      </div>
    </div>
  )
}
