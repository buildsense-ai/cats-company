export const GITHUB_URL = 'https://github.com/buildsense-ai'

/**
 * The public site deliberately does not implement authentication itself. Keep
 * credentials and payment flows on the same-origin product workspace instead
 * of posting secrets from the marketing domain.
 */
export const APP_BASE_URL = (import.meta.env.VITE_APP_BASE_URL || 'https://app.catsco.cc').replace(/\/+$/, '')

export function appUrl(path: string, params?: Record<string, string | undefined>) {
  const url = new URL(path.replace(/^\/?/, '/'), `${APP_BASE_URL}/`)
  for (const [key, value] of Object.entries(params || {})) {
    if (value) url.searchParams.set(key, value)
  }
  return url.toString()
}

export function appLoginUrl(params?: Record<string, string | undefined>) {
  return appUrl('/login', params)
}

export function appRegisterUrl(params?: Record<string, string | undefined>) {
  return appUrl('/register', params)
}
