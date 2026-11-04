import { afterEach, describe, expect, it, vi } from 'vitest';
import { captureFilePreviewScreenshots } from './file-annotations';

const target = { rect: { x: .1, y: .1, width: .3, height: .4 }, time_seconds: 12.5 };
const jpeg = 'data:image/jpeg;base64,/9j/2Q==';
let video;
afterEach(() => { video?.remove(); vi.restoreAllMocks(); vi.useRealTimers(); });
function fixture({ seeking = false, readyState = 2 } = {}) {
  video = document.createElement('video'); document.body.append(video);
  video.currentTime = 12.5;
  for (const [key, value] of Object.entries({ seeking, readyState, videoWidth: 1920, videoHeight: 1080 })) {
    Object.defineProperty(video, key, { configurable: true, writable: true, value });
  }
  video.pause = vi.fn();
  video.getBoundingClientRect = () => ({ left: 0, top: 0, width: 800, height: 450 });
  const drawImage = vi.fn();
  vi.spyOn(HTMLCanvasElement.prototype, 'getContext').mockReturnValue({ drawImage, strokeRect: vi.fn() });
  vi.spyOn(HTMLCanvasElement.prototype, 'toDataURL').mockReturnValue(jpeg);
  return { video, drawImage };
}

describe('video annotation decoded frame capture', () => {
  it('waits for the seeked frame instead of copying pixels from the previous time', async () => {
    const { video, drawImage } = fixture({ seeking: true });
    const capture = captureFilePreviewScreenshots(video, 'video', target);
    expect(drawImage).not.toHaveBeenCalled();
    video.seeking = false; video.dispatchEvent(new Event('seeked'));
    const result = await capture;
    expect(result.screenshots).toHaveLength(2);
    expect(drawImage).toHaveBeenCalledTimes(2);
    expect(video.currentTime).toBe(12.5);
    expect(video.pause).toHaveBeenCalled();
  });

  it('waits for media data when only metadata has loaded', async () => {
    const { video, drawImage } = fixture({ readyState: 1 });
    const capture = captureFilePreviewScreenshots(video, 'video', target);
    expect(drawImage).not.toHaveBeenCalled();
    video.readyState = 2; video.dispatchEvent(new Event('loadeddata'));
    expect((await capture).screenshots).toHaveLength(2);
  });

  it('rejects a superseded seek and never rewinds the video to an old target', async () => {
    const { video, drawImage } = fixture({ seeking: true });
    const capture = captureFilePreviewScreenshots(video, 'video', target);
    const rejected = expect(capture).rejects.toMatchObject({ code: 'stale' });
    video.currentTime = 24; video.seeking = false; video.dispatchEvent(new Event('seeked'));
    await rejected;
    expect(video.currentTime).toBe(24); expect(drawImage).not.toHaveBeenCalled();
  });

  it('cancels a waiting frame without leaking a late capture, then allows a new capture', async () => {
    const { video, drawImage } = fixture({ seeking: true });
    const controller = new AbortController();
    const capture = captureFilePreviewScreenshots(video, 'video', target, { signal: controller.signal });
    const rejected = expect(capture).rejects.toMatchObject({ code: 'aborted' });
    controller.abort(); await rejected;
    video.seeking = false; video.dispatchEvent(new Event('seeked'));
    expect(drawImage).not.toHaveBeenCalled();
    expect((await captureFilePreviewScreenshots(video, 'video', target)).screenshots).toHaveLength(2);
  });

  it('times out a stalled decode and permits retry instead of waiting forever', async () => {
    vi.useFakeTimers();
    fixture({ seeking: true });
    const capture = captureFilePreviewScreenshots(video, 'video', target, { timeoutMs: 100 });
    const rejected = expect(capture).rejects.toMatchObject({ code: 'capture-timeout' });
    await vi.advanceTimersByTimeAsync(100); await rejected;
    video.seeking = false;
    expect((await captureFilePreviewScreenshots(video, 'video', target)).screenshots).toHaveLength(2);
  });
});
