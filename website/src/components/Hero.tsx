import { useRef } from 'react'
import { Reveal } from './Reveal'
import { appLoginUrl } from '../site-links'

export function Hero() {
  const heroRef = useRef<HTMLElement>(null)

  const handlePointerMove = (event: React.PointerEvent<HTMLElement>) => {
    if (event.pointerType === 'touch') return
    if (window.matchMedia('(prefers-reduced-motion: reduce)').matches) return

    const hero = heroRef.current
    if (!hero) return

    const bounds = hero.getBoundingClientRect()
    const x = event.clientX - bounds.left
    const y = event.clientY - bounds.top
    const watermark = hero.querySelector<HTMLImageElement>('.hero-watermark')
    const watermarkBounds = watermark?.getBoundingClientRect()
    const centerX = bounds.width / 2
    const centerY = bounds.height / 2
    const shiftX = ((x - centerX) / Math.max(centerX, 1)) * 8
    const shiftY = ((y - centerY) / Math.max(centerY, 1)) * 6

    hero.style.setProperty('--hero-pointer-x', `${watermarkBounds ? event.clientX - watermarkBounds.left : x}px`)
    hero.style.setProperty('--hero-pointer-y', `${watermarkBounds ? event.clientY - watermarkBounds.top : y}px`)
    hero.style.setProperty('--hero-shift-x', `${shiftX.toFixed(2)}px`)
    hero.style.setProperty('--hero-shift-y', `${shiftY.toFixed(2)}px`)
  }

  const handlePointerEnter = () => {
    heroRef.current?.classList.add('is-pointer-active')
  }

  const handlePointerLeave = () => {
    const hero = heroRef.current
    if (!hero) return

    hero.classList.remove('is-pointer-active')
    hero.style.setProperty('--hero-shift-x', '0px')
    hero.style.setProperty('--hero-shift-y', '0px')
  }

  return (
    <section
      ref={heroRef}
      id="top"
      className="hero-section"
      aria-labelledby="hero-title"
      onPointerEnter={handlePointerEnter}
      onPointerMove={handlePointerMove}
      onPointerLeave={handlePointerLeave}
    >
      <img
        className="hero-watermark"
        src="/catsco-logo-mask.png"
        alt=""
        width="5140"
        height="3271"
        aria-hidden="true"
        decoding="async"
      />
      <img
        className="hero-watermark hero-watermark-hover"
        src="/catsco-logo-mask.png"
        alt=""
        width="5140"
        height="3271"
        aria-hidden="true"
        decoding="async"
      />

      <div className="relative z-[1] mx-auto max-w-[1540px] px-5 sm:px-8">
        <div className="hero-intro mx-auto text-center">
          <Reveal delay={50}>
            <h1 id="hero-title" className="hero-title">
              <span className="hero-title-brand">CatsCo</span>
              <span className="hero-title-value">你的专业AI员工</span>
            </h1>
          </Reveal>

          <Reveal delay={110}>
            <p className="hero-lead">
              一个可以进入用户授权工作环境，帮助用户完成真实任务的 AI 员工。
            </p>
          </Reveal>

          <Reveal delay={170}>
            <div className="hero-action">
              <a className="button-primary hero-cta" href={appLoginUrl({ source: 'public-site' })}>
                立刻开始
              </a>
            </div>
          </Reveal>
        </div>
      </div>
    </section>
  )
}
