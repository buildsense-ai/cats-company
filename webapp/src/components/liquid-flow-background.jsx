import React, { useEffect, useRef, useState } from 'react';

const LIQUID_PLAYBACK_RATE = 2 / 3;
export const LIQUID_REDUCED_MOTION_QUERY = '(prefers-reduced-motion: reduce)';
export const shouldMountLiquidFlowBackground = (theme) => theme === 'liquid';

function usePrefersReducedMotion() {
  const [prefersReducedMotion, setPrefersReducedMotion] = useState(() => (
    globalThis.window?.matchMedia?.(LIQUID_REDUCED_MOTION_QUERY)?.matches ?? false
  ));

  useEffect(() => {
    const mediaQuery = globalThis.window?.matchMedia?.(LIQUID_REDUCED_MOTION_QUERY);
    if (!mediaQuery) return undefined;

    const updatePreference = () => setPrefersReducedMotion(mediaQuery.matches);
    updatePreference();
    if (mediaQuery.addEventListener) {
      mediaQuery.addEventListener('change', updatePreference);
    } else {
      mediaQuery.addListener?.(updatePreference);
    }

    return () => {
      if (mediaQuery.removeEventListener) {
        mediaQuery.removeEventListener('change', updatePreference);
      } else {
        mediaQuery.removeListener?.(updatePreference);
      }
    };
  }, []);

  return prefersReducedMotion;
}

/**
 * Ambient flow used by the light liquid theme. The reference is a text-free
 * texture loop, so prefer the compressed WebM and keep the original MP4 as a
 * browser compatibility fallback. Two muted layers crossfade at the loop
 * boundary so the motion does not snap back to the first frame.
 */
export default function LiquidFlowBackground() {
  const prefersReducedMotion = usePrefersReducedMotion();
  const videoRefs = useRef([]);
  const transitionTimer = useRef(null);
  const activeIndexRef = useRef(0);
  const transitioningRef = useRef(false);
  const [activeIndex, setActiveIndex] = useState(0);

  useEffect(() => {
    const videos = videoRefs.current.filter(Boolean);
    if (!videos.length) return undefined;

    const crossfade = (index) => {
      if (transitioningRef.current) return;
      const current = videos[index];
      const nextIndex = index === 0 ? 1 : 0;
      const next = videos[nextIndex];
      if (!current || !next) return;

      transitioningRef.current = true;
      next.currentTime = 0;
      next.playbackRate = LIQUID_PLAYBACK_RATE;
      void next.play().catch(() => {});
      activeIndexRef.current = nextIndex;
      setActiveIndex(nextIndex);
      transitionTimer.current = window.setTimeout(() => {
        current.pause();
        current.currentTime = 0;
        transitioningRef.current = false;
      }, 1200);
    };

    const listeners = videos.map((video, index) => {
      const onTimeUpdate = () => {
        if (index === activeIndexRef.current && video.duration && video.duration - video.currentTime <= 1.2) {
          crossfade(index);
        }
      };
      const onEnded = () => {
        if (index === activeIndexRef.current) crossfade(index);
      };
      video.addEventListener('timeupdate', onTimeUpdate);
      video.addEventListener('ended', onEnded);
      return { video, onTimeUpdate, onEnded };
    });

    return () => {
      listeners.forEach(({ video, onTimeUpdate, onEnded }) => {
        video.removeEventListener('timeupdate', onTimeUpdate);
        video.removeEventListener('ended', onEnded);
      });
      window.clearTimeout(transitionTimer.current);
      transitionTimer.current = null;
      videos.forEach((video) => {
        video.pause();
        try {
          video.currentTime = 0;
        } catch {
          // Media that has not loaded yet can reject currentTime assignment.
        }
      });
      transitioningRef.current = false;
      activeIndexRef.current = 0;
    };
  }, [prefersReducedMotion]);

  if (prefersReducedMotion) return null;

  const setVideoRef = (index) => (node) => {
    videoRefs.current[index] = node;
  };

  const renderVideo = (index) => (
    <video
      ref={setVideoRef(index)}
      className={`cc-liquid-flow-background__video${activeIndex === index ? ' is-active' : ''}`}
      autoPlay={index === 0}
      muted
      playsInline
      preload={index === 0 ? 'auto' : 'none'}
      poster="/texture-background-2-poster.png"
      onLoadedMetadata={(event) => {
        event.currentTarget.playbackRate = LIQUID_PLAYBACK_RATE;
      }}
    >
      <source src="/texture-background-2.webm" type="video/webm" />
      <source src="/texture-background-2.mp4" type="video/mp4" />
    </video>
  );

  return (
    <div className="cc-liquid-flow-background" aria-hidden="true">
      <span className="cc-liquid-flow-background__wash" />
      {renderVideo(0)}
      {renderVideo(1)}
      <span className="cc-liquid-flow-background__veil" />
      <span className="cc-liquid-flow-background__grain" />
    </div>
  );
}
