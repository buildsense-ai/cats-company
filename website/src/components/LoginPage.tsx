import { appLoginUrl, appRegisterUrl } from '../site-links'
import { BrandMark, Icon } from './Icons'

type SelectedContext =
  | { kind: 'plan'; plan: string; price: string; fromPricing: boolean }
  | { kind: 'invite'; label: string; fromPricing: boolean }

const planDetails: Record<string, { name: string; price: string }> = {
  free: { name: 'CatsCo Free', price: '免费' },
  personal: { name: 'CatsCo Pro', price: '¥399/月' },
  pro: { name: 'CatsCo Max', price: '¥799/月' },
}

function getSelectedPlan(): SelectedContext | null {
  const params = new URLSearchParams(window.location.search)
  const plan = params.get('plan') ?? ''
  const billing = params.get('billing') ?? ''
  const access = params.get('access') ?? ''
  const fromPricing = params.get('source') === 'pricing'

  if (access === 'invite' && !plan && !billing) {
    return { kind: 'invite', label: '内测邀请码准入', fromPricing }
  }
  if (!planDetails[plan] || billing !== 'monthly') return null
  return { kind: 'plan', plan: planDetails[plan].name, price: planDetails[plan].price, fromPricing }
}

export function LoginPage() {
  const selectedPlan = getSelectedPlan()
  const params = new URLSearchParams(window.location.search)
  const loginHref = appLoginUrl({
    plan: params.get('plan') || undefined,
    billing: params.get('billing') || undefined,
    access: params.get('access') || undefined,
    source: params.get('source') || 'public-site',
  })
  const registerHref = appRegisterUrl({
    plan: params.get('plan') || undefined,
    billing: params.get('billing') || undefined,
    source: params.get('source') || 'public-site',
  })

  return (
    <div className="login-page">
      <a className="login-skip-link" href="#main-content">跳到登录入口</a>
      <header className="login-header">
        <a className="login-brand" href="/#top" aria-label="返回 CatsCo 首页">
          <BrandMark />
          <span>CatsCo</span>
        </a>
        <a className="login-back" href="/#top">
          <Icon name="arrowRight" className="h-4 w-4 rotate-180" />
          返回首页
        </a>
      </header>

      <main className="login-main" id="main-content">
        <section className="login-card" aria-labelledby="login-title">
          <div className="login-heading">
            <p className="login-kicker">WORKSPACE SIGN-IN</p>
            <h1 id="login-title">继续你的 CatsCo 任务</h1>
            <p className="login-description">登录和注册由 CatsCo 工作台安全处理，完成后即可回到真实工作空间。</p>
          </div>

          {selectedPlan && (
            <div className="login-plan" aria-label={selectedPlan.fromPricing ? '从定价页带入的选择' : '已识别的登录入口'}>
              <span className="login-plan-icon"><Icon name="check" className="h-4 w-4" /></span>
              <span>
                <small>{selectedPlan.fromPricing ? '已从定价页带入' : '已识别入口'}</small>
                <strong>
                  {selectedPlan.kind === 'plan'
                    ? <>{selectedPlan.plan} <em>{selectedPlan.price}</em></>
                    : selectedPlan.label}
                </strong>
              </span>
              <a href="/pricing">修改</a>
            </div>
          )}

          <div className="login-prototype-note">
            <Icon name="shield" className="h-4 w-4" />
            <p><strong>安全跳转：</strong>公开官网不会接收或保存密码，点击后将在 app.catsco.cc 完成登录。</p>
          </div>

          <div className="login-form" role="group" aria-label="CatsCo 账号入口">
            <a className="login-submit" href={loginHref}>
              登录 CatsCo
              <Icon name="arrowRight" className="h-4 w-4" />
            </a>
            <a className="login-register-link" href={registerHref}>还没有账号？立即注册</a>
          </div>
        </section>
      </main>
    </div>
  )
}
