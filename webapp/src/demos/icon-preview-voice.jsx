// Generated unchanged declarations from widgets/chat-composer.jsx.
import React, { useEffect, useRef } from 'react';
import { Mic } from 'lucide-react';
const VOICE_WAVE_RAMP_MS = 2200;
function voiceWavePhaseSpeed(elapsedMs) {
  const progress = Math.min(1, Math.max(0, elapsedMs / VOICE_WAVE_RAMP_MS));
  const eased = progress * progress * (3 - (2 * progress));
  return 0.08 + (1.72 * eased);
}

function voiceWavePath(level, phase, baseline) {
  const amplitude = 12 + (level * 64);
  const point = (offset, strength = 1) => Math.round(
    baseline + (Math.sin(phase + offset) * amplitude * strength),
  );
  return [
    `M0 ${point(0, 0.45)}`,
    `C120 ${point(0.8)} 232 ${point(1.9)} 360 ${point(2.8, 0.8)}`,
    `C493 ${point(3.7)} 609 ${point(4.8)} 742 ${point(5.8, 0.85)}`,
    `C852 ${point(6.8)} 929 ${point(7.7, 0.72)} 1000 ${point(8.5, 0.45)}`,
    'L1000 260 L0 260 Z',
  ].join(' ');
}

const VoiceHoldWave = React.memo(function VoiceHoldWave({ levelRef }) {
  const rimPathRef = useRef(null);
  const fillPathRef = useRef(null);

  useEffect(() => {
    if (typeof window.requestAnimationFrame !== 'function') return undefined;
    if (window.matchMedia?.('(prefers-reduced-motion: reduce)').matches) return undefined;
    let animationFrame = 0;
    let startedAt = null;
    let previousAt = null;
    let phase = 0;
    let level = 0;
    const animate = (now) => {
      if (startedAt === null) startedAt = now;
      if (previousAt === null) previousAt = now;
      const deltaSeconds = Math.min(50, Math.max(0, now - previousAt)) / 1000;
      previousAt = now;
      phase += voiceWavePhaseSpeed(now - startedAt) * deltaSeconds;
      const targetLevel = Math.min(1, Math.max(0, Number(levelRef.current) || 0));
      level += (targetLevel - level) * (targetLevel > level ? 0.4 : 0.15);
      rimPathRef.current?.setAttribute('d', voiceWavePath(level * 0.72, phase + 0.42, 58));
      fillPathRef.current?.setAttribute('d', voiceWavePath(level, phase, 72));
      animationFrame = window.requestAnimationFrame(animate);
    };
    animationFrame = window.requestAnimationFrame(animate);
    return () => window.cancelAnimationFrame(animationFrame);
  }, [levelRef]);

  return (
    <div className="v3-voice-hold-wave" aria-hidden="true">
      <svg viewBox="0 0 1000 260" preserveAspectRatio="none" focusable="false">
        <path
          ref={rimPathRef}
          className="v3-voice-hold-wave-rim"
          d={voiceWavePath(0, 0.42, 58)}
        />
        <path
          ref={fillPathRef}
          className="v3-voice-hold-wave-fill"
          d={voiceWavePath(0, 0, 72)}
        />
      </svg>
      <div className="v3-voice-hold-mic"><Mic size={38} strokeWidth={2.1} /></div>
    </div>
  );
});
export default VoiceHoldWave;
