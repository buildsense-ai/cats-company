import React, { useEffect, useState } from 'react';
import { resolveMediaURL } from '../api';
import { IdentityCat } from '../components/identity-onboarding';

const palette = ['#f59e0b', '#ef4444', '#14b8a6', '#07C160', '#06b6d4', '#3b82f6', '#8b5cf6', '#a16207'];

function hashName(name) {
  let hash = 5381;
  for (const ch of String(name || '')) {
    hash = ((hash << 5) + hash) + ch.charCodeAt(0);
  }
  return Math.abs(hash);
}

function resolveSrc(src) {
  if (!src) return null;
  return resolveMediaURL(src);
}

export default function Avatar({ name, src, size = 40, isGroup = false, isBot = false, identityFallback = false, className = '' }) {
  const label = String(name || '?').trim();
  const initials = (label[0] || '?').toUpperCase();
  const background = palette[hashName(label) % palette.length];
  const finalClassName = ['oc-avatar', className].filter(Boolean).join(' ');
  const resolvedSrc = resolveSrc(src);
  const [imageFailed, setImageFailed] = useState(false);

  useEffect(() => {
    setImageFailed(false);
  }, [resolvedSrc]);

  return (
    <div
      className={finalClassName}
      style={{
        width: size,
        height: size,
        borderRadius: 8,
        background,
      }}
    >
      {resolvedSrc && !imageFailed ? (
        <img
          src={resolvedSrc}
          alt={label}
          className="oc-avatar-img"
          loading="lazy"
          decoding="async"
          onError={() => setImageFailed(true)}
        />
      ) : isGroup ? (
        <span className="oc-avatar-icon" aria-label="group">群</span>
      ) : isBot ? (
        <span className="oc-avatar-icon" aria-label="bot">B</span>
      ) : identityFallback ? (
        <IdentityCat name={label} className="oc-avatar-identity-cat" />
      ) : (
        <span className="oc-avatar-text">{initials}</span>
      )}
    </div>
  );
}
