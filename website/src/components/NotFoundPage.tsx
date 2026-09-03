import '../styles/pages/content.css'
export function NotFoundPage() {
  return (
    <main id="main-content" className="content-page not-found-page">
      <div className="content-shell">
        <span className="page-kicker">404</span>
        <h1>这个页面没有找到。</h1>
        <p>链接可能已经移动，或者地址输入有误。你可以返回首页，或继续查看当前公开的信息页面。</p>
        <nav className="not-found-actions" aria-label="404 页面导航">
          <a className="button-primary" href="/">返回 CatsCo 首页</a>
          <a className="button-secondary" href="/pricing">查看方案</a>
        </nav>
        <p className="not-found-help">需要核对上线状态？前往 <a href="/download">下载说明</a> 或 <a href="/contact">联系说明</a>。</p>
      </div>
    </main>
  )
}
