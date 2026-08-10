const MAX_BUFFERED_AUDIO_BYTES = 160_000;
const PARTIAL_RENDER_INTERVAL_MS = 120;
const API_BASE = import.meta.env.VITE_API_BASE || '';

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

export async function createPCM16Capture({ onFrame, onLevel, onSuspended }) {
  if (!isStreamingSTTSupported()) {
    throw new Error('当前浏览器不支持流式语音输入');
  }
  const stream = await navigator.mediaDevices.getUserMedia({
    audio: {
      channelCount: 1,
      echoCancellation: true,
      noiseSuppression: true,
      autoGainControl: true,
    },
  });
  const AudioContextClass = globalThis.AudioContext || globalThis.webkitAudioContext;
  const context = new AudioContextClass({ latencyHint: 'interactive' });
  try {
    await context.audioWorklet.addModule('/stt-pcm-worklet.js');
    const source = context.createMediaStreamSource(stream);
    const worklet = new AudioWorkletNode(context, 'catsco-pcm16-capture', {
      numberOfInputs: 1,
      numberOfOutputs: 0,
      channelCount: 1,
    });
    worklet.port.onmessage = (event) => {
      if (event.data instanceof ArrayBuffer) onFrame(event.data);
      if (event.data?.type === 'level') onLevel?.(event.data.rms);
    };
    source.connect(worklet);
    context.onstatechange = () => {
      if (context.state === 'suspended' || context.state === 'interrupted') onSuspended?.();
    };
    if (context.state === 'suspended') await context.resume();

    let stopped = false;
    return {
      stop() {
        if (stopped) return;
        stopped = true;
        worklet.port.onmessage = null;
        context.onstatechange = null;
        source.disconnect();
        worklet.disconnect();
        stream.getTracks().forEach((track) => track.stop());
        void context.close();
      },
    };
  } catch (error) {
    stream.getTracks().forEach((track) => track.stop());
    void context.close();
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
    this.lastPartialAt = 0;
    this.durationTimer = null;
    this.handleVisibilityChange = () => {
      if (document.visibilityState === 'hidden') void this.stop();
    };
  }

  setState(state) {
    this.state = state;
    this.onState(state);
  }

  async start() {
    if (this.state !== 'idle') return;
    this.setState('starting');
    try {
      const capture = await this.createCapture({
        onFrame: (frame) => this.handleFrame(frame),
        onLevel: (rms) => this.publishAudioLevel(rms),
        onSuspended: () => void this.stop(),
      });
      if (this.terminal) {
        capture.stop();
        return;
      }
      this.capture = capture;
      document.addEventListener('visibilitychange', this.handleVisibilityChange);
      const session = await this.createSession();
      if (this.terminal) return;
      const maxSeconds = Math.max(1, Number(session.max_session_seconds) || 90);
      this.durationTimer = window.setTimeout(() => void this.stop(), maxSeconds * 1000);
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
      this.fail(this.normalizeStartError(error));
    }
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
      this.lastPartialAt = Date.now();
      this.onPartial(this.pendingPartial);
      return;
    }
    if (this.partialTimer) return;
    this.partialTimer = window.setTimeout(() => {
      this.partialTimer = null;
      this.lastPartialAt = Date.now();
      this.onPartial(this.pendingPartial);
    }, PARTIAL_RENDER_INTERVAL_MS - elapsed);
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
    this.capture?.stop();
    this.capture = null;
    this.stopRequested = true;
    this.setState('finalizing');
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
