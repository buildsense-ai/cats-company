import PCM_WORKLET_URL from './stt-pcm-worklet.js?url&no-inline';
import { getToken } from './auth-session';

const MAX_BUFFERED_AUDIO_BYTES = 160_000;
const MAX_PRE_ROLL_AUDIO_BYTES = 16_000;
const CAPTURE_FLUSH_TIMEOUT_MS = 300;
const FINALIZATION_TIMEOUT_MS = 5_000;
const DURATION_WARNING_WINDOW_MS = 10_000;
const DURATION_ACTIVITY_WINDOW_MS = 1_500;
const DURATION_QUIET_WINDOW_MS = 900;
const DURATION_UNPUNCTUATED_QUIET_WINDOW_MS = 2_000;
const DURATION_BOUNDARY_POLL_MS = 250;
const STT_VOICE_RMS_THRESHOLD = 0.008;
const HARD_DURATION_STOP_REASONS = new Set(['hard_timeout', 'audio_limit', 'duration_limit']);
const RECOVERABLE_FINAL_BOUNDARY_REASONS = new Set([
  'hard_timeout',
  'audio_limit',
  'duration_limit',
  'idle_timeout',
]);
const STT_LIFECYCLE_STOP_REASON = 'lifecycle_stop';
const API_BASE = import.meta.env.VITE_API_BASE || '';
const TRANSCRIPT_BOUNDARY_PUNCTUATION = /[\s,.;:!?，。；：！？、]/u;
const TRANSCRIPT_BOUNDARY_PUNCTUATION_GLOBAL = /[\s,.;:!?，。；：！？、]/gu;
const TRANSCRIPT_TERMINAL_PUNCTUATION = /[.!?。！？；;]$/u;
const CUMULATIVE_SNAPSHOT_PREFIX_CHARACTERS = 3;

function isHardDurationStopReason(reason) {
  return HARD_DURATION_STOP_REASONS.has(String(reason || ''));
}

function isRecoverableFinalBoundaryReason(reason) {
  return RECOVERABLE_FINAL_BOUNDARY_REASONS.has(String(reason || ''));
}

function scheduleFrame(callback) {
  if (typeof globalThis.requestAnimationFrame === 'function') {
    return globalThis.requestAnimationFrame(callback);
  }
  return globalThis.setTimeout(() => callback(Date.now()), 0);
}

function cancelFrame(handle) {
  if (handle === null || handle === undefined) return;
  if (typeof globalThis.cancelAnimationFrame === 'function') {
    globalThis.cancelAnimationFrame(handle);
    return;
  }
  globalThis.clearTimeout(handle);
}

function isPageHidden() {
  return typeof document !== 'undefined' && document.visibilityState === 'hidden';
}

let reusableMicrophoneStream = null;
let pendingMicrophoneRequest = null;
let reusableMicrophoneConsumers = 0;
let microphoneRequestGeneration = 0;
let microphoneLifecycleCleanupInstalled = false;

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

function longestOverlap(left, right) {
  const maxLength = Math.min(left.length, right.length);
  for (let length = maxLength; length > 0; length -= 1) {
    const leftStart = left.length - length;
    const rightEnd = length;
    const leftBoundary = leftStart === 0 || !/^[\uDC00-\uDFFF]$/.test(left[leftStart]);
    const rightBoundary = rightEnd === right.length || !/^[\uDC00-\uDFFF]$/.test(right[rightEnd]);
    const overlap = left.slice(leftStart);
    const isSafeOverlap = Array.from(overlap).length >= 2 || TRANSCRIPT_BOUNDARY_PUNCTUATION.test(overlap);
    if (leftBoundary && rightBoundary && isSafeOverlap && overlap === right.slice(0, rightEnd)) return length;
  }
  return 0;
}

function sharedPrefixCharacterCount(left, right) {
  const leftCharacters = Array.from(left);
  const rightCharacters = Array.from(right);
  const maxLength = Math.min(leftCharacters.length, rightCharacters.length);
  let length = 0;
  while (length < maxLength && leftCharacters[length] === rightCharacters[length]) length += 1;
  return length;
}

function hasNormalizedPrefix(left, right) {
  const normalizedLeft = left.replace(TRANSCRIPT_BOUNDARY_PUNCTUATION_GLOBAL, '');
  const normalizedRight = right.replace(TRANSCRIPT_BOUNDARY_PUNCTUATION_GLOBAL, '');
  return Boolean(
    normalizedLeft
    && normalizedRight
    && (normalizedRight.startsWith(normalizedLeft) || normalizedLeft.startsWith(normalizedRight)),
  );
}

function mergeTranscriptText(committed, incoming) {
  const next = String(incoming || '');
  if (!next) return committed;
  if (!committed || next.startsWith(committed)) return next;
  if (committed.startsWith(next)) return committed;
  return committed + next.slice(longestOverlap(committed, next));
}

function mergeTranscriptPreview(committed, incoming) {
  const next = String(incoming || '');
  if (!next || !committed) return next || committed;
  if (next.startsWith(committed)) return next;
  if (committed.startsWith(next)) return committed;

  // Doubao may follow a stable utterance with either the next segment alone
  // or a refreshed cumulative snapshot. A punctuation-insensitive prefix or
  // a material literal prefix means the latter, including wording revisions
  // inside that snapshot. Treat it as replacement before concatenation.
  const sharedPrefixLength = sharedPrefixCharacterCount(committed, next);
  if (
    hasNormalizedPrefix(committed, next)
    || sharedPrefixLength >= CUMULATIVE_SNAPSHOT_PREFIX_CHARACTERS
  ) return next;

  return mergeTranscriptText(committed, next);
}

export class StreamingTranscript {
  constructor() {
    this.definiteText = '';
    this.partialText = '';
  }

  updatePartial(text) {
    this.partialText = String(text || '');
    return this.preview();
  }

  updateDefinite(text) {
    const next = String(text || '').trim();
    // A Doubao two-pass response carries the complete current result in
    // result.text. Treat the definite channel as an authoritative snapshot,
    // just as Koe does, rather than joining utterances client-side.
    if (next) this.definiteText = next;
    this.partialText = '';
    return this.preview();
  }

  preview() {
    if (!this.definiteText) return this.partialText;
    if (!this.partialText) return this.definiteText;
    return mergeTranscriptPreview(this.definiteText, this.partialText);
  }

  finalize(text) {
    return String(text || '').trim() || this.preview();
  }
}

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
  const token = getToken();
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
  assertMicrophoneCaptureActive(microphoneRequestGeneration);
  const stream = await acquireReusableMicrophoneStream();
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
    this.onDurationWarning = options.onDurationWarning || (() => {});
    this.onDurationLimit = options.onDurationLimit || (() => {});
    this.onFinal = options.onFinal || (() => {});
    this.onError = options.onError || (() => {});
    this.socket = null;
    this.capture = null;
    this.sessionPromise = null;
    this.capturePromise = null;
    this.captureStopPromise = null;
    this.captureReadyPromise = null;
    this.preparedPromise = null;
    this.startPromise = null;
    this.stopPromise = null;
    this.captureError = null;
    this.state = 'idle';
    this.activated = false;
    this.ready = false;
    this.stopRequested = false;
    this.sendStopRequested = false;
    this.captureStopped = false;
    this.stopSent = false;
    this.terminal = false;
    this.finalReceived = false;
    this.acceptingAudio = true;
    this.preconnectFrames = [];
    this.preconnectBytes = 0;
    this.transcript = new StreamingTranscript();
    this.partialFrame = null;
    this.finalFrame = null;
    this.finalFallbackTimer = null;
    this.pendingFinalFinish = null;
    this.pendingPartial = '';
    this.lastPublishedPartial = '';
    this.durationTimer = null;
    this.durationWarningTimer = null;
    this.durationBoundaryTimer = null;
    this.durationDeadlineAt = null;
    this.durationWarningSent = false;
    this.durationWarningInputState = null;
    this.durationBoundaryQuietSince = null;
    this.durationLimitReached = false;
    this.lastVoiceAt = null;
    this.lastTranscriptAt = null;
    this.sawSpeechActivity = false;
    this.stopReason = null;
    this.finalizationTimer = null;
    this.handleLifecycleEnd = () => {
      this.acceptingAudio = false;
      if (this.finalReceived) {
        this.pendingFinalFinish?.();
        return;
      }
      if (this.activated) void this.stop(STT_LIFECYCLE_STOP_REASON);
      else this.cancel();
    };
    this.handleVisibilityChange = () => {
      if (!isPageHidden()) return;
      this.handleLifecycleEnd();
    };
    this.handlePageHide = () => {
      this.handleLifecycleEnd();
    };
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
    const previousDurationLimitReached = this.durationLimitReached;
    const previousStopRequested = this.stopRequested;
    const previousStopReason = this.stopReason;
    this.clearDurationTimers();
    this.durationDeadlineAt = Date.now() + maxMilliseconds;
    this.durationWarningSent = false;
    this.durationWarningInputState = null;
    this.durationBoundaryQuietSince = null;
    this.durationLimitReached = previousDurationLimitReached;
    this.stopReason = previousDurationLimitReached
      ? 'duration_limit'
      : (previousStopRequested ? previousStopReason : null);

    const warningDelay = Math.max(0, maxMilliseconds - DURATION_WARNING_WINDOW_MS);
    this.durationWarningTimer = window.setTimeout(
      () => this.handleDurationWarning(),
      warningDelay,
    );
    this.durationTimer = window.setTimeout(
      () => this.handleDurationLimitDeadline(),
      maxMilliseconds,
    );
  }

  clearDurationTimers() {
    if (this.durationTimer !== null) window.clearTimeout(this.durationTimer);
    if (this.durationWarningTimer !== null) window.clearTimeout(this.durationWarningTimer);
    if (this.durationBoundaryTimer !== null) window.clearTimeout(this.durationBoundaryTimer);
    this.durationTimer = null;
    this.durationWarningTimer = null;
    this.durationBoundaryTimer = null;
  }

  markVoiceActivity() {
    const now = Date.now();
    this.lastVoiceAt = now;
    this.sawSpeechActivity = true;
    return now;
  }

  markTranscriptActivity() {
    this.lastTranscriptAt = Date.now();
    this.sawSpeechActivity = true;
  }

  hasRecentSpeechActivity(now = Date.now()) {
    const lastActivityAt = Math.max(
      this.lastVoiceAt ?? Number.NEGATIVE_INFINITY,
      this.lastTranscriptAt ?? Number.NEGATIVE_INFINITY,
    );
    return Number.isFinite(lastActivityAt)
      && now - lastActivityAt <= DURATION_ACTIVITY_WINDOW_MS;
  }

  hasReachedDurationDeadline(now = Date.now()) {
    return this.durationDeadlineAt !== null && now >= this.durationDeadlineAt;
  }

  hasTranscriptBoundary() {
    return TRANSCRIPT_TERMINAL_PUNCTUATION.test(this.transcript.preview().trim());
  }

  checkDurationClock() {
    if (this.terminal || this.finalReceived || !this.durationDeadlineAt) return;
    const remainingMs = this.durationDeadlineAt - Date.now();
    if (this.hasReachedDurationDeadline()) {
      if (this.stopRequested) {
        this.markLifecycleBoundaryIfExpired();
      } else {
        this.handleDurationLimitDeadline();
      }
      return;
    }
    if (this.stopRequested) return;
    if (!this.durationWarningSent && remainingMs <= DURATION_WARNING_WINDOW_MS) {
      this.handleDurationWarning();
      return;
    }
    if (this.durationWarningSent) this.emitDurationWarning(remainingMs);
  }

  emitDurationWarning(remainingMs = this.durationDeadlineAt - Date.now()) {
    if (!this.durationWarningSent || !this.durationDeadlineAt) return;
    const hasRecentInput = this.hasRecentSpeechActivity();
    if (this.durationWarningInputState === hasRecentInput) return;
    this.durationWarningInputState = hasRecentInput;
    this.onDurationWarning({
      remainingMs: Math.max(0, remainingMs),
      hasRecentInput,
    });
  }

  handleDurationWarning() {
    if (this.durationWarningTimer !== null) window.clearTimeout(this.durationWarningTimer);
    this.durationWarningTimer = null;
    if (
      this.terminal
      || this.stopRequested
      || this.finalReceived
      || !this.durationDeadlineAt
      || this.durationWarningSent
    ) return;
    const remainingMs = this.durationDeadlineAt - Date.now();
    if (remainingMs <= 0) {
      this.handleDurationLimitDeadline();
      return;
    }
    if (!this.ready) {
      const retryDelay = Math.min(250, remainingMs);
      this.durationWarningTimer = window.setTimeout(() => this.handleDurationWarning(), retryDelay);
      return;
    }
    this.durationWarningSent = true;
    this.emitDurationWarning(remainingMs);
    this.scheduleDurationBoundaryCheck();
  }

  scheduleDurationBoundaryCheck() {
    if (
      this.durationBoundaryTimer !== null
      || this.terminal
      || this.stopRequested
      || this.finalReceived
      || !this.durationWarningSent
    ) return;
    this.durationBoundaryTimer = window.setTimeout(() => {
      this.durationBoundaryTimer = null;
      this.checkDurationBoundary();
    }, DURATION_BOUNDARY_POLL_MS);
  }

  checkDurationBoundary() {
    if (this.terminal || this.stopRequested || this.finalReceived || !this.durationDeadlineAt) return;
    const now = Date.now();
    if (this.hasReachedDurationDeadline(now)) {
      this.handleDurationLimitDeadline();
      return;
    }

    this.emitDurationWarning(this.durationDeadlineAt - now);

    if (this.hasRecentSpeechActivity(now)) {
      this.durationBoundaryQuietSince = null;
    } else if (!this.sawSpeechActivity) {
      this.durationBoundaryQuietSince = null;
    } else if (this.durationBoundaryQuietSince === null) {
      this.durationBoundaryQuietSince = now;
    } else if (
      now - this.durationBoundaryQuietSince >= (
        this.hasTranscriptBoundary()
          ? DURATION_QUIET_WINDOW_MS
          : DURATION_UNPUNCTUATED_QUIET_WINDOW_MS
      )
    ) {
      this.beginDurationLimitStop({ stoppedAtNaturalBoundary: true });
      return;
    }
    this.scheduleDurationBoundaryCheck();
  }

  handleDurationLimitDeadline() {
    this.durationTimer = null;
    if (this.terminal || this.finalReceived) return;
    if (this.stopRequested) {
      this.markLifecycleBoundaryIfExpired();
      return;
    }
    this.beginDurationLimitStop({ stoppedAtNaturalBoundary: false });
  }

  markLifecycleBoundaryIfExpired() {
    if (
      this.stopReason !== STT_LIFECYCLE_STOP_REASON
      || !this.hasReachedDurationDeadline()
    ) return false;
    this.recordDurationBoundary({ stoppedAtNaturalBoundary: false, reason: 'hard_timeout' });
    return true;
  }

  recordDurationBoundary({ stoppedAtNaturalBoundary = false, reason = 'duration_limit' } = {}) {
    if (
      this.terminal
      || this.finalReceived
      || this.durationLimitReached
      || (this.stopRequested && reason === 'duration_limit')
    ) return false;
    this.durationLimitReached = true;
    this.stopReason = reason;
    this.clearDurationTimers();
    this.onDurationLimit({
      hadRecentInput: this.hasRecentSpeechActivity(),
      stoppedAtNaturalBoundary,
    });
    return true;
  }

  beginDurationLimitStop({ stoppedAtNaturalBoundary = false } = {}) {
    if (this.recordDurationBoundary({ stoppedAtNaturalBoundary, reason: 'duration_limit' }) !== false) {
      void this.stop('duration_limit');
    }
  }

  applyDurationLimit(payload, fallbackMilliseconds = null) {
    const hasMilliseconds = Object.hasOwn(payload || {}, 'max_session_ms');
    const hasSeconds = Object.hasOwn(payload || {}, 'max_session_seconds');
    if (!hasMilliseconds && !hasSeconds && fallbackMilliseconds === null) return;

    const milliseconds = hasMilliseconds
      ? payload.max_session_ms
      : hasSeconds
        ? Number(payload.max_session_seconds) * 1000
        : fallbackMilliseconds;
    this.setDurationLimit(Number.isFinite(Number(milliseconds)) ? milliseconds : (fallbackMilliseconds || 150_000));
  }

  prepare() {
    if (this.preparedPromise) return this.preparedPromise;
    this.installLifecycleListeners();
    if (isPageHidden()) {
      this.completeWithoutCapture();
      this.preparedPromise = Promise.resolve();
      return this.preparedPromise;
    }
    const invoke = (callback) => {
      try {
        return Promise.resolve(callback());
      } catch (error) {
        return Promise.reject(error);
      }
    };
    this.sessionPromise = invoke(() => this.createSession());
    this.capturePromise = invoke(() => this.createCapture({
      onFrame: (frame) => this.handleFrame(frame),
      onLevel: (rms) => this.publishAudioLevel(rms),
      onSuspended: () => this.handleCaptureSuspended(),
    }));
    this.sessionPromise.catch((error) => {
      if (this.activated && !this.terminal) this.fail(this.normalizeStartError(error));
    });
    this.captureReadyPromise = this.capturePromise.then(
      (capture) => this.handleCaptureReady(capture),
      (error) => {
        this.captureError = error;
        if (this.activated && !this.terminal) {
          if (this.stopRequested || isPageHidden() || isMicrophoneLifecycleAbort(error)) {
            this.completeWithoutCapture();
          } else {
            this.fail(this.normalizeStartError(error));
          }
        }
        throw error;
      },
    );
    this.preparedPromise = Promise.all([this.sessionPromise, this.captureReadyPromise]);
    this.preparedPromise.catch(() => {});
    return this.preparedPromise;
  }

  async start() {
    if (this.terminal || this.state === 'complete') return;
    if (isPageHidden()) {
      this.completeWithoutCapture();
      return;
    }
    this.activated = true;
    if (this.state === 'idle') this.setState('starting');
    this.prepare();
    if (!this.startPromise) this.startPromise = this.connectWhenAdmitted();
    await this.startPromise;
  }

  async connectWhenAdmitted() {
    try {
      const session = await this.sessionPromise;
      if (this.terminal) return;
      if (this.captureError) throw this.captureError;
      this.applyDurationLimit(session, 150_000);
      this.setState(this.stopRequested ? 'finalizing' : 'connecting');
      const socket = this.createWebSocket(this.resolveWebSocketURL(session.ticket));
      this.socket = socket;
      socket.binaryType = 'arraybuffer';
      socket.onmessage = (event) => this.handleMessage(event.data);
      socket.onerror = () => {
        if (!this.finalReceived) this.fail(new Error('语音识别连接失败'));
      };
      socket.onclose = () => {
        if (!this.terminal && !this.finalReceived) this.fail(new Error('语音识别连接已断开'));
      };
    } catch (error) {
      if (this.stopRequested || isMicrophoneLifecycleAbort(error)) {
        if (!this.terminal) this.completeWithoutCapture();
        return;
      }
      if (!this.terminal) this.fail(this.normalizeStartError(error));
    }
  }

  async handleCaptureReady(capture) {
    this.capture = capture;
    if (this.terminal) {
      await this.stopCapture();
      return capture;
    }
    if (isPageHidden()) {
      this.handleVisibilityChange();
      return capture;
    }
    this.syncReadyState();
    return capture;
  }

  handleCaptureSuspended() {
    // A suspended/interrupted context can still flush an already queued
    // worklet frame while stop() runs. That audio was sampled after the
    // interruption boundary from the browser's point of view, so never send
    // it to CatsCo.
    this.acceptingAudio = false;
    if (this.activated) void this.stop(STT_LIFECYCLE_STOP_REASON);
    else this.cancel();
  }

  syncReadyState() {
    if (!this.ready || !this.capture || this.terminal) return;
    this.setState(this.stopRequested ? 'finalizing' : 'recording');
  }

  normalizeStartError(error) {
    if (error?.name === 'NotAllowedError') return new Error('需要麦克风权限才能使用语音输入');
    if (error?.status === 429) return new Error('语音输入额度已用完，请稍后再试');
    if (error?.status === 409) return new Error('已有语音输入正在进行');
    return error instanceof Error ? error : new Error('无法启动语音输入');
  }

  handleFrame(frame) {
    if (this.terminal || !this.acceptingAudio || !(frame instanceof ArrayBuffer) || frame.byteLength === 0) return;
    if (isPageHidden()) {
      this.handleVisibilityChange();
      return;
    }
    if (!this.ready) {
      this.bufferPreconnectFrame(frame, this.activated ? MAX_BUFFERED_AUDIO_BYTES : MAX_PRE_ROLL_AUDIO_BYTES);
      return;
    }
    this.sendAudio(frame);
  }

  bufferPreconnectFrame(frame, limit) {
    if (frame.byteLength > limit || (this.activated && this.preconnectBytes + frame.byteLength > limit)) {
      this.fail(new Error('语音识别连接超时，请重试'));
      return;
    }
    while (this.preconnectFrames.length > 0 && this.preconnectBytes + frame.byteLength > limit) {
      this.preconnectBytes -= this.preconnectFrames.shift().byteLength;
    }
    this.preconnectFrames.push(frame);
    this.preconnectBytes += frame.byteLength;
  }

  publishAudioLevel(rms) {
    if (this.terminal) return;
    if (Number(rms) >= STT_VOICE_RMS_THRESHOLD) this.markVoiceActivity();
    this.checkDurationClock();
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
    if (this.terminal || this.finalReceived) return;
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
        this.checkDurationClock();
        this.syncReadyState();
        this.drainPreconnectFrames();
        this.maybeSendStop();
        break;
      case 'partial':
        if (message.text) this.markTranscriptActivity();
        this.checkDurationClock();
        this.publishPartial(this.transcript.updatePartial(message.text));
        break;
      case 'definite':
        if (message.text) this.markTranscriptActivity();
        this.checkDurationClock();
        this.publishPartial(this.transcript.updateDefinite(message.text));
        break;
      case 'final': {
        const previousStopReason = this.stopReason;
        if (message.stop_reason) {
          this.stopReason = message.stop_reason;
          if (
            message.stop_reason === 'client_stop'
            && (previousStopReason === STT_LIFECYCLE_STOP_REASON || previousStopReason === 'duration_limit')
          ) {
            this.stopReason = previousStopReason;
          }
          if (isHardDurationStopReason(message.stop_reason)) {
            this.durationLimitReached = true;
          }
        }
        const shouldCheckLocalDeadline = !message.stop_reason
          || message.stop_reason === STT_LIFECYCLE_STOP_REASON
          || (message.stop_reason === 'client_stop'
            && (previousStopReason === STT_LIFECYCLE_STOP_REASON || this.durationLimitReached));
        if (shouldCheckLocalDeadline) {
          // A throttled foreground timer may not run before the provider sends
          // its terminal frame. Classify the local deadline when the server
          // did not provide a more specific boundary reason.
          this.checkDurationClock();
        }
        this.finishWithFinal(this.transcript.finalize(message.text));
        break;
      }
      case 'error':
        if (!this.finalReceived) this.fail(this.normalizeRealtimeError(message));
        break;
      default:
        break;
    }
  }

  drainPreconnectFrames() {
    for (const frame of this.preconnectFrames) {
      if (this.terminal) return;
      this.sendAudio(frame);
    }
    this.preconnectFrames = [];
    this.preconnectBytes = 0;
  }

  publishPartial(text) {
    this.pendingPartial = String(text || '');
    if (this.partialFrame !== null) return;
    this.partialFrame = scheduleFrame(() => {
      this.partialFrame = null;
      this.flushPartial();
    });
  }

  flushPartial() {
    this.clearPartialFrame();
    const text = this.pendingPartial;
    this.pendingPartial = '';
    if (!text || text === this.lastPublishedPartial) return;
    this.lastPublishedPartial = text;
    this.onPartial(text);
  }

  clearPartialFrame() {
    cancelFrame(this.partialFrame);
    this.partialFrame = null;
  }

  transcriptSnapshot() {
    // The transcript model is updated before a partial is coalesced for
    // painting. Keep the coalesced values as a defensive fallback for custom
    // transports and for a final frame that races with teardown.
    const candidates = [
      this.transcript.preview(),
      this.pendingPartial,
      this.lastPublishedPartial,
    ];
    for (const candidate of candidates) {
      const text = String(candidate || '').trim();
      if (text) return text;
    }
    return '';
  }

  finishWithFinal(text) {
    if (this.terminal || this.finalFrame !== null) return;
    this.finalReceived = true;
    const needsPreviewPaint = Boolean(this.pendingPartial && this.pendingPartial !== this.lastPublishedPartial);
    this.flushPartial();
    this.stopRequested = true;
    this.acceptingAudio = false;
    this.setState('finalizing');
    void this.stopCapture();
    const finish = () => {
      if (this.terminal) return;
      this.pendingFinalFinish = null;
      if (this.finalFallbackTimer !== null) window.clearTimeout(this.finalFallbackTimer);
      this.finalFallbackTimer = null;
      cancelFrame(this.finalFrame);
      this.finalFrame = null;
      this.terminal = true;
      this.cleanup();
      this.setState('complete');
      // The provider may legitimately finish with no recognized text (for
      // example, after silence). Composer uses this callback to release its
      // session reference, while its caller already ignores an empty draft.
      const boundaryReason = isRecoverableFinalBoundaryReason(this.stopReason)
        ? this.stopReason
        : null;
      const finalReason = boundaryReason
        ? (isHardDurationStopReason(boundaryReason) ? 'duration_limit' : boundaryReason)
        : (this.durationLimitReached ? 'duration_limit' : (this.stopReason || 'complete'));
      this.onFinal(text, {
        reason: finalReason,
      });
    };
    if (!needsPreviewPaint) {
      finish();
      return;
    }
    this.pendingFinalFinish = finish;
    if (isPageHidden()) {
      finish();
      return;
    }
    this.finalFrame = scheduleFrame(() => {
      this.finalFrame = scheduleFrame(() => {
        finish();
      });
    });
    this.finalFallbackTimer = window.setTimeout(() => {
      this.finalFallbackTimer = null;
      cancelFrame(this.finalFrame);
      this.finalFrame = null;
      finish();
    }, 250);
  }

  sendControl(type, payload = null) {
    if (this.socket?.readyState !== 1) return;
    this.socket.send(JSON.stringify(payload ? { type, ...payload } : { type }));
  }

  normalizeRealtimeError(message) {
    let messageText;
    switch (message?.code) {
      case 'quota_exhausted':
        messageText = '语音输入额度已用完，请稍后再试';
        break;
      case 'session_active':
        messageText = '已有语音输入正在进行';
        break;
      case 'capacity_full':
        messageText = '语音输入服务繁忙，请稍后再试';
        break;
      case 'final_timeout':
        messageText = '语音识别结束超时，请重试';
        break;
      default:
        messageText = message?.message || '语音识别失败';
        break;
    }
    const error = new Error(messageText);
    error.code = message?.code || '';
    error.stopReason = message?.stop_reason || '';
    return error;
  }

  startFinalizationTimer() {
    if (this.finalizationTimer !== null || this.terminal || this.finalReceived) return;
    this.finalizationTimer = window.setTimeout(() => {
      this.finalizationTimer = null;
      this.fail(new Error('语音识别结束超时，请重试'));
    }, FINALIZATION_TIMEOUT_MS);
  }

  clearFinalizationTimer() {
    if (this.finalizationTimer !== null) window.clearTimeout(this.finalizationTimer);
    this.finalizationTimer = null;
  }

  maybeSendStop() {
    if (!this.ready || !this.sendStopRequested || !this.captureStopped || this.stopSent || this.terminal) return;
    this.stopSent = true;
    const stopReason = this.durationLimitReached
      ? 'duration_limit'
      : this.stopReason === STT_LIFECYCLE_STOP_REASON
        ? STT_LIFECYCLE_STOP_REASON
        : null;
    this.sendControl('stop', stopReason ? { stop_reason: stopReason } : null);
  }

  async stop(reason = 'user_stop') {
    if (reason && this.stopReason === null) this.stopReason = reason;
    if (this.terminal || this.state === 'complete') return;
    if (this.stopRequested) return this.stopPromise;
    this.stopRequested = true;
    this.sendStopRequested = true;
    this.setState('finalizing');
    this.startFinalizationTimer();
    this.stopPromise = this.stopCapture();
    await this.stopPromise;
  }

  stopCapture() {
    if (this.captureStopPromise) return this.captureStopPromise;
    if (!this.capturePromise) {
      this.captureStopped = true;
      this.maybeSendStop();
      return Promise.resolve();
    }
    const stop = (capture) => capture?.stop?.();
    const stopping = this.capture ? stop(this.capture) : this.capturePromise.then(stop);
    this.captureStopPromise = Promise.resolve(stopping)
      .catch((error) => {
        if (!this.terminal && !this.finalReceived) this.fail(this.normalizeStartError(error));
      })
      .finally(() => {
        this.captureStopped = true;
        this.capture = null;
        this.maybeSendStop();
      });
    return this.captureStopPromise;
  }

  cancel() {
    if (this.terminal) return;
    this.terminal = true;
    this.sendControl('cancel');
    this.cleanup();
    this.setState('cancelled');
  }

  fail(error) {
    if (this.terminal || this.finalReceived) return;
    const normalized = error instanceof Error ? error : new Error('语音识别失败');
    const reportedStopReason = normalized.stopReason || '';
    const lifecycleBoundaryAlreadyReached = reportedStopReason === STT_LIFECYCLE_STOP_REASON
      && this.durationLimitReached;
    const implicitClientStop = reportedStopReason === 'client_stop'
      && (this.stopReason === STT_LIFECYCLE_STOP_REASON
        || this.stopReason === 'duration_limit'
        || this.durationLimitReached);
    let serverStopReason = reportedStopReason;
    if (lifecycleBoundaryAlreadyReached) serverStopReason = 'duration_limit';
    else if (implicitClientStop) serverStopReason = this.stopReason;
    const reachedServerBoundary = isHardDurationStopReason(serverStopReason);
    const serverLifecycleStop = serverStopReason === STT_LIFECYCLE_STOP_REASON;
    const reachedClientBoundary = this.hasReachedDurationDeadline()
      && (!serverStopReason || serverLifecycleStop)
      && (!this.stopRequested || this.stopReason === STT_LIFECYCLE_STOP_REASON);
    if (reachedClientBoundary) {
      this.recordDurationBoundary({ stoppedAtNaturalBoundary: false, reason: 'hard_timeout' });
    }
    const transcript = this.transcriptSnapshot();
    // Publish the last coalesced preview before tearing down the session. The
    // composer receives the snapshot as a second argument as well, because a
    // React state update from onPartial may still be batched with onError.
    this.flushPartial();
    this.terminal = true;
    this.cleanup();
    this.setState('error');
    const terminalBoundaryReached = reachedServerBoundary
      || reachedClientBoundary
      || (serverStopReason === STT_LIFECYCLE_STOP_REASON && this.durationLimitReached);
    const errorReason = terminalBoundaryReached
      ? 'duration_limit'
      : serverStopReason || (this.durationLimitReached ? 'duration_limit' : (this.stopReason || 'error'));
    this.onError(normalized, transcript, {
      reason: errorReason,
    });
  }

  cleanup() {
    this.clearPartialFrame();
    cancelFrame(this.finalFrame);
    this.finalFrame = null;
    if (this.finalFallbackTimer !== null) window.clearTimeout(this.finalFallbackTimer);
    this.finalFallbackTimer = null;
    this.pendingFinalFinish = null;
    this.clearDurationTimers();
    this.clearFinalizationTimer();
    document.removeEventListener('visibilitychange', this.handleVisibilityChange);
    globalThis.removeEventListener?.('pagehide', this.handlePageHide);
    this.lifecycleListenersInstalled = false;
    void this.stopCapture();
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
