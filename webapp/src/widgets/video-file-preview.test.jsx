import React, { act } from 'react';
import { createRoot } from 'react-dom/client';
import { Simulate } from 'react-dom/test-utils';
import { afterEach, beforeEach, expect, it, vi } from 'vitest';
import VideoFilePreview from './video-file-preview';
let container, root;
beforeEach(() => {
  container = document.createElement('div'); document.body.append(container); root = createRoot(container);
  vi.spyOn(HTMLMediaElement.prototype, 'pause').mockImplementation(() => {});
  vi.spyOn(HTMLMediaElement.prototype, 'play').mockResolvedValue();
});
afterEach(async () => { await act(async () => root.unmount()); container.remove(); vi.restoreAllMocks(); });
async function mount() {
  await act(async () => root.render(<VideoFilePreview src="/demo.mp4" annotating />));
  const video = container.querySelector('video');
  Object.defineProperties(video, { duration: { configurable: true, value: 30 }, readyState: { configurable: true, value: 2 } });
  video.currentTime = 12.5;
  await act(async () => video.dispatchEvent(new Event('loadeddata')));
  return video;
}

it('keeps the same decoded media and usable transport when annotation mode changes', async () => {
  const video = await mount();
  expect(container.querySelector('input[type=range]').disabled).toBe(false);
  expect(container.querySelector('.file-annotation-video-time').textContent).toContain('0:12.50');
  expect(video.controls).toBe(false);
  await act(async () => root.render(<VideoFilePreview src="/demo.mp4" annotating={false} />));
  expect(container.querySelector('video')).toBe(video);
  expect(video.currentTime).toBe(12.5); expect(video.controls).toBe(true);
});

it('seeks from a separate timeline and pauses before changing the selected frame', async () => {
  const video = await mount();
  const range = container.querySelector('input[type=range]');
  await act(async () => { range.value = '18.25'; Simulate.change(range); });
  expect(video.pause).toHaveBeenCalled(); expect(video.currentTime).toBe(18.25);
  expect(container.querySelector('.file-annotation-video-time').textContent).toContain('0:18.25');
});

it('makes playback failure recoverable and preserves the selected time', async () => {
  const video = await mount();
  video.play.mockRejectedValueOnce(new Error('blocked'));
  await act(async () => container.querySelector('button').click());
  expect(container.querySelector('[role=alert]').textContent).toContain('暂时无法播放');
  expect(video.currentTime).toBe(12.5);
  await act(async () => container.querySelector('button').click());
  expect(container.querySelector('[role=alert]')).toBeNull();
});
