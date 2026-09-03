import type { SVGProps } from 'react'

export type IconName =
  | 'arrowRight'
  | 'briefcase'
  | 'check'
  | 'chevronDown'
  | 'cloud'
  | 'computer'
  | 'download'
  | 'devices'
  | 'file'
  | 'github'
  | 'home'
  | 'lock'
  | 'layers'
  | 'magic'
  | 'menu'
  | 'mobile'
  | 'pen'
  | 'refresh'
  | 'search'
  | 'shield'
  | 'spark'
  | 'trend'
  | 'target'
  | 'users'
  | 'close'

type IconProps = SVGProps<SVGSVGElement> & { name: IconName }

export function Icon({ name, ...props }: IconProps) {
  const common = {
    fill: 'none',
    stroke: 'currentColor',
    strokeWidth: 1.8,
    strokeLinecap: 'round' as const,
    strokeLinejoin: 'round' as const,
  }

  const paths: Record<IconName, React.ReactNode> = {
    arrowRight: <><path d="M5 12h14" /><path d="m14 7 5 5-5 5" /></>,
    briefcase: <><rect x="3" y="7" width="18" height="13" rx="2" /><path d="M8 7V5a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2M3 12h18M10 12v2h4v-2" /></>,
    check: <path d="m5 12 4 4L19 6" />,
    chevronDown: <path d="m7 10 5 5 5-5" />,
    cloud: <path d="M17.5 19H7a5 5 0 1 1 1.5-9.8A6 6 0 0 1 20 12a3.5 3.5 0 0 1-2.5 7Z" />,
    computer: <><rect x="3" y="4" width="18" height="13" rx="2" /><path d="M8 21h8M12 17v4" /></>,
    download: <><path d="M12 3v12" /><path d="m7 10 5 5 5-5" /><path d="M5 21h14" /></>,
    devices: <><rect x="3" y="5" width="13" height="10" rx="1.8" /><path d="M6.5 19.5h6M9.5 15v4.5" /><rect x="17.5" y="8" width="3.5" height="10" rx="1.2" /></>,
    file: <><path d="M14 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8Z" /><path d="M14 2v6h6M8 13h8M8 17h6" /></>,
    github: <path fill="currentColor" stroke="none" d="M12 .5C5.65.5.5 5.65.5 12c0 5.09 3.29 9.4 7.86 10.93.58.11.79-.25.79-.56 0-.28-.01-1.02-.02-2-3.2.69-3.88-1.54-3.88-1.54-.53-1.33-1.31-1.69-1.31-1.69-1.07-.73.08-.72.08-.72 1.18.08 1.8 1.21 1.8 1.21 1.05 1.8 2.75 1.28 3.42.98.11-.76.41-1.28.75-1.58-2.55-.29-5.24-1.28-5.24-5.69 0-1.26.45-2.29 1.2-3.1-.12-.29-.52-1.47.11-3.06 0 0 .98-.31 3.2 1.18a11.1 11.1 0 0 1 5.82 0c2.22-1.5 3.2-1.18 3.2-1.18.63 1.59.23 2.77.11 3.06.75.81 1.2 1.84 1.2 3.1 0 4.42-2.7 5.4-5.27 5.68.42.36.8 1.08.8 2.18 0 1.58-.02 2.85-.02 3.24 0 .31.21.68.8.56A11.51 11.51 0 0 0 23.5 12C23.5 5.65 18.35.5 12 .5Z" />,
    home: <><path d="m3 11 9-8 9 8" /><path d="M5 10v10h14V10M9 20v-6h6v6" /></>,
    lock: <><rect x="4" y="10" width="16" height="11" rx="2" /><path d="M8 10V7a4 4 0 0 1 8 0v3M12 14v3" /></>,
    layers: <><path d="m12 4 7 4-7 4-7-4 7-4Z" /><path d="m5 12 7 4 7-4" /><path d="m5 16 7 4 7-4" /></>,
    magic: <><path d="m15 4 5 5L8 21H3v-5Z" /><path d="m13 6 5 5M5 3v3M3.5 4.5h3M20 16v4M18 18h4" /></>,
    menu: <><path d="M4 7h16M4 12h16M4 17h16" /></>,
    mobile: <><rect x="6" y="2" width="12" height="20" rx="3" /><path d="M10 5h4M11 18h2" /></>,
    pen: <><path d="M12 20h9" /><path d="M16.5 3.5a2.1 2.1 0 0 1 3 3L8 18l-4 1 1-4Z" /></>,
    refresh: <><path d="M20 7v5h-5" /><path d="M4 17v-5h5" /><path d="M18.2 9A7 7 0 0 0 6.4 6.4L4 9M5.8 15A7 7 0 0 0 17.6 17.6L20 15" /></>,
    search: <><circle cx="11" cy="11" r="7" /><path d="m20 20-4-4" /></>,
    shield: <><path d="M12 22s8-4 8-10V5l-8-3-8 3v7c0 6 8 10 8 10Z" /><path d="m9 12 2 2 4-4" /></>,
    spark: <><path d="m12 3-1.2 4.1L7 9l3.8 1.9L12 15l1.2-4.1L17 9l-3.8-1.9Z" /><path d="m5 14-.7 2.3L2 17.5l2.3 1.2L5 21l.7-2.3L8 17.5l-2.3-1.2Z" /></>,
    trend: <><path d="M3 17 9 11l4 4 8-9" /><path d="M15 6h6v6" /></>,
    target: <><circle cx="12" cy="12" r="7.5" /><circle cx="12" cy="12" r="2.5" /><path d="M12 2.5v3M12 18.5v3" /></>,
    users: <><path d="M16 21v-2a4 4 0 0 0-4-4H6a4 4 0 0 0-4 4v2" /><circle cx="9" cy="7" r="4" /><path d="M22 21v-2a4 4 0 0 0-3-3.9M16 3.1a4 4 0 0 1 0 7.8" /></>,
    close: <><path d="m6 6 12 12M18 6 6 18" /></>,
  }

  return (
    <svg viewBox="0 0 24 24" aria-hidden="true" {...common} {...props}>
      {paths[name]}
    </svg>
  )
}

export function BrandMark({ className = '' }: { className?: string }) {
  return (
    <span className={`brand-mark ${className}`} aria-hidden="true">
      <img src="/catsco-logo.webp" alt="" width="5580" height="2093" decoding="async" />
    </span>
  )
}
