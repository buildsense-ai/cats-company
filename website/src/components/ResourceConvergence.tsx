import {
  m,
  useMotionValue,
  useMotionValueEvent,
  useReducedMotion,
  useScroll,
  useTransform,
  type MotionValue,
} from 'framer-motion'
import { useRef, type ReactNode } from 'react'

type CapabilityKind =
  | 'spreadsheet'
  | 'document'
  | 'presentation'
  | 'analytics'
  | 'research'
  | 'answer'
  | 'image'
  | 'website'
  | 'code'
  | 'files'
  | 'mail'
  | 'collaboration'

type Capability = {
  title: string
  description: string
  kind: CapabilityKind
  start: [number, number]
  width: number
  depth: 'near' | 'mid' | 'far'
  rotation: number
}

const capabilities: Capability[] = [
  { title: '处理表格', description: '整理与汇总', kind: 'spreadsheet', start: [-470, -245], width: 225, depth: 'near', rotation: -2.1 },
  { title: '撰写文档', description: '邮件、报告、方案', kind: 'document', start: [520, 185], width: 185, depth: 'mid', rotation: 1.8 },
  { title: '制作演示', description: '组织汇报内容', kind: 'presentation', start: [610, -270], width: 150, depth: 'far', rotation: 2.7 },
  { title: '分析数据', description: '发现趋势与问题', kind: 'analytics', start: [-565, 225], width: 185, depth: 'mid', rotation: -2 },
  { title: '搜索研究', description: '查找并整理资料', kind: 'research', start: [395, -220], width: 225, depth: 'near', rotation: 1.7 },
  { title: '回答问题', description: '结合资料给出答案', kind: 'answer', start: [-650, 70], width: 150, depth: 'far', rotation: -2.8 },
  { title: '生成图片', description: '创建视觉素材', kind: 'image', start: [625, -55], width: 185, depth: 'mid', rotation: 1.5 },
  { title: '搭建网站', description: '完成页面与内容', kind: 'website', start: [-610, -270], width: 150, depth: 'far', rotation: -2.9 },
  { title: '编写代码', description: '实现与修复功能', kind: 'code', start: [70, 255], width: 225, depth: 'near', rotation: 0.9 },
  { title: '整理文件', description: '分类与归档资料', kind: 'files', start: [-425, -35], width: 185, depth: 'mid', rotation: -1.4 },
  { title: '处理邮件', description: '提炼重点并回复', kind: 'mail', start: [650, 225], width: 150, depth: 'far', rotation: 2.5 },
  { title: '对话协作', description: '记录并推进任务', kind: 'collaboration', start: [35, -305], width: 150, depth: 'far', rotation: -1 },
]

const depthOpacity = { near: 0.98, mid: 0.86, far: 0.72 } as const
const depthZIndex = { near: 8, mid: 6, far: 4 } as const
const capabilityAspectRatio: Record<CapabilityKind, number> = {
  spreadsheet: 1.52,
  document: 0.94,
  presentation: 1.58,
  analytics: 1.46,
  research: 1.34,
  answer: 1.18,
  image: 1.08,
  website: 1.5,
  code: 1.52,
  files: 1.12,
  mail: 1.4,
  collaboration: 1.28,
}
const cardStart = 0.06
const cardStagger = 0.045
const cardDuration = 0.14

const cardWindows = capabilities.map((_, index) => {
  const start = cardStart + index * cardStagger
  return [start, start + cardDuration] as const
})
const logoRevealEnd = cardWindows[cardWindows.length - 1][1]

function CapabilityVisual({ kind }: { kind: CapabilityKind }) {
  let content: ReactNode

  switch (kind) {
    case 'spreadsheet':
      content = <>
        <rect x="40" y="28" width="160" height="76" rx="8" className="cap-scene-surface" />
        <path d="M40 41h160" className="cap-scene-line" />
        <circle cx="51" cy="35" r="2.5" className="cap-scene-accent" />
        <circle cx="60" cy="35" r="2.5" className="cap-scene-accent-alt" />
        <path d="M40 48h160M72 28v76M112 28v76M154 28v76M40 70h160M40 88h160" className="cap-scene-line" />
        <rect x="74" y="50" width="36" height="18" rx="3" className="cap-scene-accent-soft" />
        <rect x="113" y="71" width="39" height="16" rx="3" className="cap-scene-accent-alt-soft" />
        <path d="M163 83v-9M175 83V62M187 83V53" className="cap-scene-accent-line" />
      </>
      break
    case 'document':
      content = <>
        <rect x="56" y="26" width="112" height="86" rx="9" className="cap-scene-ghost" />
        <rect x="63" y="20" width="114" height="90" rx="9" className="cap-scene-surface" />
        <rect x="79" y="36" width="54" height="7" rx="3.5" className="cap-scene-accent" />
        <path d="M79 55h80M79 65h67M79 75h75M79 85h48" className="cap-scene-line" />
        <rect x="79" y="93" width="32" height="6" rx="3" className="cap-scene-accent-alt-soft" />
        <rect x="137" y="88" width="22" height="10" rx="5" className="cap-scene-accent-soft" />
        <path d="m159 20 18 18h-18Z" className="cap-scene-accent-alt-soft" />
      </>
      break
    case 'presentation':
      content = <>
        <rect x="38" y="17" width="164" height="82" rx="10" className="cap-scene-ghost" />
        <rect x="31" y="24" width="178" height="84" rx="10" className="cap-scene-surface" />
        <rect x="45" y="38" width="68" height="9" rx="4.5" className="cap-scene-accent" />
        <path d="M45 57h67M45 66h48" className="cap-scene-line" />
        <rect x="132" y="43" width="55" height="42" rx="6" className="cap-scene-accent-soft" />
        <path d="M141 75V63M152 75V54M163 75V60M174 75V49" className="cap-scene-accent-line" />
        <circle cx="179" cy="52" r="5" className="cap-scene-accent-alt" />
        <rect x="45" y="86" width="142" height="7" rx="3.5" className="cap-scene-muted" />
      </>
      break
    case 'analytics':
      content = <>
        <path d="M43 54h154M43 73h154M81 30v62M120 30v62M159 30v62" className="cap-scene-grid" />
        <path d="M43 92V36M43 92h154" className="cap-scene-line-strong" />
        <path d="m52 82 28-18 25 7 31-30 23 12 30-28v67H52Z" className="cap-scene-area" />
        <path d="m52 82 28-18 25 7 31-30 23 12 30-28" className="cap-scene-accent-line" />
        <circle cx="80" cy="64" r="4" className="cap-scene-accent-alt" />
        <circle cx="136" cy="41" r="4" className="cap-scene-accent" />
        <circle cx="189" cy="25" r="5" className="cap-scene-accent-alt" />
        <rect x="129" y="74" width="55" height="16" rx="8" className="cap-scene-accent-alt-soft" />
      </>
      break
    case 'research':
      content = <>
        <rect x="33" y="22" width="174" height="24" rx="12" className="cap-scene-surface" />
        <circle cx="49" cy="34" r="6" className="cap-scene-line-strong" />
        <path d="m53 39 5 5" className="cap-scene-line-strong" />
        <path d="M70 34h71" className="cap-scene-line" />
        <rect x="39" y="56" width="162" height="20" rx="6" className="cap-scene-surface" />
        <rect x="39" y="83" width="162" height="20" rx="6" className="cap-scene-surface" />
        <rect x="39" y="56" width="7" height="20" rx="3.5" className="cap-scene-accent" />
        <rect x="39" y="83" width="7" height="20" rx="3.5" className="cap-scene-accent-alt" />
        <rect x="50" y="63" width="48" height="6" rx="3" className="cap-scene-accent" />
        <path d="M107 66h72M50 90h58M116 93h63" className="cap-scene-line" />
        <circle cx="188" cy="34" r="7" className="cap-scene-accent-alt-soft" />
      </>
      break
    case 'answer':
      content = <>
        <rect x="35" y="26" width="82" height="35" rx="12" className="cap-scene-surface" />
        <path d="M55 43h42" className="cap-scene-line" />
        <circle cx="48" cy="43" r="6" className="cap-scene-accent-alt" />
        <rect x="75" y="70" width="132" height="38" rx="12" className="cap-scene-accent-soft" />
        <path d="M91 84h92M91 94h65" className="cap-scene-accent-line" />
        <circle cx="186" cy="95" r="5" className="cap-scene-accent" />
        <circle cx="196" cy="29" r="13" className="cap-scene-accent-alt-soft" />
        <path d="m190 29 4 4 8-9" className="cap-scene-accent-alt-line" />
      </>
      break
    case 'image':
      content = <>
        <rect x="48" y="13" width="151" height="93" rx="12" className="cap-scene-ghost" />
        <rect x="40" y="19" width="160" height="92" rx="12" className="cap-scene-surface" />
        <circle cx="166" cy="46" r="11" className="cap-scene-accent-alt" />
        <path d="m52 96 40-38 25 24 18-15 49 29" className="cap-scene-area" />
        <path d="m52 96 40-38 25 24 18-15 49 29" className="cap-scene-accent-line" />
        <path d="M48 30h18M57 21v18M178 82h16M186 74v16" className="cap-scene-line-strong" />
        <circle cx="70" cy="40" r="4" className="cap-scene-accent" />
      </>
      break
    case 'website':
      content = <>
        <rect x="29" y="21" width="182" height="90" rx="11" className="cap-scene-surface" />
        <path d="M29 39h182" className="cap-scene-line" />
        <circle cx="43" cy="30" r="3" className="cap-scene-accent" />
        <circle cx="53" cy="30" r="3" className="cap-scene-accent-alt" />
        <circle cx="63" cy="30" r="3" className="cap-scene-muted" />
        <rect x="44" y="52" width="75" height="9" rx="4.5" className="cap-scene-accent" />
        <path d="M44 71h62M44 80h51" className="cap-scene-line" />
        <rect x="139" y="51" width="53" height="42" rx="7" className="cap-scene-accent-soft" />
        <path d="m145 85 13-13 8 8 8-10 13 15" className="cap-scene-accent-alt-line" />
        <rect x="44" y="92" width="62" height="10" rx="5" className="cap-scene-accent-alt" />
      </>
      break
    case 'code':
      content = <>
        <rect x="28" y="21" width="184" height="90" rx="11" className="cap-scene-dark" />
        <circle cx="42" cy="33" r="3" className="cap-scene-accent" />
        <circle cx="52" cy="33" r="3" className="cap-scene-accent-alt" />
        <path d="M28 43h184M45 43v68" className="cap-scene-code-divider" />
        <path d="M35 56h4M35 68h4M35 80h4" className="cap-scene-code-line" />
        <path d="M49 52h45M63 65h62M49 78h32M63 91h73" className="cap-scene-code-line" />
        <path d="m159 58 9 9-9 9M148 76l-9-9 9-9" className="cap-scene-accent-alt-line" />
        <circle cx="186" cy="92" r="10" className="cap-scene-accent-alt" />
        <path d="m181 92 4 4 7-8" className="cap-scene-check" />
      </>
      break
    case 'files':
      content = <>
        <path d="M45 34h56l10 10h80v55H45Z" className="cap-scene-ghost" />
        <path d="M34 46h68l11 12h93v48H34Z" className="cap-scene-surface" />
        <path d="M34 58h172" className="cap-scene-line" />
        <rect x="48" y="72" width="42" height="24" rx="6" className="cap-scene-accent-soft" />
        <rect x="99" y="72" width="42" height="24" rx="6" className="cap-scene-accent-alt-soft" />
        <rect x="150" y="72" width="42" height="24" rx="6" className="cap-scene-muted" />
        <path d="M52 38h55" className="cap-scene-accent-line" />
        <path d="M58 79h22M109 79h22M160 79h22" className="cap-scene-line" />
      </>
      break
    case 'mail':
      content = <>
        <rect x="42" y="21" width="164" height="78" rx="11" className="cap-scene-ghost" />
        <rect x="34" y="27" width="172" height="79" rx="11" className="cap-scene-surface" />
        <path d="m35 39 85 55 85-55" className="cap-scene-line-strong" />
        <path d="m35 98 57-45M205 98l-57-45" className="cap-scene-line" />
        <circle cx="184" cy="40" r="15" className="cap-scene-accent-alt" />
        <path d="m177 40 5 5 9-11" className="cap-scene-check" />
        <rect x="52" y="34" width="42" height="7" rx="3.5" className="cap-scene-accent" />
      </>
      break
    case 'collaboration':
      content = <>
        <rect x="33" y="26" width="112" height="33" rx="12" className="cap-scene-surface" />
        <path d="M50 40h75M50 49h49" className="cap-scene-line" />
        <rect x="83" y="67" width="124" height="36" rx="12" className="cap-scene-accent-soft" />
        <path d="M99 81h88M99 91h59" className="cap-scene-accent-line" />
        <circle cx="47" cy="91" r="15" className="cap-scene-accent-alt" />
        <path d="m40 91 5 5 9-11" className="cap-scene-check" />
        <circle cx="168" cy="31" r="11" className="cap-scene-accent" />
        <circle cx="190" cy="31" r="11" className="cap-scene-accent-alt-soft" />
      </>
      break
  }

  return (
    <svg viewBox="0 0 240 130" className="capability-scene" aria-hidden="true">
      <rect x="10" y="8" width="220" height="114" rx="22" className="cap-scene-backdrop" />
      <circle cx="205" cy="22" r="34" className="cap-scene-ambient" />
      {content}
    </svg>
  )
}

function CapabilityCard({
  capability,
  index,
  progress,
  reduceMotion,
}: {
  capability: Capability
  index: number
  progress: MotionValue<number>
  reduceMotion: boolean
}) {
  const [startAt, endAt] = cardWindows[index]
  const shrinkAt = endAt - 0.028
  const initialOpacity = depthOpacity[capability.depth]
  const x = useTransform(progress, [startAt, endAt], [capability.start[0], 0])
  const y = useTransform(progress, [startAt, endAt], [capability.start[1], 0])
  const scale = useTransform(progress, [startAt, shrinkAt, endAt], [1, 0.18, 0])
  const rotate = useTransform(progress, [startAt, endAt], [capability.rotation, 0])
  const aspectRatio = capabilityAspectRatio[capability.kind]
  const height = capability.width / aspectRatio

  return (
    <div className="capability-anchor">
      <m.div
        className="capability-flight"
        style={reduceMotion ? undefined : { x, y, zIndex: depthZIndex[capability.depth] }}
      >
        <div className="capability-drift" data-drift={index % 6}>
          <m.article
            className="capability-card"
            data-depth={capability.depth}
            data-kind={capability.kind}
            style={reduceMotion ? {
              width: capability.width,
              aspectRatio,
              marginLeft: -capability.width / 2,
              marginTop: -height / 2,
              opacity: 0,
              scale: 0.02,
            } : {
              width: capability.width,
              aspectRatio,
              marginLeft: -capability.width / 2,
              marginTop: -height / 2,
              scale,
              opacity: initialOpacity,
              rotate,
            }}
            aria-label={`${capability.title}：${capability.description}`}
          >
            <div className="capability-card-visual"><CapabilityVisual kind={capability.kind} /></div>
            <div className="capability-card-copy">
              <strong>{capability.title}</strong>
              <span>{capability.description}</span>
            </div>
          </m.article>
        </div>
      </m.div>
    </div>
  )
}

export function ResourceConvergenceScene() {
  const sectionRef = useRef<HTMLElement>(null)
  const reduceMotion = useReducedMotion() ?? false
  const { scrollY, scrollYProgress } = useScroll({
    target: sectionRef,
    offset: ['start start', 'end end'],
  })

  const logoFillProgress = useMotionValue(reduceMotion ? 1 : 0)
  const logoFillLockedRef = useRef(false)
  const previousPageYRef = useRef(0)

  useMotionValueEvent(scrollYProgress, 'change', (latest) => {
    if (reduceMotion) {
      logoFillProgress.set(1)
      return
    }

    const pageY = scrollY.get()
    const movingUp = pageY < previousPageYRef.current
    previousPageYRef.current = pageY
    const normalizedFill = Math.max(
      0,
      Math.min(1, (latest - cardStart) / (logoRevealEnd - cardStart)),
    )

    if (!movingUp && normalizedFill >= 1) {
      logoFillLockedRef.current = true
    } else if (movingUp && latest < logoRevealEnd) {
      logoFillLockedRef.current = false
    }

    logoFillProgress.set(logoFillLockedRef.current ? 1 : normalizedFill)
  })

  const logoReveal = useTransform(
    logoFillProgress,
    [0, 1],
    ['inset(100% 0 0 0)', 'inset(0% 0 0 0)'],
  )
  const logoOutlineOpacity = useTransform(
    logoFillProgress,
    [0, 0.9, 1],
    [0.66, 0.66, 0],
  )
  const orbitOpacity = useTransform(scrollYProgress, [0, 0.16, 0.64, 0.78], [0.18, 0.32, 0.2, 0])
  const finalOpacity = useTransform(scrollYProgress, [0.74, 0.86, 1], [0, 1, 1])
  const finalY = useTransform(scrollYProgress, [0.74, 0.88], [20, 0])

  return (
    <section ref={sectionRef} className="resource-scroll-section" aria-labelledby="resource-title">
      <div className="resource-sticky">
        <div className="resource-stage">
          <m.div className="resource-orbit resource-orbit-outer" style={reduceMotion ? { opacity: 0 } : { opacity: orbitOpacity }} aria-hidden="true" />
          <m.div className="resource-orbit resource-orbit-inner" style={reduceMotion ? { opacity: 0 } : { opacity: orbitOpacity }} aria-hidden="true" />

          <div className="resource-motion-layer">
            {capabilities.map((capability, index) => (
              <CapabilityCard
                key={capability.title}
                capability={capability}
                index={index}
                progress={scrollYProgress}
                reduceMotion={reduceMotion}
              />
            ))}
          </div>

          <div className="resource-logo-wrap">
            <div className="resource-logo-only" aria-hidden="true">
              <svg className="resource-logo-filter-defs" width="0" height="0" aria-hidden="true">
                <defs>
                  <filter id="resource-logo-outline-filter" x="-25%" y="-60%" width="150%" height="220%" colorInterpolationFilters="sRGB">
                    <feMorphology in="SourceAlpha" operator="dilate" radius="0.27" result="expanded" />
                    <feMorphology in="SourceAlpha" operator="erode" radius="0.27" result="contracted" />
                    <feComposite in="expanded" in2="contracted" operator="out" result="outline" />
                    <feFlood floodColor="#999999" result="outlineColor" />
                    <feComposite in="outlineColor" in2="outline" operator="in" />
                  </filter>
                </defs>
              </svg>
              <m.img
                className="resource-logo-outline"
                src="/catsco-logo.webp"
                alt=""
                width="2800" height="1050" decoding="async"
                style={reduceMotion ? { opacity: 0 } : { opacity: logoOutlineOpacity }}
              />
              <m.img
                className="resource-logo-color"
                src="/catsco-logo.webp"
                alt=""
                width="2800"
                height="1050"
                decoding="async"
                style={reduceMotion ? { clipPath: 'inset(0% 0 0 0)' } : { clipPath: logoReveal }}
              />
            </div>
          </div>

          <m.div
            className="resource-final-copy"
            style={reduceMotion ? { opacity: 1, y: 0 } : { opacity: finalOpacity, y: finalY }}
          >
            <h2 id="resource-title">One AI employee.</h2>
            <p>Every authorized environment.</p>
          </m.div>
        </div>
      </div>
    </section>
  )
}

export function ResourceConvergence() {
  return null
}
