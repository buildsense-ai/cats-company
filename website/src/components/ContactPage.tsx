import '../styles/pages/contact.css'
import '../styles/pages/content.css'
import { useEffect, useMemo, useRef, useState, type FocusEvent, type FormEvent, type PointerEvent as ReactPointerEvent } from 'react'
import { Icon } from './Icons'

type ContactTopic = 'enterprise' | 'download' | 'general'
type ContactField = 'name' | 'email' | 'company' | 'goal'
type ContactErrors = Partial<Record<ContactField, string>>

const topicLabels: Record<ContactTopic, string> = {
  enterprise: 'BUSINESS INQUIRY',
  download: 'DOWNLOAD & RELEASE UPDATES',
  general: 'PRODUCT INQUIRY',
}

const fieldLabels: Record<ContactField, string> = {
  name: '姓名',
  email: '工作邮箱',
  company: '公司名称',
  goal: '希望解决的问题',
}

const teamSizeOptions = ['1–10 人', '11–50 人', '51–200 人', '200 人以上']

function getFieldError(field: ContactField, value: string) {
  if (!value.trim()) return `请填写${fieldLabels[field]}`
  if (field === 'email' && !/^\S+@\S+\.\S+$/.test(value.trim())) {
    return '请输入有效的工作邮箱 例如 name@company.com'
  }
  return ''
}

export function ContactPage() {
  const topic = useMemo<ContactTopic>(() => {
    const requestedTopic = new URLSearchParams(window.location.search).get('topic')
    return requestedTopic === 'enterprise' || requestedTopic === 'download' ? requestedTopic : 'general'
  }, [])
  const formRef = useRef<HTMLFormElement>(null)
  const pendingInvalidFocusRef = useRef<ContactField | null>(null)
  const teamSizeControlRef = useRef<HTMLDivElement>(null)
  const teamSizeTriggerRef = useRef<HTMLButtonElement>(null)
  const teamSizeOptionRefs = useRef<Array<HTMLButtonElement | null>>([])
  const [errors, setErrors] = useState<ContactErrors>({})
  const [message, setMessage] = useState<{ tone: 'error' | 'notice'; text: string } | null>(null)
  const [teamSize, setTeamSize] = useState('')
  const [isTeamSizeOpen, setIsTeamSizeOpen] = useState(false)

  useEffect(() => {
    const field = pendingInvalidFocusRef.current
    if (!field) return undefined

    pendingInvalidFocusRef.current = null
    const frame = window.requestAnimationFrame(() => {
      const firstInvalidControl = formRef.current?.querySelector<HTMLElement>(`[name="${field}"]`)
      firstInvalidControl?.focus({ preventScroll: true })
      firstInvalidControl?.scrollIntoView({ block: 'center', behavior: 'instant' })
    })

    return () => window.cancelAnimationFrame(frame)
  }, [errors])

  useEffect(() => {
    if (!isTeamSizeOpen) return undefined

    const selectedIndex = Math.max(0, teamSizeOptions.indexOf(teamSize))
    const frame = window.requestAnimationFrame(() => teamSizeOptionRefs.current[selectedIndex]?.focus())
    const closeOnOutsidePress = (event: PointerEvent) => {
      if (!teamSizeControlRef.current?.contains(event.target as Node)) setIsTeamSizeOpen(false)
    }
    const closeOnEscape = (event: KeyboardEvent) => {
      if (event.key !== 'Escape') return
      setIsTeamSizeOpen(false)
      teamSizeTriggerRef.current?.focus()
    }

    document.addEventListener('pointerdown', closeOnOutsidePress)
    document.addEventListener('keydown', closeOnEscape)
    return () => {
      window.cancelAnimationFrame(frame)
      document.removeEventListener('pointerdown', closeOnOutsidePress)
      document.removeEventListener('keydown', closeOnEscape)
    }
  }, [isTeamSizeOpen, teamSize])

  function focusTeamSizeOption(index: number) {
    const wrappedIndex = (index + teamSizeOptions.length) % teamSizeOptions.length
    teamSizeOptionRefs.current[wrappedIndex]?.focus()
  }

  function selectTeamSize(value: string) {
    setTeamSize(value)
    setIsTeamSizeOpen(false)
    teamSizeTriggerRef.current?.focus()
  }

  function handleBlur(event: FocusEvent<HTMLFormElement>) {
    const target = event.target
    if (!(target instanceof HTMLInputElement || target instanceof HTMLTextAreaElement)) return
    const field = target.name as ContactField
    if (!(field in fieldLabels)) return

    const error = getFieldError(field, target.value)
    setErrors((current) => {
      const next = { ...current }
      if (error) next[field] = error
      else delete next[field]
      return next
    })
  }

  function handleFieldPointerDown(event: ReactPointerEvent<HTMLFormElement>) {
    const target = event.target
    if (!(target instanceof HTMLInputElement || target instanceof HTMLTextAreaElement)) return
    const field = target.name as ContactField
    if (!(field in fieldLabels)) return
    clearFieldError(field)
  }

  function clearFieldError(field: ContactField) {
    setErrors((current) => {
      if (!current[field]) return current
      const next = { ...current }
      delete next[field]
      return next
    })
    setMessage(null)
  }

  function handleSubmit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    const form = event.currentTarget
    const data = new FormData(form)
    const fields: ContactField[] = ['name', 'email', 'company', 'goal']
    const nextErrors = fields.reduce<ContactErrors>((result, field) => {
      const error = getFieldError(field, String(data.get(field) ?? ''))
      if (error) result[field] = error
      return result
    }, {})
    const firstInvalidField = fields.find((field) => nextErrors[field])

    if (firstInvalidField) {
      pendingInvalidFocusRef.current = firstInvalidField
      setErrors(nextErrors)
      setMessage({ tone: 'error', text: '请检查下方标出的信息。内容仍保留在你的浏览器中，没有被发送。' })
      return
    }

    setErrors({})
    setMessage({
      tone: 'notice',
      text: '填写格式检查完成。当前联系服务尚未接入，你填写的内容没有发送，也没有保存。',
    })
  }

  return (
    <main id="main-content" className="content-page contact-page">
      <div className="content-shell contact-shell contact-grid">
        <section className="contact-copy" aria-labelledby="contact-title">
          <span className="page-kicker">{topicLabels[topic]}</span>
          <h1 id="contact-title">联系我们</h1>
          <p>告诉我们团队目前最希望完成的任务、使用环境和协作要求。正式服务范围会在双方确认后确定。</p>

          <div className="contact-intake" aria-labelledby="contact-intake-title">
            <h2 id="contact-intake-title">交流时，可以从这三件事说起</h2>
            <ol>
              <li>
                <Icon name="check" className="contact-intake-marker" aria-hidden="true" />
                <div><strong>工作目标</strong><p>现在最希望完成哪一项真实任务。</p></div>
              </li>
              <li>
                <Icon name="check" className="contact-intake-marker" aria-hidden="true" />
                <div><strong>使用环境</strong><p>任务需要在哪些已授权环境中推进。</p></div>
              </li>
              <li>
                <Icon name="check" className="contact-intake-marker" aria-hidden="true" />
                <div><strong>交付结果</strong><p>团队最终需要拿到什么成果。</p></div>
              </li>
            </ol>
          </div>

        </section>

        <section className="contact-card" aria-labelledby="contact-form-title">
          <div className="contact-card-intro">
            <h2 id="contact-form-title">我们能帮您做什么？</h2>
          </div>
          <form ref={formRef} className="contact-form-grid" onSubmit={handleSubmit} onBlur={handleBlur} onPointerDownCapture={handleFieldPointerDown} noValidate>
            <input type="hidden" name="topic" value={topic} />
            <div className="form-field contact-field-wide">
              <label htmlFor="contact-name">姓名</label>
              <input id="contact-name" name="name" autoComplete="name" placeholder="例如：林一" required aria-invalid={Boolean(errors.name)} aria-describedby={errors.name ? 'contact-name-error' : undefined} onInput={() => clearFieldError('name')} />
              {errors.name && <p id="contact-name-error" className="form-field-error" role="alert">{errors.name}</p>}
            </div>
            <div className="form-field contact-field-wide">
              <label htmlFor="contact-email">工作邮箱</label>
              <input id="contact-email" name="email" type="email" autoComplete="email" inputMode="email" spellCheck={false} placeholder="name@company.com" required aria-invalid={Boolean(errors.email)} aria-describedby={errors.email ? 'contact-email-error' : undefined} onInput={() => clearFieldError('email')} />
              {errors.email && <p id="contact-email-error" className="form-field-error" role="alert">{errors.email}</p>}
            </div>
            <div className="contact-organization-group contact-field-wide" role="group" aria-labelledby="contact-organization-label">
              <label id="contact-organization-label" className="contact-organization-label" htmlFor="contact-company">团队名称与规模</label>
              <div className="contact-organization-controls">
                <input id="contact-company" name="company" autoComplete="organization" placeholder="输入团队名称" required aria-invalid={Boolean(errors.company)} aria-describedby={errors.company ? 'contact-company-error' : undefined} onInput={() => clearFieldError('company')} />
                <div ref={teamSizeControlRef} className="contact-team-size">
                  <input type="hidden" name="teamSize" value={teamSize} />
                  <button
                    ref={teamSizeTriggerRef}
                    className="contact-team-size-trigger"
                    type="button"
                    aria-label={`选择团队规模${teamSize ? `，当前为 ${teamSize}` : ''}`}
                    aria-haspopup="listbox"
                    aria-expanded={isTeamSizeOpen}
                    aria-controls="contact-size-options"
                    onClick={() => setIsTeamSizeOpen((current) => !current)}
                    onKeyDown={(event) => {
                      if (event.key !== 'ArrowDown' && event.key !== 'ArrowUp') return
                      event.preventDefault()
                      setIsTeamSizeOpen(true)
                    }}
                  >
                    <span>{teamSize || '团队规模'}</span>
                    <svg viewBox="0 0 12 12" aria-hidden="true"><path d="m1 4 5 5 5-5" /></svg>
                  </button>
                  {isTeamSizeOpen && (
                    <div id="contact-size-options" className="contact-team-size-menu" role="listbox" aria-label="选择团队规模">
                      {teamSizeOptions.map((option, index) => (
                        <button
                          key={option}
                          ref={(element) => { teamSizeOptionRefs.current[index] = element }}
                          type="button"
                          role="option"
                          aria-selected={teamSize === option}
                          tabIndex={-1}
                          onClick={() => selectTeamSize(option)}
                          onKeyDown={(event) => {
                            if (event.key === 'ArrowDown') {
                              event.preventDefault()
                              focusTeamSizeOption(index + 1)
                            } else if (event.key === 'ArrowUp') {
                              event.preventDefault()
                              focusTeamSizeOption(index - 1)
                            } else if (event.key === 'Home') {
                              event.preventDefault()
                              focusTeamSizeOption(0)
                            } else if (event.key === 'End') {
                              event.preventDefault()
                              focusTeamSizeOption(teamSizeOptions.length - 1)
                            }
                          }}
                        >
                          <span>{option}</span>
                          {teamSize === option && <Icon name="check" className="h-4 w-4" />}
                        </button>
                      ))}
                    </div>
                  )}
                </div>
              </div>
              {errors.company && <p id="contact-company-error" className="form-field-error" role="alert">{errors.company}</p>}
            </div>
            <div className="form-field contact-field-wide">
              <label htmlFor="contact-goal">希望 CatsCo 帮助完成什么？</label>
              <textarea id="contact-goal" name="goal" rows={6} placeholder="例如：整理每周销售数据并生成一份可复核的报告…" required aria-invalid={Boolean(errors.goal)} aria-describedby={errors.goal ? 'contact-goal-error' : undefined} onInput={() => clearFieldError('goal')} />
              {errors.goal && <p id="contact-goal-error" className="form-field-error" role="alert">{errors.goal}</p>}
            </div>
            <button className="button-primary contact-submit contact-field-wide" type="submit">发送信息</button>
            {message && <p className={`form-message form-message--${message.tone} contact-field-wide`} role={message.tone === 'error' ? 'alert' : 'status'} aria-live="polite">{message.text}</p>}
          </form>
        </section>
      </div>
    </main>
  )
}
