import { Icon } from './Icons'
import { appLoginUrl } from '../site-links'

const desktopPlatforms = [
  { icon: 'computer' as const, title: 'Windows 桌面端', text: '从 CatsCo 工作台获取当前版本的官方安装包。' },
  { icon: 'computer' as const, title: 'macOS 桌面端', text: '从 CatsCo 工作台获取当前版本的官方安装包。' },
]

export function DownloadPage() {
  return (
    <main id="main-content" className="content-page download-page">
      <div className="content-shell">
        <header className="content-hero">
          <span className="page-kicker">CATSCO DOWNLOAD</span>
          <h1>从真实工作台获取 CatsCo 客户端。</h1>
          <p>桌面端下载由 CatsCo 工作台提供当前发布版本，登录后可以看到与你的设备和版本匹配的下载入口。</p>
        </header>
        <div className="download-disclosure" role="note">
          <Icon name="shield" className="h-5 w-5" />
          <div><strong>安全下载说明</strong><span>请始终从 CatsCo 工作台打开下载入口，避免使用来历不明的安装包。</span></div>
        </div>
        <div className="download-grid">
          {desktopPlatforms.map((platform) => (
            <article key={platform.title}>
              <div className="download-card-top">
                <span className="download-icon"><Icon name={platform.icon} className="h-6 w-6" /></span>
                <span className="download-status">登录后可用</span>
              </div>
              <h2>{platform.title}</h2>
              <p>{platform.text}</p>
              <p className="download-availability">当前状态：前往工作台下载。</p>
              <a href={appLoginUrl({ source: 'public-site', next: '/?open=download' })}>打开 CatsCo 工作台</a>
            </article>
          ))}
          <article className="is-entry">
            <div className="download-card-top">
              <span className="download-icon"><Icon name="cloud" className="h-6 w-6" /></span>
              <span className="download-status">可用</span>
            </div>
            <h2>Web 工作台</h2>
            <p>无需安装，登录后即可直接使用 CatsCo 的真实工作空间。</p>
            <p className="download-availability">当前状态：可登录。</p>
            <a href={appLoginUrl({ source: 'public-site' })}>登录 CatsCo 工作台</a>
          </article>
        </div>
      </div>
    </main>
  )
}
