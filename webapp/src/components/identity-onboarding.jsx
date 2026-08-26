import React, { useId, useMemo, useRef, useState, useEffect } from 'react';
import { ArrowRight } from 'lucide-react';
import { InlineFeedback } from './feedback-system';
import './identity-onboarding.css';

const CAT_PALETTES = [
  { body: '#62c9a8', ear: '#f4a6a0', detail: '#207f6c' },
  { body: '#9b8ed8', ear: '#f3aaa8', detail: '#6256a8' },
  { body: '#f1a36f', ear: '#ffd0b0', detail: '#a85f35' },
  { body: '#70b7d4', ear: '#f3a9b6', detail: '#317b99' },
  { body: '#e3bd5e', ear: '#f3a4a4', detail: '#9a7420' },
];

function hashName(value) {
  return Array.from(value || 'CatsCo').reduce(
    (hash, character) => ((hash * 33) ^ character.codePointAt(0)) >>> 0,
    5381,
  );
}

function catTraits(name) {
  const hash = hashName(name.trim().toLocaleLowerCase());
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
  };
}

export function IdentityCat({ name }) {
  const filterId = useId().replace(/:/g, '');
  const traits = useMemo(() => catTraits(name), [name]);
  const {
    hash, palette, headTilt, eyeGap, eyeSize, bodyWidth, earLift, smile, tailSide, whiskerLift,
  } = traits;

  return (
    <svg
      className="cc-identity-cat"
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
        <path
          className="cc-identity-cat__tail"
          d={tailSide > 0
            ? 'M 184 213 C 235 206, 241 248, 205 252 C 188 254, 188 238, 204 237'
            : 'M 96 213 C 45 206, 39 248, 75 252 C 92 254, 92 238, 76 237'}
          style={{ stroke: palette.detail }}
        />
        <ellipse
          cx="140"
          cy="218"
          rx={bodyWidth / 2}
          ry="52"
          fill={palette.body}
          className="cc-identity-cat__body"
        />
        <path className="cc-identity-cat__belly" d="M119 189 C104 221 113 254 140 263 C167 254 176 221 161 189" />
        <g transform={`rotate(${headTilt} 140 125)`}>
          <path
            d={`M84 ${86 - earLift} C78 54, 93 40, 116 70 L164 70 C187 40, 202 54, 196 ${86 - earLift} C213 103, 210 143, 190 160 C169 178, 111 178, 90 160 C70 143, 67 103, 84 ${86 - earLift} Z`}
            fill={palette.body}
            className="cc-identity-cat__head"
          />
          <path d={`M89 ${78 - earLift} C88 61, 96 55, 108 72`} fill={palette.ear} className="cc-identity-cat__inner-ear" />
          <path d={`M171 72 C184 55, 192 61, 191 ${78 - earLift}`} fill={palette.ear} className="cc-identity-cat__inner-ear" />
          <circle cx={140 - eyeGap} cy="118" r={eyeSize + 4} className="cc-identity-cat__eye-white" />
          <circle cx={140 + eyeGap} cy="118" r={eyeSize + 4} className="cc-identity-cat__eye-white" />
          <circle cx={141 - eyeGap} cy="119" r={eyeSize} className="cc-identity-cat__eye" />
          <circle cx={139 + eyeGap} cy="119" r={eyeSize} className="cc-identity-cat__eye" />
          <circle cx={143 - eyeGap} cy="117" r="1.8" className="cc-identity-cat__eye-shine" />
          <circle cx={141 + eyeGap} cy="117" r="1.8" className="cc-identity-cat__eye-shine" />
          <path d="M135 136 Q140 140 145 136 Q140 132 135 136" fill={palette.ear} className="cc-identity-cat__nose" />
          <path d={`M140 140 C139 147, ${140 - smile} 151, ${132 - smile} 146 M140 140 C141 147, ${140 + smile} 151, ${148 + smile} 146`} className="cc-identity-cat__mouth" />
          <path d={`M126 ${141 + whiskerLift} L83 ${135 + whiskerLift} M125 ${148 + whiskerLift} L80 ${153 + whiskerLift} M154 ${141 - whiskerLift} L197 ${135 - whiskerLift} M155 ${148 - whiskerLift} L200 ${153 - whiskerLift}`} className="cc-identity-cat__whiskers" />
          <path d="M116 82 Q126 60 136 80 M132 78 Q141 55 150 79 M147 79 Q158 61 166 84" className="cc-identity-cat__tufts" />
        </g>
        <path d="M107 213 C96 219 94 237 103 245" className="cc-identity-cat__arm" />
        <path d="M173 213 C184 219 186 237 177 245" className="cc-identity-cat__arm" />
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
        <span>CatsCo</span>
      </div>

      <section className="cc-identity-onboarding__content" aria-labelledby="identity-onboarding-title">
        <h1 id="identity-onboarding-title">你是谁？</h1>
        <div className="cc-identity-onboarding__stage">
          <IdentityCat name={name} />
        </div>

        <form className="cc-identity-onboarding__form" onSubmit={handleSubmit}>
          <label htmlFor="catsco-display-name">你的名字</label>
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
            <span>{submitting ? '正在保存…' : '确认名字'}</span>
            <ArrowRight size={18} aria-hidden="true" />
          </button>
        </form>
      </section>
    </main>
  );
}
