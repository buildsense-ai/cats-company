import React, { act } from 'react';
import { createRoot } from 'react-dom/client';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import LiquidFlowBackground, {
  LIQUID_REDUCED_MOTION_QUERY,
  shouldMountLiquidFlowBackground,
} from './liquid-flow-background';

const tinodeWebSource = readFileSync(
  resolve(process.cwd(), 'src/views/tinode-web.jsx'),
  'utf8',
);

function installMotionPreference(initialMatches = false) {
  const listeners = new Set();
  let matches = initialMatches;
  const mediaQuery = {
    media: LIQUID_REDUCED_MOTION_QUERY,
    get matches() {
      return matches;
    },
    addEventListener: vi.fn((type, listener) => {
      if (type === 'change') listeners.add(listener);
    }),
    removeEventListener: vi.fn((type, listener) => {
      if (type === 'change') listeners.delete(listener);
    }),
  };
  const matchMedia = vi.fn().mockReturnValue(mediaQuery);
  Object.defineProperty(window, 'matchMedia', {
    configurable: true,
    value: matchMedia,
  });

  return {
    matchMedia,
    setMatches(nextMatches) {
      matches = nextMatches;
      listeners.forEach((listener) => listener({ matches, media: LIQUID_REDUCED_MOTION_QUERY }));
    },
  };
}

function stubCurrentTime(video, value) {
  Object.defineProperty(video, 'currentTime', {
    configurable: true,
    get: () => value,
    set: () => {},
  });
}

describe('LiquidFlowBackground', () => {
  let container;
  let root;
  let originalMatchMedia;
  let pause;
  let play;

  beforeEach(() => {
    originalMatchMedia = window.matchMedia;
    pause = vi.spyOn(HTMLMediaElement.prototype, 'pause').mockImplementation(() => {});
    play = vi.spyOn(HTMLMediaElement.prototype, 'play').mockResolvedValue();
    container = document.createElement('div');
    document.body.appendChild(container);
    root = createRoot(container);
  });

  afterEach(async () => {
    await act(async () => root.unmount());
    container.remove();
    pause.mockRestore();
    play.mockRestore();
    Object.defineProperty(window, 'matchMedia', {
      configurable: true,
      value: originalMatchMedia,
    });
  });

  it('mounts media only for the light liquid theme', () => {
    expect(shouldMountLiquidFlowBackground('liquid')).toBe(true);
    ['light', 'dark', 'liquid-green', undefined].forEach((theme) => {
      expect(shouldMountLiquidFlowBackground(theme)).toBe(false);
    });
    expect(tinodeWebSource).toContain(
      '{shouldMountLiquidFlowBackground(theme) && <LiquidFlowBackground />}',
    );
  });

  it('loads the active layer first and defers the crossfade layer', async () => {
    const { matchMedia } = installMotionPreference(false);

    await act(async () => root.render(<LiquidFlowBackground />));

    const videos = [...container.querySelectorAll('video')];
    expect(matchMedia).toHaveBeenCalledWith(LIQUID_REDUCED_MOTION_QUERY);
    expect(videos).toHaveLength(2);
    expect(videos[0].getAttribute('preload')).toBe('auto');
    expect(videos[0].autoplay).toBe(true);
    expect(videos[1].getAttribute('preload')).toBe('none');
    expect(videos[1].autoplay).toBe(false);
    videos.forEach((video) => {
      expect(video.querySelector('source[type="video/webm"]')).not.toBeNull();
      expect(video.querySelector('source[type="video/mp4"]')).not.toBeNull();
    });
  });

  it('does not create video nodes for reduced motion and stops active media when it changes', async () => {
    const motionPreference = installMotionPreference(true);

    await act(async () => root.render(<LiquidFlowBackground />));
    expect(container.querySelector('video')).toBeNull();

    await act(async () => motionPreference.setMatches(false));
    expect(container.querySelectorAll('video')).toHaveLength(2);

    await act(async () => motionPreference.setMatches(true));
    expect(container.querySelector('video')).toBeNull();
    expect(pause).toHaveBeenCalledTimes(2);
  });

  it('warms the crossfade layer once the primary layer can play', async () => {
    installMotionPreference(false);
    const load = vi.spyOn(HTMLMediaElement.prototype, 'load').mockImplementation(() => {});

    try {
      await act(async () => root.render(<LiquidFlowBackground />));

      const videos = [...container.querySelectorAll('video')];
      expect(videos[1].getAttribute('preload')).toBe('none');
      expect(load).not.toHaveBeenCalled();

      await act(async () => {
        videos[0].dispatchEvent(new Event('canplay'));
      });

      expect(videos[0].getAttribute('preload')).toBe('auto');
      expect(videos[1].getAttribute('preload')).toBe('auto');
      expect(load).toHaveBeenCalledTimes(1);
    } finally {
      load.mockRestore();
    }
  });

  it('waits for the incoming layer to play before revealing it and pausing the outgoing layer', async () => {
    installMotionPreference(false);
    await act(async () => root.render(<LiquidFlowBackground />));

    const videos = [...container.querySelectorAll('video')];
    Object.defineProperty(videos[0], 'duration', { configurable: true, value: 6 });
    stubCurrentTime(videos[0], 4.9);
    stubCurrentTime(videos[1], 0);

    vi.useFakeTimers();
    try {
      await act(async () => {
        videos[0].dispatchEvent(new Event('timeupdate'));
      });
      expect(play).toHaveBeenCalledTimes(1);
      expect(videos[1].className).not.toContain('is-active');
      expect(pause).not.toHaveBeenCalled();

      await act(async () => {
        vi.advanceTimersByTime(1200);
      });
      expect(pause).not.toHaveBeenCalled();
      expect(videos[1].className).not.toContain('is-active');

      await act(async () => {
        videos[1].dispatchEvent(new Event('playing'));
      });
      expect(videos[1].className).toContain('is-active');
      expect(videos[0].className).not.toContain('is-active');
      expect(pause).toHaveBeenCalledTimes(1);
    } finally {
      vi.useRealTimers();
    }
  });

  it('releases the transition lock when the incoming layer never starts playing', async () => {
    installMotionPreference(false);
    await act(async () => root.render(<LiquidFlowBackground />));

    const videos = [...container.querySelectorAll('video')];
    Object.defineProperty(videos[0], 'duration', { configurable: true, value: 6 });
    stubCurrentTime(videos[0], 4.9);
    stubCurrentTime(videos[1], 0);

    vi.useFakeTimers();
    try {
      await act(async () => {
        videos[0].dispatchEvent(new Event('timeupdate'));
      });
      expect(play).toHaveBeenCalledTimes(1);

      await act(async () => {
        vi.advanceTimersByTime(4000);
      });
      expect(pause).not.toHaveBeenCalled();

      await act(async () => {
        videos[0].dispatchEvent(new Event('timeupdate'));
      });
      expect(play).toHaveBeenCalledTimes(2);
    } finally {
      vi.useRealTimers();
    }
  });
});
