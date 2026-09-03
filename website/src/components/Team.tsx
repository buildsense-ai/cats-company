import { useEffect, useRef, useState, type CSSProperties } from 'react'

type TeamPhase = 'title' | 'people' | 'story'
type PurposePhase = 'intro' | 'expanded'

type AvatarStyle = CSSProperties & {
  '--avatar-index': number
  '--avatar-left': string
  '--avatar-top': string
  '--avatar-size': string
  '--avatar-delay': string
  '--avatar-left-mobile': string
  '--avatar-top-mobile': string
  '--avatar-size-mobile': string
  '--avatar-caption-offset'?: string
  '--avatar-caption-offset-mobile'?: string
}

type TeamMember = {
  name: string
  role: string
  position: string
  image?: string
  imageWidth?: number
  imageHeight?: number
  imageScale?: number
  imageOffsetY?: number
  left: number
  top: number
  size: number
  mobileLeft: number
  mobileTop: number
  mobileSize: number
  captionOffset?: number
  mobileCaptionOffset?: number
}

const teamMembers: TeamMember[] = [
  { name: '林益', role: '产品', position: '0% 0%', image: '/team-linyi-v2.webp', imageWidth: 512, imageHeight: 512, imageScale: 1.2, imageOffsetY: 3, left: 12, top: 34, size: 98, mobileLeft: 18, mobileTop: 18, mobileSize: 72 },
  { name: '罗冬阳', role: '团队', position: '33.333% 0%', image: '/team-avatar-luodongyang.webp', imageWidth: 512, imageHeight: 512, imageScale: 1.35, left: 27, top: 21, size: 86, mobileLeft: 50, mobileTop: 16, mobileSize: 64 },
  { name: '朱汉元', role: '团队', position: '66.667% 0%', image: '/team-avatar-zhuhanyuan.webp', imageWidth: 512, imageHeight: 478, imageScale: 1.2, left: 46, top: 33, size: 124, mobileLeft: 78, mobileTop: 19, mobileSize: 84, captionOffset: 4, mobileCaptionOffset: 2 },
  { name: '方快', role: '工程', position: '100% 0%', image: '/team-avatar-fangkuai.webp', imageWidth: 512, imageHeight: 512, imageScale: 1.22, left: 59, top: 20, size: 90, mobileLeft: 17, mobileTop: 38, mobileSize: 66 },
  { name: '钟慧', role: '团队', position: '0% 50%', image: '/team-avatar-zhonghui.webp', imageWidth: 512, imageHeight: 512, imageScale: 1, left: 77, top: 31, size: 96, mobileLeft: 50, mobileTop: 36, mobileSize: 70 },
  { name: '陈永轩', role: '团队', position: '33.333% 50%', image: '/team-avatar-chenyongxuan.webp', imageWidth: 512, imageHeight: 512, imageScale: 1.05, left: 90, top: 46, size: 82, mobileLeft: 83, mobileTop: 39, mobileSize: 64 },
  { name: '陈曦', role: '工程', position: '66.667% 50%', image: '/team-avatar-chenxi.webp', imageWidth: 512, imageHeight: 512, imageScale: 1.3, imageOffsetY: 2, left: 74, top: 57, size: 94, mobileLeft: 18, mobileTop: 58, mobileSize: 74 },
  { name: '陈坤', role: '团队', position: '100% 50%', image: '/team-avatar-chenkun.webp', imageWidth: 512, imageHeight: 512, imageScale: 1.15, left: 56, top: 68, size: 110, mobileLeft: 50, mobileTop: 57, mobileSize: 68 },
  { name: '王荩婧', role: '团队', position: '0% 100%', image: '/team-avatar-wangjinjing.webp', imageWidth: 512, imageHeight: 512, imageScale: 1.5, left: 36, top: 59, size: 88, mobileLeft: 82, mobileTop: 58, mobileSize: 74 },
  { name: '陈大为', role: '团队', position: '33.333% 100%', image: '/team-avatar-chendawei.webp', imageWidth: 512, imageHeight: 512, imageScale: 1.08, left: 18, top: 69, size: 102, mobileLeft: 18, mobileTop: 78, mobileSize: 64 },
  { name: '李冠烨', role: '团队', position: '66.667% 100%', image: '/team-avatar-liguanye-v3.webp', imageWidth: 512, imageHeight: 512, imageScale: 1.7, imageOffsetY: -3, left: 40, top: 79, size: 76, mobileLeft: 50, mobileTop: 77, mobileSize: 76 },
  { name: '杨佳尊', role: '品牌', position: '100% 100%', image: '/team-avatar-custom-v6.webp', imageWidth: 512, imageHeight: 512, imageScale: 1.42, left: 82, top: 77, size: 88, mobileLeft: 82, mobileTop: 79, mobileSize: 66 },
]

export function Team() {
  const purposeSectionRef = useRef<HTMLElement>(null)
  const purposeFrameRef = useRef<number | null>(null)
  const sectionRef = useRef<HTMLElement>(null)
  const frameRef = useRef<number | null>(null)
  const [purposeScrollPhase, setPurposeScrollPhase] = useState<PurposePhase>('intro')
  const [scrollPhase, setScrollPhase] = useState<TeamPhase>('title')
  const [hoverPreview, setHoverPreview] = useState(false)
  const [reducedMotion, setReducedMotion] = useState(false)

  useEffect(() => {
    const motionQuery = window.matchMedia('(prefers-reduced-motion: reduce)')
    const updatePreference = () => setReducedMotion(motionQuery.matches)

    updatePreference()
    motionQuery.addEventListener('change', updatePreference)
    return () => motionQuery.removeEventListener('change', updatePreference)
  }, [])

  useEffect(() => {
    const updatePurposePhase = () => {
      purposeFrameRef.current = null
      const section = purposeSectionRef.current

      if (!section) return

      const rect = section.getBoundingClientRect()
      const travel = Math.max(section.offsetHeight - window.innerHeight, 1)
      const progress = Math.min(1, Math.max(0, -rect.top / travel))
      const nextPhase: PurposePhase = progress < 0.28 ? 'intro' : 'expanded'

      setPurposeScrollPhase((current) => current === nextPhase ? current : nextPhase)
    }

    const requestUpdate = () => {
      if (purposeFrameRef.current === null) {
        purposeFrameRef.current = window.requestAnimationFrame(updatePurposePhase)
      }
    }

    updatePurposePhase()
    window.addEventListener('scroll', requestUpdate, { passive: true })
    window.addEventListener('resize', requestUpdate)

    return () => {
      window.removeEventListener('scroll', requestUpdate)
      window.removeEventListener('resize', requestUpdate)
      if (purposeFrameRef.current !== null) window.cancelAnimationFrame(purposeFrameRef.current)
    }
  }, [])

  useEffect(() => {
    const updatePhase = () => {
      frameRef.current = null
      const section = sectionRef.current

      if (!section) return

      const rect = section.getBoundingClientRect()
      const travel = Math.max(section.offsetHeight - window.innerHeight, 1)
      const progress = Math.min(1, Math.max(0, -rect.top / travel))
      const nextPhase: TeamPhase = progress < 0.24
        ? 'title'
        : progress < 0.68
          ? 'people'
          : 'story'

      setScrollPhase((current) => current === nextPhase ? current : nextPhase)
    }

    const requestUpdate = () => {
      if (frameRef.current === null) {
        frameRef.current = window.requestAnimationFrame(updatePhase)
      }
    }

    updatePhase()
    window.addEventListener('scroll', requestUpdate, { passive: true })
    window.addEventListener('resize', requestUpdate)

    return () => {
      window.removeEventListener('scroll', requestUpdate)
      window.removeEventListener('resize', requestUpdate)
      if (frameRef.current !== null) window.cancelAnimationFrame(frameRef.current)
    }
  }, [])

  const phase = reducedMotion
    ? 'people'
    : scrollPhase === 'title' && hoverPreview
      ? 'people'
      : scrollPhase
  const purposePhase = reducedMotion ? 'expanded' : purposeScrollPhase

  return (
    <>
      <section
        ref={purposeSectionRef}
        id="company-purpose"
        className="team-purpose-section"
        aria-labelledby="team-purpose-title"
      >
        <div className="section-container team-purpose-container">
          <div className="team-purpose-stage" data-phase={purposePhase}>
            <div className="team-purpose-copy">
              <div className="team-purpose-heading">
                <span className="section-kicker">ABOUT CATSCO</span>
                <h2 id="team-purpose-title">我们为什么做 CatsCo</h2>
              </div>
              <div className="team-purpose-statements">
                <p>
                  我们希望 AI 不只是提供建议，而是能在清晰的授权边界内持续推进工作，<br className="team-purpose-desktop-break" />
                  并把可以检查、修改和继续使用的成果交还给用户。
                </p>
                <p>
                  我们希望复杂的技术留在幕后，让不懂技术的人也能从一个清晰目标开始，
                  把工作自然地交出去。
                </p>
                <p>
                  我们希望人把更多时间留给判断、沟通和创造，
                  把重复而重要的执行工作交给 CatsCo。
                </p>
              </div>
            </div>
          </div>
        </div>
      </section>

      <section ref={sectionRef} id="team" className="team-section" aria-labelledby="team-title">
        <div className="team-sticky">
          <div
            className="team-stage"
            data-phase={phase}
            onMouseEnter={() => setHoverPreview(true)}
            onMouseLeave={() => setHoverPreview(false)}
          >
            <div className="team-title-wrap">
              <h2 id="team-title" className="team-title">CatsCo</h2>
              <p className="team-story">
                我们是一支由产品、工程、研究与设计共同组成的团队。我们把 AI 放进真实的工作环境里，
                让它理解目标、持续推进，并把清晰、可靠的成果交还给每一位使用者。
              </p>
            </div>

            <div className="team-avatar-field" aria-label="CatsCo 团队成员">
              {teamMembers.map((member, index) => {
                const style: AvatarStyle = {
                  '--avatar-index': index,
                  '--avatar-left': `${member.left}%`,
                  '--avatar-top': `${member.top}%`,
                  '--avatar-size': `${member.size}px`,
                  '--avatar-delay': `${index * 24}ms`,
                  '--avatar-left-mobile': `${member.mobileLeft}%`,
                  '--avatar-top-mobile': `${member.mobileTop}%`,
                  '--avatar-size-mobile': `${member.mobileSize}px`,
                  '--avatar-caption-offset': member.captionOffset ? `${member.captionOffset}px` : undefined,
                  '--avatar-caption-offset-mobile': member.mobileCaptionOffset ? `${member.mobileCaptionOffset}px` : undefined,
                }

                return (
                  <figure
                    className="team-avatar"
                    key={member.name}
                    style={style}
                    tabIndex={phase === 'people' ? 0 : -1}
                    aria-hidden={phase !== 'people'}
                    aria-label={`${member.name}，${member.role}`}
                  >
                    {member.image ? (
                      <span className="team-avatar-photo team-avatar-photo--standalone" aria-hidden="true">
                        <img
                          className="team-avatar-photo-image"
                          src={member.image}
                          alt=""
                          width={member.imageWidth ?? 1413}
                          height={member.imageHeight ?? 1413}
                          style={member.imageScale || member.imageOffsetY
                            ? { transform: `translateY(${member.imageOffsetY ?? 0}%) scale(${member.imageScale ?? 1.22})` }
                            : undefined}
                          loading="lazy"
                          decoding="async"
                        />
                      </span>
                    ) : (
                      <span
                        className="team-avatar-photo"
                        style={{ backgroundPosition: member.position }}
                        aria-hidden="true"
                      />
                    )}
                    <figcaption>{member.name}</figcaption>
                  </figure>
                )
              })}
            </div>
          </div>
        </div>
      </section>
    </>
  )
}
