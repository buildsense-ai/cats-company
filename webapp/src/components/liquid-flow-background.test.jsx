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
});
