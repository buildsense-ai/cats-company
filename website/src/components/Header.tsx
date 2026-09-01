import { useEffect, useRef, useState } from 'react'
import { appLoginUrl, GITHUB_URL } from '../site-links'
import { Icon } from './Icons'

export function Header() {
  const pathname = window.location.pathname.replace(/\/+$/, '')
  const isHome = pathname === ''
  const homeHref = (anchor: string) => isHome ? anchor : `/${anchor}`
  const [currentHash, setCurrentHash] = useState(() => window.location.hash)
  const [menuOpen, setMenuOpen] = useState(false)
  const menuButtonRef = useRef<HTMLButtonElement>(null)
  const isHomeTop = isHome && (currentHash === '' || currentHash === '#top')
  const isSolutions = isHome && currentHash === '#solutions'
  const navigation = [
    { label: '首页', href: homeHref('#top'), active: isHomeTop },
    { label: '解决方案', href: homeHref('#solutions'), active: isSolutions },
    { label: '定价', href: '/pricing', active: pathname === '/pricing' },
  ]

  useEffect(() => {
    const syncHash = () => setCurrentHash(window.location.hash)
    window.addEventListener('hashchange', syncHash)
    return () => window.removeEventListener('hashchange', syncHash)
  }, [])

  useEffect(() => {
    if (!menuOpen) return

    const closeOnEscape = (event: KeyboardEvent) => {
      if (event.key !== 'Escape') return
      setMenuOpen(false)
      menuButtonRef.current?.focus()
    }

    window.addEventListener('keydown', closeOnEscape)
    return () => window.removeEventListener('keydown', closeOnEscape)
  }, [menuOpen])

  return (
    <header className="site-header">
      <div className="mx-auto flex h-[72px] max-w-[1200px] items-center justify-between px-5 sm:px-8">
        <a href={homeHref('#top')} className="site-header-brand rounded-lg focus:outline-none focus-visible:ring-2 focus-visible:ring-cats-500 focus-visible:ring-offset-4" aria-label="CatsCo 首页">
          <img className="site-header-brand-mark" src="/catsco-brand-mark.webp" alt="" width="256" height="96" />
          <img className="site-header-wordmark" src="/catsco-wordmark.png" alt="" width="512" height="93" />
        </a>

        <nav className="site-primary-nav hidden items-center gap-9 text-sm font-medium text-[#52605B] md:flex" aria-label="主导航">
          {navigation.map((item) => (
            <a className={`nav-link${item.active ? ' is-active' : ''}`} href={item.href} aria-current={item.active ? 'page' : undefined} key={item.label}>{item.label}</a>
          ))}
        </nav>

        <div className="header-actions">
          <a
            className="header-social-link"
            href={GITHUB_URL}
            target="_blank"
            rel="noopener noreferrer"
            aria-label="在 GitHub 查看 CatsCo"
            title="在 GitHub 查看 CatsCo"
          >
            <Icon name="github" className="h-5 w-5" />
          </a>
          <a className="header-login" href={appLoginUrl({ source: 'public-site' })}>
            登录
          </a>
          <a className="header-download" href="/download" aria-label="下载 CatsCo" title="下载 CatsCo">
            <span>下载</span>
          </a>
          <button ref={menuButtonRef} className="mobile-menu-button" type="button" aria-label={menuOpen ? '关闭导航菜单' : '打开导航菜单'} aria-expanded={menuOpen} aria-controls="mobile-navigation" onClick={() => setMenuOpen((open) => !open)}>
            <Icon name={menuOpen ? 'close' : 'menu'} className="h-5 w-5" />
          </button>
        </div>
      </div>
      <nav id="mobile-navigation" className={`mobile-navigation${menuOpen ? ' is-open' : ''}`} aria-label="移动端主导航" aria-hidden={!menuOpen} inert={!menuOpen}>
        {navigation.map((item) => <a href={item.href} aria-current={item.active ? 'page' : undefined} onClick={() => setMenuOpen(false)} key={item.label}>{item.label}</a>)}
        <a href="/contact" onClick={() => setMenuOpen(false)}>联系我们</a>
      </nav>
    </header>
  )
}
