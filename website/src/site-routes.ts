export type SitePage =
  | 'home'
  | 'pricing'
  | 'login'
  | 'download'
  | 'contact'
  | 'privacy'
  | 'terms'

export type SiteRoute = {
  page: SitePage
  path: string
  title: string
  description: string
}

export const siteRoutes: SiteRoute[] = [
  {
    page: 'home',
    path: '/',
    title: 'CatsCo — 你的专业 AI 员工',
    description: 'CatsCo 可以进入你授权的工作环境，持续推进真实任务，并把完成的成果交还给你。',
  },
  {
    page: 'pricing',
    path: '/pricing',
    title: '定价 — CatsCo',
    description: '比较 CatsCo Free、Pro、Max 与 Business Start，选择适合个人工作强度或企业启动范围的方案。',
  },
  {
    page: 'login',
    path: '/login',
    title: '登录 — CatsCo',
    description: '登录 CatsCo，继续你的 AI 员工正在推进的任务。',
  },
  {
    page: 'download',
    path: '/download',
    title: '下载 — CatsCo',
    description: '了解 CatsCo 桌面端与 Web 端的可用方式。',
  },
  {
    page: 'contact',
    path: '/contact',
    title: '联系 CatsCo',
    description: '联系 CatsCo 团队，讨论企业使用、产品接入或发布通知。',
  },
  {
    page: 'privacy',
    path: '/privacy',
    title: '隐私政策 — CatsCo',
    description: '了解 CatsCo 隐私政策草案与正式上线前的数据说明范围。',
  },
  {
    page: 'terms',
    path: '/terms',
    title: '使用条款 — CatsCo',
    description: '了解 CatsCo 使用条款草案与正式服务启用前的说明。',
  },
]

export function normalizePathname(pathname: string) {
  const normalized = pathname.replace(/\/{2,}/g, '/').replace(/\/+$/, '')
  return normalized || '/'
}

export function resolveSiteRoute(pathname: string) {
  const normalized = normalizePathname(pathname)
  return siteRoutes.find((route) => route.path === normalized) ?? null
}

export function applyRouteMetadata(route: SiteRoute | null) {
  const title = route?.title ?? '页面未找到 — CatsCo'
  const description = route?.description ?? '你访问的 CatsCo 页面不存在或已经移动。'

  document.title = title
  let meta = document.querySelector<HTMLMetaElement>('meta[name="description"]')
  if (!meta) {
    meta = document.createElement('meta')
    meta.name = 'description'
    document.head.append(meta)
  }
  meta.content = description

  const managedRobots = document.querySelector<HTMLMetaElement>('meta[name="robots"][data-catsco-managed]')
  if (route) {
    managedRobots?.remove()
    return
  }

  const robots = managedRobots ?? document.createElement('meta')
  robots.name = 'robots'
  robots.content = 'noindex, follow'
  robots.dataset.catscoManaged = 'true'
  if (!managedRobots) document.head.append(robots)
}
