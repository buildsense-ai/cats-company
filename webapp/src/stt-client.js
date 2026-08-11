import PCM_WORKLET_URL from './stt-pcm-worklet.js?url&no-inline';

const MAX_BUFFERED_AUDIO_BYTES = 160_000;
const PARTIAL_RENDER_INTERVAL_MS = 80;
const CAPTURE_FLUSH_TIMEOUT_MS = 300;
const API_BASE = import.meta.env.VITE_API_BASE || '';

let reusableMicrophoneStream = null;
let pendingMicrophoneRequest = null;
let reusableMicrophoneConsumers = 0;
let microphoneRequestGeneration = 0;
let microphoneLifecycleCleanupInstalled = false;

function normalizeAudioLevel(rms) {
  const decibels = 20 * Math.log10(Math.max(Number(rms) || 0, 0.00001));
  const linear = Math.min(1, Math.max(0, (decibels + 55) / 47));
  return linear ** 1.35;
}

function sttAPIBaseURL() {
  if (!API_BASE) return globalThis.location?.origin || 'http://localhost';
  return new URL(API_BASE, globalThis.location?.origin || 'http://localhost').toString().replace(/\/+$/, '');
}

async function createSTTSessionRequest() {
  const headers = { 'Content-Type': 'application/json' };
  const token = globalThis.localStorage?.getItem('oc_token');
  if (token) headers.Authorization = `Bearer ${token}`;
  const response = await fetch(`${API_BASE}/api/stt/sessions`, { method: 'POST', headers });
  let payload = {};
  try {
    payload = await response.json();
  } catch {
    payload = {};
  }
  if (!response.ok) {
    const error = new Error(payload.error || '无法创建语音识别会话');
    error.status = response.status;
    throw error;
  }
  return payload;
}

export function isStreamingSTTSupported() {
  return Boolean(
    globalThis.navigator?.mediaDevices?.getUserMedia
    && (globalThis.AudioContext || globalThis.webkitAudioContext)
    && globalThis.AudioWorkletNode,
  );
}

export function resolveSTTWebSocketURL(ticket) {
  const url = new URL('/api/stt/realtime', sttAPIBaseURL());
  url.protocol = url.protocol === 'https:' ? 'wss:' : 'ws:';
  url.searchParams.set('ticket', ticket);
  return url.toString();
}

function hasLiveAudioTrack(stream) {
  return Boolean(stream?.getAudioTracks?.().some((track) => track.readyState !== 'ended'));
}

function setMicrophoneTracksEnabled(stream, enabled) {
  stream?.getAudioTracks?.().forEach((track) => {
    if (track.readyState !== 'ended') track.enabled = enabled;
  });
}

function createMicrophoneAbortError() {
  const error = new Error('麦克风会话已关闭');
  error.name = 'AbortError';
  error.code = 'STT_LIFECYCLE_ABORT';
  return error;
}

function isMicrophoneLifecycleAbort(error) {
  return error?.code === 'STT_LIFECYCLE_ABORT';
}

function isPageHidden() {
  return typeof document !== 'undefined' && document.visibilityState === 'hidden';
}

function assertMicrophoneCaptureActive(generation) {
  if (generation !== microphoneRequestGeneration || isPageHidden()) {
    throw createMicrophoneAbortError();
  }
}

// Keep the authorized stream muted between foreground captures. Stopping the
// track can make standalone PWAs show the platform microphone prompt again.
export function releaseReusableMicrophoneStream() {
  microphoneRequestGeneration += 1;
  reusableMicrophoneConsumers = 0;
  const stream = reusableMicrophoneStream;
  reusableMicrophoneStream = null;
  stream?.getTracks?.().forEach((track) => track.stop?.());
}

function installMicrophoneLifecycleCleanup() {
  if (microphoneLifecycleCleanupInstalled || typeof document === 'undefined') return;
  microphoneLifecycleCleanupInstalled = true;

  document.addEventListener('visibilitychange', () => {
    if (document.visibilityState === 'hidden') releaseReusableMicrophoneStream();
  });
  globalThis.addEventListener?.('pagehide', releaseReusableMicrophoneStream);
}

function rememberReusableMicrophoneStream(stream) {
  reusableMicrophoneStream = stream;
  reusableMicrophoneConsumers = 0;
  const clearEndedStream = () => {
    if (reusableMicrophoneStream !== stream || hasLiveAudioTrack(stream)) return;
    reusableMicrophoneStream = null;
    reusableMicrophoneConsumers = 0;
  };
  stream.getTracks?.().forEach((track) => {
    track.addEventListener?.('ended', clearEndedStream, { once: true });
  });
}

async function acquireReusableMicrophoneStream() {
  installMicrophoneLifecycleCleanup();
  assertMicrophoneCaptureActive(microphoneRequestGeneration);

  if (hasLiveAudioTrack(reusableMicrophoneStream)) {
    setMicrophoneTracksEnabled(reusableMicrophoneStream, true);
    return reusableMicrophoneStream;
  }
  if (reusableMicrophoneStream) releaseReusableMicrophoneStream();

  // Releasing an ended stream advances the lifecycle generation. Read it only
  // after that cleanup so a fresh foreground request is not mistaken for the
  // stale request that owned the ended stream.
  const generation = microphoneRequestGeneration;
  assertMicrophoneCaptureActive(generation);
  if (pendingMicrophoneRequest?.generation === microphoneRequestGeneration) {
    return pendingMicrophoneRequest.promise;
  }

  const pendingRequest = { generation, promise: null };
  pendingRequest.promise = navigator.mediaDevices.getUserMedia({
    audio: {
      channelCount: 1,
      echoCancellation: true,
      noiseSuppression: true,
      autoGainControl: true,
    },
  }).then((stream) => {
    if (generation !== microphoneRequestGeneration) {
      stream.getTracks().forEach((track) => track.stop());
      throw createMicrophoneAbortError();
    }
    if (!hasLiveAudioTrack(stream)) {
      stream.getTracks().forEach((track) => track.stop());
      throw new Error('未获取到可用的麦克风音轨');
    }
    rememberReusableMicrophoneStream(stream);
    setMicrophoneTracksEnabled(stream, true);
    return stream;
  }).finally(() => {
    if (pendingMicrophoneRequest === pendingRequest) pendingMicrophoneRequest = null;
  });
  pendingMicrophoneRequest = pendingRequest;
  return pendingRequest.promise;
}

function retainReusableMicrophoneStream(stream) {
  if (stream !== reusableMicrophoneStream) return;
  reusableMicrophoneConsumers += 1;
  setMicrophoneTracksEnabled(stream, true);
}

function releaseReusableMicrophoneConsumer(stream) {
  if (stream !== reusableMicrophoneStream) return;
  reusableMicrophoneConsumers = Math.max(0, reusableMicrophoneConsumers - 1);
  if (reusableMicrophoneConsumers === 0) setMicrophoneTracksEnabled(stream, false);
}

export async function createPCM16Capture({ onFrame, onLevel, onSuspended }) {
  if (!isStreamingSTTSupported()) {
    throw new Error('当前浏览器不支持流式语音输入');
  }
  assertMicrophoneCaptureActive(microphoneRequestGeneration);
  const stream = await acquireReusableMicrophoneStream();
  // acquireReusableMicrophoneStream() may release a stale ended stream and
  // advance the generation before returning a fresh stream.
  const generation = microphoneRequestGeneration;
  assertMicrophoneCaptureActive(generation);
  retainReusableMicrophoneStream(stream);
  const AudioContextClass = globalThis.AudioContext || globalThis.webkitAudioContext;
  let context;
  let source;
  let worklet;
  try {
    context = new AudioContextClass({ latencyHint: 'interactive' });
    await context.audioWorklet.addModule(PCM_WORKLET_URL);
    assertMicrophoneCaptureActive(generation);
    source = context.createMediaStreamSource(stream);
    worklet = new AudioWorkletNode(context, 'catsco-pcm16-capture', {
      numberOfInputs: 1,
      numberOfOutputs: 0,
      channelCount: 1,
    });
    let resolveFlush = null;
    worklet.port.onmessage = (event) => {
      if (event.data instanceof ArrayBuffer) onFrame(event.data);
      if (event.data?.type === 'level') onLevel?.(event.data.rms);
      if (event.data?.type === 'flushed') resolveFlush?.();
    };
    source.connect(worklet);
    context.onstatechange = () => {
      if (context.state === 'suspended' || context.state === 'interrupted') onSuspended?.();
    };
    if (context.state === 'suspended') {
      await context.resume();
      assertMicrophoneCaptureActive(generation);
    }
    assertMicrophoneCaptureActive(generation);

    let stopPromise = null;
    return {
      stop() {
        if (stopPromise) return stopPromise;
        stopPromise = new Promise((resolve) => {
          let settled = false;
          const finish = () => {
            if (settled) return;
            settled = true;
            globalThis.clearTimeout(timeout);
            resolveFlush = null;
            worklet.port.onmessage = null;
            context.onstatechange = null;
            source.disconnect();
            worklet.disconnect();
            releaseReusableMicrophoneConsumer(stream);
            void context.close();
            resolve();
          };
          const timeout = globalThis.setTimeout(finish, CAPTURE_FLUSH_TIMEOUT_MS);
          resolveFlush = finish;
          worklet.port.postMessage({ type: 'flush' });
        });
        return stopPromise;
      },
    };
  } catch (error) {
    worklet?.disconnect?.();
    source?.disconnect?.();
    releaseReusableMicrophoneConsumer(stream);
    void context?.close?.();
    throw error;
  }
}

export class StreamingSTTSession {
  constructor(options = {}) {
    this.createSession = options.createSession || createSTTSessionRequest;
    this.createCapture = options.createCapture || createPCM16Capture;
    this.createWebSocket = options.createWebSocket || ((url) => new WebSocket(url));
    this.resolveWebSocketURL = options.resolveWebSocketURL || resolveSTTWebSocketURL;
    this.onState = options.onState || (() => {});
    this.onPartial = options.onPartial || (() => {});
    this.onAudioLevel = options.onAudioLevel || (() => {});
    this.onFinal = options.onFinal || (() => {});
    this.onError = options.onError || (() => {});
    this.socket = null;
    this.capture = null;
    this.state = 'idle';
    this.ready = false;
    this.stopRequested = false;
    this.terminal = false;
    this.preconnectFrames = [];
    this.preconnectBytes = 0;
    this.partialTimer = null;
    this.pendingPartial = '';
    this.lastPublishedPartial = '';
    this.lastPartialAt = 0;
    this.durationTimer = null;
    this.handleVisibilityChange = () => {
      if (document.visibilityState === 'hidden') void this.stop();
    };
    this.handlePageHide = () => void this.stop();
    this.lifecycleListenersInstalled = false;
  }

  installLifecycleListeners() {
    if (this.lifecycleListenersInstalled) return;
    this.lifecycleListenersInstalled = true;
    document.addEventListener('visibilitychange', this.handleVisibilityChange);
    globalThis.addEventListener?.('pagehide', this.handlePageHide);
  }

  completeWithoutCapture() {
    this.stopRequested = true;
    this.terminal = true;
    this.cleanup();
    this.setState('complete');
  }

  setState(state) {
    this.state = state;
    this.onState(state);
  }

  setDurationLimit(milliseconds) {
    const parsed = Number(milliseconds);
    if (!Number.isFinite(parsed)) return;
    const maxMilliseconds = Math.max(1, parsed);
    if (this.durationTimer) window.clearTimeout(this.durationTimer);
    this.durationTimer = window.setTimeout(() => void this.stop(), maxMilliseconds);
  }

  applyDurationLimit(payload) {
    if (Object.hasOwn(payload || {}, 'max_session_ms')) {
      this.setDurationLimit(payload.max_session_ms);
    } else if (Object.hasOwn(payload || {}, 'max_session_seconds')) {
      this.setDurationLimit(Number(payload.max_session_seconds) * 1000);
    }
  }

  async start() {
    if (this.state !== 'idle') return;
    this.setState('starting');
    this.installLifecycleListeners();
    if (isPageHidden()) {
      this.completeWithoutCapture();
      return;
    }
    const sessionPromise = this.createSession();
    const capturePromise = this.startCapture();
    try {
      const [capture, session] = await Promise.all([capturePromise, sessionPromise]);
      if (this.terminal) {
        return;
      }
      if (this.stopRequested || isPageHidden()) {
        if (this.capture === capture) {
          this.capture = null;
          await capture?.stop();
        }
        this.completeWithoutCapture();
        return;
      }
      if (!capture) return;
      this.applyDurationLimit(session);
      this.setState('connecting');
      const socket = this.createWebSocket(this.resolveWebSocketURL(session.ticket));
      this.socket = socket;
      socket.binaryType = 'arraybuffer';
      socket.onmessage = (event) => this.handleMessage(event.data);
      socket.onerror = () => this.fail(new Error('语音识别连接失败'));
      socket.onclose = () => {
        if (!this.terminal) this.fail(new Error('语音识别连接已断开'));
      };
    } catch (error) {
      if (this.stopRequested || isMicrophoneLifecycleAbort(error)) {
        this.completeWithoutCapture();
        return;
      }
      if (this.terminal) return;
      this.fail(this.normalizeStartError(error));
    }
  }

  async startCapture() {
    const capture = await this.createCapture({
      onFrame: (frame) => this.handleFrame(frame),
      onLevel: (rms) => this.publishAudioLevel(rms),
      onSuspended: () => void this.stop(),
    });
    if (this.terminal || this.stopRequested || isPageHidden()) {
      await capture.stop();
      return null;
    }
    this.capture = capture;
    return capture;
  }

  normalizeStartError(error) {
    if (error?.name === 'NotAllowedError') return new Error('需要麦克风权限才能使用语音输入');
    if (error?.status === 429) return new Error('语音输入额度已用完，请稍后再试');
    if (error?.status === 409) return new Error('已有语音输入正在进行');
    return error instanceof Error ? error : new Error('无法启动语音输入');
  }

  handleFrame(frame) {
    if (this.terminal || !(frame instanceof ArrayBuffer) || frame.byteLength === 0) return;
    if (!this.ready) {
      if (this.preconnectBytes + frame.byteLength > MAX_BUFFERED_AUDIO_BYTES) {
        this.fail(new Error('语音识别连接超时，请重试'));
        return;
      }
      this.preconnectFrames.push(frame);
      this.preconnectBytes += frame.byteLength;
      return;
    }
    this.sendAudio(frame);
  }

  publishAudioLevel(rms) {
    if (this.terminal) return;
    this.onAudioLevel(normalizeAudioLevel(rms));
  }

  sendAudio(frame) {
    if (this.socket?.readyState !== 1) return;
    if (this.socket.bufferedAmount > MAX_BUFFERED_AUDIO_BYTES) {
      this.fail(new Error('网络较慢，语音输入已停止'));
      return;
    }
    this.socket.send(frame);
  }

  handleMessage(raw) {
    let message;
    try {
      message = JSON.parse(raw);
    } catch {
      return;
    }
    switch (message.type) {
      case 'ready':
        this.ready = true;
        this.applyDurationLimit(message);
        this.setState(this.stopRequested ? 'finalizing' : 'recording');
        for (const frame of this.preconnectFrames) {
          if (this.terminal) return;
          this.sendAudio(frame);
        }
        this.preconnectFrames = [];
        this.preconnectBytes = 0;
        if (this.stopRequested && !this.terminal) this.sendControl('stop');
        break;
      case 'partial':
        this.publishPartial(String(message.text || ''));
        break;
      case 'final': {
        const text = String(message.text || '').trim();
        this.flushPartial();
        this.terminal = true;
        this.clearPartialTimer();
        this.cleanup();
        this.setState('complete');
        if (text) this.onFinal(text);
        break;
      }
      case 'error':
        this.fail(new Error(message.message || '语音识别失败'));
        break;
      default:
        break;
    }
  }

  publishPartial(text) {
    this.pendingPartial = text;
    const elapsed = Date.now() - this.lastPartialAt;
    if (elapsed >= PARTIAL_RENDER_INTERVAL_MS) {
      this.flushPartial();
      return;
    }
    if (this.partialTimer) return;
    this.partialTimer = window.setTimeout(() => {
      this.flushPartial();
    }, PARTIAL_RENDER_INTERVAL_MS - elapsed);
  }

  flushPartial() {
    this.clearPartialTimer();
    const text = this.pendingPartial;
    this.pendingPartial = '';
    if (!text || text === this.lastPublishedPartial) return;
    this.lastPublishedPartial = text;
    this.lastPartialAt = Date.now();
    this.onPartial(text);
  }

  clearPartialTimer() {
    if (this.partialTimer) window.clearTimeout(this.partialTimer);
    this.partialTimer = null;
  }

  sendControl(type) {
    if (this.socket?.readyState === 1) this.socket.send(JSON.stringify({ type }));
  }

  async stop() {
    if (this.terminal || this.state === 'idle' || this.state === 'complete') return;
    if (this.stopRequested) return;
    this.stopRequested = true;
    const capture = this.capture;
    this.capture = null;
    this.setState('finalizing');
    await capture?.stop();
    if (this.ready) this.sendControl('stop');
  }

  cancel() {
    if (this.terminal) return;
    this.terminal = true;
    this.sendControl('cancel');
    this.cleanup();
    this.setState('cancelled');
  }

  fail(error) {
    if (this.terminal) return;
    this.terminal = true;
    this.cleanup();
    this.setState('error');
    this.onError(error instanceof Error ? error : new Error('语音识别失败'));
  }

  cleanup() {
    this.clearPartialTimer();
    if (this.durationTimer) window.clearTimeout(this.durationTimer);
    this.durationTimer = null;
    document.removeEventListener('visibilitychange', this.handleVisibilityChange);
    globalThis.removeEventListener?.('pagehide', this.handlePageHide);
    this.lifecycleListenersInstalled = false;
    this.capture?.stop();
    this.capture = null;
    const socket = this.socket;
    this.socket = null;
    if (socket) {
      socket.onmessage = null;
      socket.onerror = null;
      socket.onclose = null;
      if (socket.readyState === 0 || socket.readyState === 1) socket.close();
    }
    this.preconnectFrames = [];
    this.preconnectBytes = 0;
  }
}

export function createStreamingSTTSession(options) {
  return new StreamingSTTSession(options);
}
