import React, { useId, useMemo, useRef, useState, useEffect } from 'react';
import { InlineFeedback } from './feedback-system';
import './identity-onboarding.css';

const CAT_PALETTES = [
  { body: '#62c9a8', ear: '#f4a6a0', detail: '#207f6c' },
  { body: '#9b8ed8', ear: '#f3aaa8', detail: '#6256a8' },
  { body: '#f1a36f', ear: '#ffd0b0', detail: '#a85f35' },
  { body: '#70b7d4', ear: '#f3a9b6', detail: '#317b99' },
  { body: '#e3bd5e', ear: '#f3a4a4', detail: '#9a7420' },
  { body: '#d47f9d', ear: '#ffd0df', detail: '#8a3f61' },
  { body: '#34456f', ear: '#ff71ad', detail: '#3ee6cf' },
  { body: '#50396f', ear: '#f6a1ff', detail: '#56dff5' },
  { body: '#465064', ear: '#ff8ca8', detail: '#c9ee58' },
];
const BRAND_GREEN = '#29bc95';
const EYE_COLORS = ['#202a35', '#315b68', '#4b3f72', '#754b38'];
const GLASSES_COLORS = ['#7b6bd6', '#e07a5f', '#3f8fba', '#d39b45'];

function hashName(value) {
  return Array.from(value || 'CatsCo').reduce(
    (hash, character) => ((hash * 33) ^ character.codePointAt(0)) >>> 0,
    5381,
  );
}

function mixHash(value) {
  let mixed = (value ^ (value >>> 16)) >>> 0;
  mixed = Math.imul(mixed, 0x7feb352d) >>> 0;
  return (mixed ^ (mixed >>> 15)) >>> 0;
}

function colorLuminance(hex) {
  const channels = hex.slice(1).match(/.{2}/g).map((value) => parseInt(value, 16) / 255);
  const [red, green, blue] = channels.map((value) => (
    value <= 0.04045 ? value / 12.92 : ((value + 0.055) / 1.055) ** 2.4
  ));
  return (0.2126 * red) + (0.7152 * green) + (0.0722 * blue);
}

function catTraits(name) {
  const hash = hashName(name.trim().toLocaleLowerCase());
  const variantHash = mixHash(hash);
  return {
    hash,
    palette: CAT_PALETTES[hash % CAT_PALETTES.length],
    headTilt: ((hash >> 3) % 9) - 4,
    eyeGap: 18 + ((hash >> 6) % 8),
    eyeSize: 5 + ((hash >> 10) % 3),
    bodyWidth: 70 + ((hash >> 13) % 18),
    earLift: (hash >> 17) % 8,
    smile: 5 + ((hash >> 20) % 5),
    tailSide: (hash >> 23) % 2 === 0 ? 1 : -1,
    whiskerLift: ((hash >> 25) % 7) - 3,
    bodyTilt: ((hash >> 28) % 7) - 3,
    eyeColor: EYE_COLORS[(hash >>> 29) % EYE_COLORS.length],
    glassesColor: GLASSES_COLORS[(hash >>> 30) % GLASSES_COLORS.length],
    wearsGlasses: variantHash % 5 === 0,
  };
}

export function IdentityCat({ name, className = '' }) {
  const filterId = useId().replace(/:/g, '');
  const traits = useMemo(() => catTraits(name), [name]);
  const {
    hash, palette, headTilt, eyeGap, eyeSize, bodyWidth, earLift, smile, tailSide, whiskerLift,
    bodyTilt, eyeColor, glassesColor, wearsGlasses,
  } = traits;
  const bodyLeft = 140 - (bodyWidth / 2);
  const bodyRight = 140 + (bodyWidth / 2);
  const bellyFill = colorLuminance(palette.body) < 0.28
    ? 'rgba(255, 255, 255, 0.24)'
    : 'rgba(255, 255, 255, 0.13)';
  const blinkDelay = 1.8 + ((hash >>> 12) % 24) / 10;
  const blinkDuration = 4.6 + ((hash >>> 18) % 18) / 10;
  const tailDuration = 2.2 + ((hash >>> 22) % 7) / 10;

  return (
    <svg
      className={['cc-identity-cat', className].filter(Boolean).join(' ')}
      viewBox="0 0 280 300"
      role="img"
      aria-label={name.trim() ? `${name.trim()} 的 CatsCo 小猫` : '等待名字的 CatsCo 小猫'}
      data-identity-seed={hash}
    >
      <defs>
        <filter id={filterId} x="-12%" y="-12%" width="124%" height="124%">
          <feTurbulence type="fractalNoise" baseFrequency="0.015" numOctaves="2" seed={hash % 31} result="noise" />
          <feDisplacementMap in="SourceGraphic" in2="noise" scale="1.2" />
        </filter>
      </defs>
      <g key={hash} className="cc-identity-cat__morph" filter={`url(#${filterId})`}>
        <ellipse className="cc-identity-cat__shadow" cx="140" cy="270" rx="72" ry="7" />
        <g transform={`rotate(${bodyTilt} 140 218)`}>
          <g
            className="cc-identity-cat__tail-motion"
            style={{
              '--identity-tail-duration': `${tailDuration}s`,
              transformOrigin: `${tailSide > 0 ? bodyRight - 5 : bodyLeft + 5}px 213px`,
            }}
          >
            <path
              className="cc-identity-cat__tail"
              d={tailSide > 0
                ? `M ${bodyRight - 5} 213 C ${bodyRight + 47} 206, ${bodyRight + 53} 248, ${bodyRight + 17} 252 C ${bodyRight} 254, ${bodyRight} 238, ${bodyRight + 16} 237`
                : `M ${bodyLeft + 5} 213 C ${bodyLeft - 47} 206, ${bodyLeft - 53} 248, ${bodyLeft - 17} 252 C ${bodyLeft} 254, ${bodyLeft} 238, ${bodyLeft - 16} 237`}
              style={{ stroke: palette.detail }}
            />
          </g>
          <ellipse
            cx="140"
            cy="218"
            rx={bodyWidth / 2}
            ry="52"
            fill={palette.body}
            className="cc-identity-cat__body"
          />
          <path
            className="cc-identity-cat__belly"
            d="M119 189 C104 221 113 254 140 263 C167 254 176 221 161 189"
            style={{ fill: bellyFill }}
          />
          <g className="cc-identity-cat__brand-badge">
            <g transform="rotate(3 142 217)">
              <path d="M140 192 L142 211" />
              <rect x="132" y="209" width="20" height="17" rx="3" fill={BRAND_GREEN} />
              <path d="M137 216 H147 M137 220 H143" />
            </g>
          </g>
          <path
            d={`M${bodyLeft + 4} 211 C${bodyLeft - 7} 219 ${bodyLeft - 9} 237 ${bodyLeft} 245`}
            className="cc-identity-cat__arm"
          />
          <path
            d={`M${bodyRight - 4} 211 C${bodyRight + 7} 219 ${bodyRight + 9} 237 ${bodyRight} 245`}
            className="cc-identity-cat__arm"
          />
        </g>
        <g transform={`rotate(${headTilt} 140 125)`}>
          <path
            d={`M84 ${86 - earLift} C78 54, 93 40, 116 70 C130 63, 150 63, 164 70 C187 40, 202 54, 196 ${86 - earLift} C213 103, 210 143, 190 160 C169 178, 111 178, 90 160 C70 143, 67 103, 84 ${86 - earLift} Z`}
            fill={palette.body}
            className="cc-identity-cat__head"
          />
          <path d={`M89 ${78 - earLift} C88 61, 96 55, 108 72`} fill={palette.ear} className="cc-identity-cat__inner-ear" />
          <path d={`M171 72 C184 55, 192 61, 191 ${78 - earLift}`} fill={palette.ear} className="cc-identity-cat__inner-ear" />
          <g
            className="cc-identity-cat__eyes"
            style={{
              '--identity-blink-delay': `${blinkDelay}s`,
              '--identity-blink-duration': `${blinkDuration}s`,
            }}
          >
            <g className="cc-identity-cat__eye-group">
              <circle cx={140 - eyeGap} cy="118" r={eyeSize + 4} className="cc-identity-cat__eye-white" />
              <circle cx={141 - eyeGap} cy="119" r={eyeSize} className="cc-identity-cat__eye" style={{ fill: eyeColor }} />
              <circle cx={143 - eyeGap} cy="117" r="1.8" className="cc-identity-cat__eye-shine" />
            </g>
            <g className="cc-identity-cat__eye-group">
              <circle cx={140 + eyeGap} cy="118" r={eyeSize + 4} className="cc-identity-cat__eye-white" />
              <circle cx={139 + eyeGap} cy="119" r={eyeSize} className="cc-identity-cat__eye" style={{ fill: eyeColor }} />
              <circle cx={141 + eyeGap} cy="117" r="1.8" className="cc-identity-cat__eye-shine" />
            </g>
          </g>
          {wearsGlasses && (
            <g
              className="cc-identity-cat__brand-glasses"
              style={{ stroke: 'var(--identity-ink)', fill: glassesColor, fillOpacity: 0.72 }}
            >
              <rect
                x={140 - eyeGap - 15}
                y="110"
                width="30"
                height="18"
                rx="8"
                transform={`rotate(-3 ${140 - eyeGap} 119)`}
              />
              <rect
                x={140 + eyeGap - 15}
                y="110"
                width="30"
                height="18"
                rx="8"
                transform={`rotate(3 ${140 + eyeGap} 119)`}
              />
              <path className="cc-identity-cat__brand-glasses-arm" d={`M${125 - eyeGap} 116 L${117 - eyeGap} 114 M${155 + eyeGap} 116 L${163 + eyeGap} 114`} />
              <ellipse className="cc-identity-cat__brand-glasses-glint" cx={132 - eyeGap} cy="114" rx="3.2" ry="1.7" transform={`rotate(-18 ${132 - eyeGap} 114)`} />
              <ellipse className="cc-identity-cat__brand-glasses-glint" cx={132 + eyeGap} cy="114" rx="3.2" ry="1.7" transform={`rotate(-18 ${132 + eyeGap} 114)`} />
              <path className="cc-identity-cat__brand-glasses-bridge" d="M126 117 Q140 113 154 117" />
            </g>
          )}
          <path d="M135 136 Q140 140 145 136 Q140 132 135 136" fill={palette.ear} className="cc-identity-cat__nose" />
          <path d={`M140 140 C139 147, ${140 - smile} 151, ${132 - smile} 146 M140 140 C141 147, ${140 + smile} 151, ${148 + smile} 146`} className="cc-identity-cat__mouth" />
          <path d={`M126 ${141 + whiskerLift} L83 ${135 + whiskerLift} M125 ${148 + whiskerLift} L80 ${153 + whiskerLift} M154 ${141 - whiskerLift} L197 ${135 - whiskerLift} M155 ${148 - whiskerLift} L200 ${153 - whiskerLift}`} className="cc-identity-cat__whiskers" />
          <path d="M117 77 Q124 57 133 73 M133 74 Q141 51 149 74 M150 76 Q158 61 165 80" className="cc-identity-cat__tufts" />
        </g>
        <path d="M124 264 L121 275 M156 264 L159 275" className="cc-identity-cat__leg" />
      </g>
    </svg>
  );
}

export default function IdentityOnboarding({ initialName = '', onComplete }) {
  const [name, setName] = useState(initialName);
  const [error, setError] = useState('');
  const [submitting, setSubmitting] = useState(false);
  const inputRef = useRef(null);

  useEffect(() => {
    if (window.matchMedia?.('(pointer: fine)').matches) inputRef.current?.focus();
  }, []);

  const handleSubmit = async (event) => {
    event.preventDefault();
    const displayName = name.trim();
    if (!displayName) {
      setError('请输入一个名字');
      inputRef.current?.focus();
      return;
    }
    setSubmitting(true);
    setError('');
    try {
      await onComplete(displayName);
    } catch (submitError) {
      setError(submitError?.message || '名字保存失败，请重试');
      setSubmitting(false);
    }
  };

  return (
    <main className="cc-identity-onboarding">
      <div className="cc-identity-onboarding__brand" translate="no">
        <span className="catsco-brand-mark" aria-hidden="true" />
        <span className="catsco-brand-name">CatsCo</span>
      </div>

      <section className="cc-identity-onboarding__content" aria-labelledby="identity-onboarding-title">
        <header className="cc-identity-onboarding__intro">
          <h1 id="identity-onboarding-title">怎么称呼你？</h1>
        </header>
        <div className="cc-identity-onboarding__stage">
          <IdentityCat name={name} />
        </div>

        <form className="cc-identity-onboarding__form" onSubmit={handleSubmit}>
          <div className="cc-identity-onboarding__field">
            <div className="cc-identity-onboarding__field-header">
              <label htmlFor="catsco-display-name">你的名字</label>
              <span className="cc-identity-onboarding__counter" aria-live="polite">{name.length}/32</span>
            </div>
            <input
              ref={inputRef}
              id="catsco-display-name"
              name="displayName"
              type="text"
              autoComplete="nickname"
              maxLength={32}
              placeholder="输入名字…"
              value={name}
              onChange={(event) => {
                setName(event.target.value);
                if (error) setError('');
              }}
              aria-describedby={error ? 'identity-onboarding-error' : undefined}
            />
          </div>
          <div
            id="identity-onboarding-error"
            className="cc-identity-onboarding__feedback"
            aria-live="polite"
          >
            {error && (
              <InlineFeedback tone="error">
                {error}
              </InlineFeedback>
            )}
          </div>
          <button type="submit" disabled={submitting || !name.trim()}>
            <span>{submitting ? '正在保存…' : '继续'}</span>
          </button>
        </form>
      </section>
    </main>
  );
}
