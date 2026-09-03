import { BrandMark } from './Icons'
import { appLoginUrl, GITHUB_URL } from '../site-links'

type FooterLink = {
  label: string
  href?: string
}

const footerGroups: { title: string; links: FooterLink[] }[] = [
  {
    title: '产品',
    links: [
      { label: '产品介绍', href: '#top' },
      { label: '工作方式', href: '#workflows' },
      { label: '价格方案', href: '/pricing' },
    ],
  },
  {
    title: '资源',
    links: [
      { label: '能力概览', href: '#solutions' },
      { label: '产品演示', href: '#task-demo' },
      { label: '开发者文档' },
      { label: 'GitHub', href: GITHUB_URL },
    ],
  },
  {
    title: '公司',
    links: [
      { label: '关于 CatsCo', href: '#company-purpose' },
      { label: '团队介绍', href: '#team' },
      { label: '联系我们', href: '/contact' },
    ],
  },
]

export function Footer() {
  const isHome = window.location.pathname.replace(/\/+$/, '') === ''
  const homeHref = (href: string) => href.startsWith('#') && !isHome ? `/${href}` : href

  return (
    <footer id="developers" className="final-footer">
      <img
        className="final-footer-watermark"
        src="/catsco-logo-mask.webp"
        alt=""
        width="5140"
        height="3271"
        aria-hidden="true"
        loading="lazy"
        decoding="async"
      />

      <div className="final-footer-shell">
        <div className="final-footer-main">
          <div className="final-footer-cta">
            <span className="final-footer-kicker">CATSCO · FROM GOAL TO OUTCOME</span>
            <h2 className="final-footer-home-title">
              <svg
                className="final-footer-home-arrow"
                viewBox="0 0 64 64"
                aria-hidden="true"
              >
                <path d="M20 44 52 12M22 12h30v30" />
              </svg>
              <span>拥抱你的第一位 AI 员工</span>
            </h2>
            <p>从下一项任务开始，让 CatsCo 帮你持续推进并交付成果。</p>

            <div className="final-footer-actions">
              <a className="final-footer-primary" href={appLoginUrl({ source: 'public-site' })}>
                立刻开始
              </a>
              <a className="final-footer-secondary" href={homeHref('#task-demo')}>
                观看演示
              </a>
            </div>
          </div>

          <nav className="final-footer-nav" aria-label="页脚导航">
            {footerGroups.map((group) => (
              <div className="final-footer-group" key={group.title}>
                <h3>{group.title}</h3>
                <div>
                  {group.links.map((link) => link.href ? (
                    <a
                      href={homeHref(link.href)}
                      target={link.href.startsWith('https://') ? '_blank' : undefined}
                      rel={link.href.startsWith('https://') ? 'noopener noreferrer' : undefined}
                      key={link.label}
                    >
                      {link.label}
                    </a>
                  ) : (
                    <span className="final-footer-link-unavailable" key={link.label}>{link.label}</span>
                  ))}
                </div>
              </div>
            ))}
          </nav>
        </div>

        <div className="final-footer-bottom">
          <div className="final-footer-brand">
            <BrandMark />
            <span>CatsCo</span>
          </div>

          <div className="final-footer-legal" aria-label="法律与联系信息">
            <a href="/privacy">隐私政策</a>
            <a href="/terms">使用条款</a>
            <a href="/contact">联系我们</a>
          </div>

          <small className="final-footer-copyright">© 2026 CatsCo</small>
        </div>
      </div>
    </footer>
  )
}
