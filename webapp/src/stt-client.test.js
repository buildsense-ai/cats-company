import {
  createPCM16Capture,
  releaseReusableMicrophoneStream,
  StreamingSTTSession,
  StreamingTranscript,
} from './stt-client';

class FakeWebSocket {
  static CONNECTING = 0;
  static OPEN = 1;
  static CLOSED = 3;

  constructor(url) {
    this.url = url;
    this.readyState = FakeWebSocket.CONNECTING;
    this.bufferedAmount = 0;
    this.sent = [];
  }

  open() {
    this.readyState = FakeWebSocket.OPEN;
    this.onopen?.();
  }

  receive(payload) {
    this.onmessage?.({ data: JSON.stringify(payload) });
  }

  send(payload) {
    this.sent.push(payload);
  }

  close() {
    this.readyState = FakeWebSocket.CLOSED;
    this.onclose?.();
  }
}

function createMicrophoneStreamFixture() {
  const track = {
    enabled: true,
    readyState: 'live',
    stop: vi.fn(),
    addEventListener: vi.fn(),
  };
  return {
    track,
    stream: {
      getAudioTracks: () => [track],
      getTracks: () => [track],
    },
  };
}

function installMicrophoneTestRuntime({ getUserMedia, AudioContext, AudioWorkletNode }) {
  const originalMediaDevices = Object.getOwnPropertyDescriptor(navigator, 'mediaDevices');
  const originalAudioContext = globalThis.AudioContext;
  const originalAudioWorkletNode = globalThis.AudioWorkletNode;

  Object.defineProperty(navigator, 'mediaDevices', {
    configurable: true,
    value: { getUserMedia },
  });
  globalThis.AudioContext = AudioContext;
  globalThis.AudioWorkletNode = AudioWorkletNode;

  return () => {
    releaseReusableMicrophoneStream();
    if (originalMediaDevices) Object.defineProperty(navigator, 'mediaDevices', originalMediaDevices);
    else delete navigator.mediaDevices;
    if (originalAudioContext === undefined) delete globalThis.AudioContext;
    else globalThis.AudioContext = originalAudioContext;
    if (originalAudioWorkletNode === undefined) delete globalThis.AudioWorkletNode;
    else globalThis.AudioWorkletNode = originalAudioWorkletNode;
  };
}

function setDocumentVisibility(state) {
  Object.defineProperty(document, 'visibilityState', {
    configurable: true,
    value: state,
  });
}

async function flushMicrotasks() {
  await Promise.resolve();
  await Promise.resolve();
}

function installAnimationFrameStub() {
  const callbacks = new Map();
  let nextHandle = 1;
  vi.stubGlobal('requestAnimationFrame', vi.fn((callback) => {
    const handle = nextHandle;
    nextHandle += 1;
    callbacks.set(handle, callback);
    return handle;
  }));
  vi.stubGlobal('cancelAnimationFrame', vi.fn((handle) => callbacks.delete(handle)));
  return {
    get size() {
      return callbacks.size;
    },
    runNextFrame(timestamp = 0) {
      const next = callbacks.entries().next().value;
      if (!next) throw new Error('no animation frame is pending');
      const [handle, callback] = next;
      callbacks.delete(handle);
      callback(timestamp);
    },
  };
}

describe('StreamingTranscript', () => {
  it('keeps a definite prefix visible when a later partial contains only the next utterance', () => {
    const transcript = new StreamingTranscript();

    expect(transcript.updatePartial('第一句话')).toBe('第一句话');
    expect(transcript.updateDefinite('第一句话。')).toBe('第一句话。');
    expect(transcript.updatePartial('第二句话')).toBe('第一句话。第二句话');
    expect(transcript.finalize('第一句话。第二句话。')).toBe('第一句话。第二句话。');
  });

  it('does not drop a real one-character boundary when joining utterances', () => {
    const transcript = new StreamingTranscript();

    transcript.updateDefinite('今天');
    expect(transcript.updatePartial('天气很好')).toBe('今天天气很好');
  });

  it('replaces a cumulative snapshot when the provider revises its stable prefix', () => {
    const transcript = new StreamingTranscript();

    transcript.updateDefinite('第一句已经稳定。');
    expect(transcript.updatePartial('第一句已经稳定，第二句正在识别')).toBe('第一句已经稳定，第二句正在识别');
    expect(transcript.finalize('第一句已经稳定，第二句已经完成。')).toBe('第一句已经稳定，第二句已经完成。');
  });

  it('replaces a cumulative snapshot when the provider removes an earlier punctuation mark', () => {
    const transcript = new StreamingTranscript();

    transcript.updateDefinite('你好。');
    expect(transcript.updatePartial('你好世界')).toBe('你好世界');
  });

  it('replaces a cumulative definite snapshot instead of appending it twice', () => {
    const transcript = new StreamingTranscript();

    expect(transcript.updateDefinite('第一句已经稳定。')).toBe('第一句已经稳定。');
    expect(transcript.updateDefinite('第一句已经稳定，第二句也稳定。')).toBe('第一句已经稳定，第二句也稳定。');
  });

  it('treats each definite result as the provider\'s latest complete snapshot', () => {
    const transcript = new StreamingTranscript();

    expect(transcript.updateDefinite('我去北京')).toBe('我去北京');
    expect(transcript.updateDefinite('我去上海')).toBe('我去上海');
  });

  it('uses the terminal result as the authoritative transcript', () => {
    const transcript = new StreamingTranscript();

    transcript.updateDefinite('我去北京');
    expect(transcript.finalize('我去上海')).toBe('我去上海');
  });

  it('does not let a stale shorter partial erase confirmed text', () => {
    const transcript = new StreamingTranscript();

    transcript.updateDefinite('第一句已经稳定。');
    expect(transcript.updatePartial('第一句')).toBe('第一句已经稳定。');
  });
});

describe('StreamingSTTSession', () => {
  beforeEach(() => {
    setDocumentVisibility('visible');
  });

  afterEach(() => {
    releaseReusableMicrophoneStream();
  });

  it('reuses an authorized microphone stream for consecutive foreground captures', async () => {
    const { track, stream } = createMicrophoneStreamFixture();
    const getUserMedia = vi.fn().mockResolvedValue(stream);
    const contexts = [];

    class FakeAudioContext {
      constructor() {
        this.state = 'running';
        this.audioWorklet = { addModule: vi.fn().mockResolvedValue(undefined) };
        this.close = vi.fn().mockResolvedValue(undefined);
        this.source = { connect: vi.fn(), disconnect: vi.fn() };
        contexts.push(this);
      }

      createMediaStreamSource() {
        return this.source;
      }
    }

    class FakeAudioWorkletNode {
      constructor() {
        this.port = {
          onmessage: null,
          postMessage: vi.fn((message) => {
            if (message?.type === 'flush') this.port.onmessage?.({ data: { type: 'flushed' } });
          }),
        };
        this.disconnect = vi.fn();
      }
    }

    const restoreRuntime = installMicrophoneTestRuntime({
      getUserMedia,
      AudioContext: FakeAudioContext,
      AudioWorkletNode: FakeAudioWorkletNode,
    });

    try {
      const firstCapture = await createPCM16Capture({ onFrame: vi.fn() });
      await firstCapture.stop();
      expect(track.enabled).toBe(false);

      const secondCapture = await createPCM16Capture({ onFrame: vi.fn() });
      await secondCapture.stop();

      expect(getUserMedia).toHaveBeenCalledTimes(1);
      expect(contexts).toHaveLength(2);
      expect(track.stop).not.toHaveBeenCalled();

      window.dispatchEvent(new Event('pagehide'));
      expect(track.stop).toHaveBeenCalledTimes(1);
    } finally {
      restoreRuntime();
    }
  });

  it('starts a fresh capture when a previously reusable track has ended', async () => {
    const first = createMicrophoneStreamFixture();
    const second = createMicrophoneStreamFixture();
    const getUserMedia = vi.fn()
      .mockResolvedValueOnce(first.stream)
      .mockResolvedValueOnce(second.stream);

    class FakeAudioContext {
      constructor() {
        this.state = 'running';
        this.audioWorklet = { addModule: vi.fn().mockResolvedValue(undefined) };
        this.close = vi.fn().mockResolvedValue(undefined);
        this.source = { connect: vi.fn(), disconnect: vi.fn() };
      }

      createMediaStreamSource() {
        return this.source;
      }
    }

    class FakeAudioWorkletNode {
      constructor() {
        this.port = {
          onmessage: null,
          postMessage: vi.fn((message) => {
            if (message?.type === 'flush') this.port.onmessage?.({ data: { type: 'flushed' } });
          }),
        };
        this.disconnect = vi.fn();
      }
    }

    const restoreRuntime = installMicrophoneTestRuntime({
      getUserMedia,
      AudioContext: FakeAudioContext,
      AudioWorkletNode: FakeAudioWorkletNode,
    });

    try {
      const firstCapture = await createPCM16Capture({ onFrame: vi.fn() });
      await firstCapture.stop();
      first.track.readyState = 'ended';

      const secondCapture = await createPCM16Capture({ onFrame: vi.fn() });
      await secondCapture.stop();

      expect(getUserMedia).toHaveBeenCalledTimes(2);
      expect(second.track.stop).not.toHaveBeenCalled();
    } finally {
      restoreRuntime();
    }
  });

  it('releases a microphone request that resolves after the PWA leaves the foreground', async () => {
    let resolveMicrophone;
    const { track, stream } = createMicrophoneStreamFixture();

    const getUserMedia = vi.fn(() => new Promise((resolve) => { resolveMicrophone = resolve; }));
    const restoreRuntime = installMicrophoneTestRuntime({
      getUserMedia,
      AudioContext: class FakeAudioContext {},
      AudioWorkletNode: class FakeAudioWorkletNode {},
    });

    try {
      const pendingCapture = createPCM16Capture({ onFrame: vi.fn() });
      expect(getUserMedia).toHaveBeenCalledTimes(1);

      window.dispatchEvent(new Event('pagehide'));
      resolveMicrophone(stream);

      await expect(pendingCapture).rejects.toMatchObject({ name: 'AbortError' });
      expect(track.stop).toHaveBeenCalledTimes(1);
    } finally {
      restoreRuntime();
    }
  });

  it('aborts when the PWA leaves the foreground during AudioWorklet initialization', async () => {
    let resolveWorklet;
    const { track, stream } = createMicrophoneStreamFixture();
    const getUserMedia = vi.fn().mockResolvedValue(stream);

    class DelayedAudioContext {
      constructor() {
        this.state = 'running';
        this.audioWorklet = {
          addModule: vi.fn(() => new Promise((resolve) => { resolveWorklet = resolve; })),
        };
        this.close = vi.fn().mockResolvedValue(undefined);
      }
    }

    const restoreRuntime = installMicrophoneTestRuntime({
      getUserMedia,
      AudioContext: DelayedAudioContext,
      AudioWorkletNode: class FakeAudioWorkletNode {},
    });

    try {
      const pendingCapture = createPCM16Capture({ onFrame: vi.fn() });
      await vi.waitFor(() => expect(resolveWorklet).toBeTypeOf('function'));
      window.dispatchEvent(new Event('pagehide'));
      resolveWorklet();

      await expect(pendingCapture).rejects.toMatchObject({ name: 'AbortError' });
      expect(track.stop).toHaveBeenCalledTimes(1);
    } finally {
      restoreRuntime();
    }
  });

  it('starts microphone capture while session admission is in flight', async () => {
    const order = [];
    const session = new StreamingSTTSession({
      createSession: vi.fn(async () => {
        order.push('session');
        return { ticket: 'ticket-auth-first' };
      }),
      createCapture: vi.fn(async () => {
        order.push('capture');
        return { stop: vi.fn() };
      }),
      createWebSocket: () => {
        order.push('socket');
        return new FakeWebSocket('wss://app.catsco.cc/api/stt/realtime');
      },
    });

    await session.start();

    expect(order).toEqual(['session', 'capture', 'socket']);
    session.cancel();
  });

  it('opens the browser websocket as soon as admission completes without waiting for capture setup', async () => {
    let resolveCapture;
    let socket;
    const capture = { stop: vi.fn().mockResolvedValue(undefined) };
    const session = new StreamingSTTSession({
      createSession: vi.fn().mockResolvedValue({ ticket: 'ticket-connect-early' }),
      createCapture: vi.fn(() => new Promise((resolve) => { resolveCapture = resolve; })),
      createWebSocket: () => {
        socket = new FakeWebSocket('wss://app.catsco.cc/api/stt/realtime');
        return socket;
      },
    });

    const starting = session.start();
    await flushMicrotasks();

    expect(socket).toBeInstanceOf(FakeWebSocket);
    resolveCapture(capture);
    await starting;
    session.cancel();
  });

  it('captures early audio while session admission is pending and forwards it after ready', async () => {
    let resolveSession;
    let emitFrame;
    let socket;
    const frame = new Uint8Array([1, 2, 3, 4]).buffer;
    const capture = { stop: vi.fn().mockResolvedValue(undefined) };
    const createCapture = vi.fn(async ({ onFrame }) => {
      emitFrame = onFrame;
      return capture;
    });
    const session = new StreamingSTTSession({
      createSession: vi.fn(() => new Promise((resolve) => { resolveSession = resolve; })),
      createCapture,
      createWebSocket: () => {
        socket = new FakeWebSocket('wss://app.catsco.cc/api/stt/realtime');
        return socket;
      },
    });

    const starting = session.start();
    await Promise.resolve();

    expect(createCapture).toHaveBeenCalledTimes(1);
    emitFrame(frame);

    resolveSession({ ticket: 'ticket-early-audio' });
    await starting;
    socket.open();
    socket.receive({ type: 'ready' });

    expect(socket.sent[0]).toBe(frame);
    session.cancel();
  });

  it('stops locally captured audio when session admission fails', async () => {
    const errors = [];
    const capture = { stop: vi.fn().mockResolvedValue(undefined) };
    const session = new StreamingSTTSession({
      createSession: vi.fn().mockRejectedValue(Object.assign(new Error('quota'), { status: 429 })),
      createCapture: vi.fn().mockResolvedValue(capture),
      onError: (error) => errors.push(error.message),
    });

    await session.start();

    expect(session.createCapture).toHaveBeenCalledTimes(1);
    expect(capture.stop).toHaveBeenCalledTimes(1);
    expect(errors).toEqual(['语音输入额度已用完，请稍后再试']);
  });

  it('buffers PCM before ready and publishes only the final transcript', async () => {
    let emitFrame;
    const capture = { stop: vi.fn() };
    const partials = [];
    const finals = [];
    const sockets = [];
    const animationFrames = installAnimationFrameStub();
    const session = new StreamingSTTSession({
      createSession: vi.fn().mockResolvedValue({ ticket: 'ticket-1', max_session_seconds: 90 }),
      createCapture: vi.fn(async ({ onFrame }) => {
        emitFrame = onFrame;
        return capture;
      }),
      createWebSocket: (url) => {
        const socket = new FakeWebSocket(url);
        sockets.push(socket);
        return socket;
      },
      resolveWebSocketURL: (ticket) => `wss://app.catsco.cc/api/stt/realtime?ticket=${ticket}`,
      onPartial: (text) => partials.push(text),
      onFinal: (text) => finals.push(text),
    });

    try {
      const starting = session.start();
      await Promise.resolve();
      emitFrame(new Uint8Array([1, 2, 3, 4]).buffer);
      await starting;

      expect(sockets).toHaveLength(1);
      expect(sockets[0].sent).toHaveLength(0);
      sockets[0].open();
      sockets[0].receive({ type: 'ready', max_session_seconds: 90 });
      expect(sockets[0].sent[0]).toBeInstanceOf(ArrayBuffer);

      sockets[0].receive({ type: 'partial', text: '你好' });
      animationFrames.runNextFrame(0);
      expect(partials).toEqual(['你好']);
      expect(finals).toEqual([]);

      sockets[0].receive({ type: 'final', text: '你好世界' });
      expect(finals).toEqual(['你好世界']);
      expect(capture.stop).toHaveBeenCalledTimes(1);
    } finally {
      session.cancel();
      vi.unstubAllGlobals();
    }
  });

  it('publishes only the newest partial on the next animation frame', async () => {
    const partials = [];
    const animationFrames = installAnimationFrameStub();
    let socket;
    const session = new StreamingSTTSession({
      createSession: vi.fn().mockResolvedValue({ ticket: 'ticket-raf', max_session_seconds: 90 }),
      createCapture: vi.fn().mockResolvedValue({ stop: vi.fn() }),
      createWebSocket: () => {
        socket = new FakeWebSocket('wss://app.catsco.cc/api/stt/realtime?ticket=ticket-raf');
        return socket;
      },
      onPartial: (text) => partials.push(text),
    });

    try {
      await session.start();
      socket.open();
      socket.receive({ type: 'ready' });
      socket.receive({ type: 'partial', text: '第一段' });
      socket.receive({ type: 'partial', text: '最新的一段' });

      expect(partials).toEqual([]);
      expect(animationFrames.size).toBe(1);
      animationFrames.runNextFrame(0);
      expect(partials).toEqual(['最新的一段']);
    } finally {
      session.cancel();
      vi.unstubAllGlobals();
    }
  });

  it('combines a stable provider prefix with the current mutable partial', async () => {
    const partials = [];
    const animationFrames = installAnimationFrameStub();
    let socket;
    const session = new StreamingSTTSession({
      createSession: vi.fn().mockResolvedValue({ ticket: 'ticket-definite', max_session_seconds: 90 }),
      createCapture: vi.fn().mockResolvedValue({ stop: vi.fn() }),
      createWebSocket: () => {
        socket = new FakeWebSocket('wss://app.catsco.cc/api/stt/realtime?ticket=ticket-definite');
        return socket;
      },
      onPartial: (text) => partials.push(text),
    });

    try {
      await session.start();
      socket.open();
      socket.receive({ type: 'ready' });
      socket.receive({ type: 'definite', text: '已经稳定的前半句。' });
      socket.receive({ type: 'partial', text: '正在识别的后半句' });

      animationFrames.runNextFrame(0);
      expect(partials).toEqual(['已经稳定的前半句。正在识别的后半句']);
    } finally {
      session.cancel();
      vi.unstubAllGlobals();
    }
  });

  it('flushes the newest coalesced partial before publishing the final transcript', async () => {
    const partials = [];
    const finals = [];
    const animationFrames = installAnimationFrameStub();
    let socket;
    const session = new StreamingSTTSession({
      createSession: vi.fn().mockResolvedValue({ ticket: 'ticket-flush', max_session_seconds: 90 }),
      createCapture: vi.fn().mockResolvedValue({ stop: vi.fn() }),
      createWebSocket: () => {
        socket = new FakeWebSocket('wss://app.catsco.cc/api/stt/realtime?ticket=ticket-flush');
        return socket;
      },
      onPartial: (text) => partials.push(text),
      onFinal: (text) => finals.push(text),
    });

    try {
      await session.start();
      socket.open();
      socket.receive({ type: 'ready' });
      socket.receive({ type: 'partial', text: '第一段' });
      socket.receive({ type: 'partial', text: '最新的一段' });
      socket.receive({ type: 'final', text: '最终文字' });

      expect(partials).toEqual(['最新的一段']);
      expect(finals).toEqual([]);
      animationFrames.runNextFrame(0);
      animationFrames.runNextFrame(16);
      expect(finals).toEqual(['最终文字']);
    } finally {
      session.cancel();
      vi.unstubAllGlobals();
    }
  });

  it('keeps a received final when the provider closes during its preview paint', async () => {
    const partials = [];
    const finals = [];
    const animationFrames = installAnimationFrameStub();
    let socket;
    const session = new StreamingSTTSession({
      createSession: vi.fn().mockResolvedValue({ ticket: 'ticket-final-close', max_session_seconds: 90 }),
      createCapture: vi.fn().mockResolvedValue({ stop: vi.fn() }),
      createWebSocket: () => {
        socket = new FakeWebSocket('wss://app.catsco.cc/api/stt/realtime?ticket=ticket-final-close');
        return socket;
      },
      onPartial: (text) => partials.push(text),
      onFinal: (text) => finals.push(text),
    });

    try {
      await session.start();
      socket.open();
      socket.receive({ type: 'ready' });
      socket.receive({ type: 'partial', text: '最后一段预览' });
      socket.receive({ type: 'final', text: '最终文本' });
      socket.close();

      animationFrames.runNextFrame(0);
      animationFrames.runNextFrame(16);
      expect(partials).toEqual(['最后一段预览']);
      expect(finals).toEqual(['最终文本']);
      expect(session.state).toBe('complete');
    } finally {
      session.cancel();
      vi.unstubAllGlobals();
    }
  });

  it('publishes a pending final immediately when a PWA becomes hidden', async () => {
    const partials = [];
    const finals = [];
    const animationFrames = installAnimationFrameStub();
    let socket;
    const session = new StreamingSTTSession({
      createSession: vi.fn().mockResolvedValue({ ticket: 'ticket-final-hidden', max_session_ms: 150_000 }),
      createCapture: vi.fn().mockResolvedValue({ stop: vi.fn() }),
      createWebSocket: () => {
        socket = new FakeWebSocket('wss://app.catsco.cc/api/stt/realtime?ticket=ticket-final-hidden');
        return socket;
      },
      onPartial: (text) => partials.push(text),
      onFinal: (text) => finals.push(text),
    });

    try {
      await session.start();
      socket.open();
      socket.receive({ type: 'ready' });
      socket.receive({ type: 'partial', text: '隐藏前的最新预览' });
      socket.receive({ type: 'final', text: '隐藏前的最终文本' });

      setDocumentVisibility('hidden');
      document.dispatchEvent(new Event('visibilitychange'));

      expect(partials).toEqual(['隐藏前的最新预览']);
      expect(finals).toEqual(['隐藏前的最终文本']);
      expect(session.state).toBe('complete');
      expect(animationFrames.size).toBe(0);
    } finally {
      session.cancel();
      setDocumentVisibility('visible');
      vi.unstubAllGlobals();
    }
  });

  it('publishes an empty final so the composer can release the terminal session', async () => {
    const finals = [];
    let socket;
    const session = new StreamingSTTSession({
      createSession: vi.fn().mockResolvedValue({ ticket: 'ticket-empty-final', max_session_ms: 150_000 }),
      createCapture: vi.fn().mockResolvedValue({ stop: vi.fn().mockResolvedValue(undefined) }),
      createWebSocket: () => {
        socket = new FakeWebSocket('wss://app.catsco.cc/api/stt/realtime');
        return socket;
      },
      onFinal: (text) => finals.push(text),
    });

    await session.start();
    socket.open();
    socket.receive({ type: 'ready', max_session_ms: 150_000 });
    socket.receive({ type: 'final', text: '' });

    expect(session.state).toBe('complete');
    expect(finals).toEqual(['']);
  });

  it('marks a server hard-limit final as a recoverable duration boundary', async () => {
    let socket;
    const finals = [];
    const session = new StreamingSTTSession({
      createSession: vi.fn().mockResolvedValue({ ticket: 'ticket-hard-final', max_session_ms: 150_000 }),
      createCapture: vi.fn().mockResolvedValue({ stop: vi.fn().mockResolvedValue(undefined) }),
      createWebSocket: () => {
        socket = new FakeWebSocket('wss://app.catsco.cc/api/stt/realtime');
        return socket;
      },
      onFinal: (text, details) => finals.push({ text, details }),
    });

    await session.start();
    socket.open();
    socket.receive({ type: 'ready', max_session_ms: 150_000 });
    socket.receive({ type: 'final', text: '达到硬上限前的内容', stop_reason: 'audio_limit' });

    expect(finals).toEqual([{
      text: '达到硬上限前的内容',
      details: { reason: 'duration_limit' },
    }]);
    session.cancel();
  });

  it('keeps an idle-timeout final as a natural, non-duration boundary', async () => {
    let socket;
    const finals = [];
    const session = new StreamingSTTSession({
      createSession: vi.fn().mockResolvedValue({ ticket: 'ticket-idle-final', max_session_ms: 150_000 }),
      createCapture: vi.fn().mockResolvedValue({ stop: vi.fn().mockResolvedValue(undefined) }),
      createWebSocket: () => {
        socket = new FakeWebSocket('wss://app.catsco.cc/api/stt/realtime');
        return socket;
      },
      onFinal: (text, details) => finals.push({ text, details }),
    });

    await session.start();
    socket.open();
    socket.receive({ type: 'ready', max_session_ms: 150_000 });
    socket.receive({ type: 'final', text: '静音前的内容', stop_reason: 'idle_timeout' });

    expect(finals).toEqual([{
      text: '静音前的内容',
      details: { reason: 'idle_timeout' },
    }]);
    session.cancel();
  });

  it('preserves an explicit late idle final reason over the local deadline fallback', async () => {
    vi.useFakeTimers();
    let socket;
    const finals = [];
    const limits = [];
    const session = new StreamingSTTSession({
      createSession: vi.fn().mockResolvedValue({ ticket: 'ticket-late-idle-final', max_session_ms: 1_000 }),
      createCapture: vi.fn().mockResolvedValue({ stop: vi.fn().mockResolvedValue(undefined) }),
      createWebSocket: () => {
        socket = new FakeWebSocket('wss://app.catsco.cc/api/stt/realtime');
        return socket;
      },
      onDurationLimit: (payload) => limits.push(payload),
      onFinal: (text, details) => finals.push({ text, details }),
    });

    try {
      await session.start();
      socket.open();
      socket.receive({ type: 'ready', max_session_ms: 1_000 });
      window.clearTimeout(session.durationWarningTimer);
      window.clearTimeout(session.durationTimer);
      window.clearTimeout(session.durationBoundaryTimer);
      session.durationWarningTimer = null;
      session.durationTimer = null;
      session.durationBoundaryTimer = null;

      await vi.advanceTimersByTimeAsync(1_001);
      socket.receive({ type: 'final', text: '静音边界前的内容', stop_reason: 'idle_timeout' });

      expect(limits).toHaveLength(0);
      expect(finals).toEqual([{
        text: '静音边界前的内容',
        details: { reason: 'idle_timeout' },
      }]);
    } finally {
      session.cancel();
      vi.useRealTimers();
    }
  });

  it('maps capture RMS through the VoicePi-style decibel curve', async () => {
    let emitLevel;
    const levels = [];
    const session = new StreamingSTTSession({
      createSession: vi.fn().mockResolvedValue({ ticket: 'ticket-level' }),
      createCapture: vi.fn(async ({ onLevel }) => {
        emitLevel = onLevel;
        return { stop: vi.fn() };
      }),
      createWebSocket: () => new FakeWebSocket('wss://app.catsco.cc/api/stt/realtime'),
      onAudioLevel: (level) => levels.push(level),
    });

    await session.start();
    emitLevel(0.002);
    emitLevel(0.1);

    expect(levels).toHaveLength(2);
    expect(levels[0]).toBeGreaterThan(0);
    expect(levels[1]).toBeGreaterThan(levels[0]);
    expect(levels[1]).toBeLessThanOrEqual(1);
    session.cancel();
  });

  it('fails closed when browser websocket backpressure exceeds the audio buffer limit', async () => {
    let emitFrame;
    let socket;
    const errors = [];
    const session = new StreamingSTTSession({
      createSession: vi.fn().mockResolvedValue({ ticket: 'ticket-2', max_session_seconds: 90 }),
      createCapture: vi.fn(async ({ onFrame }) => {
        emitFrame = onFrame;
        return { stop: vi.fn() };
      }),
      createWebSocket: (url) => {
        socket = new FakeWebSocket(url);
        return socket;
      },
      resolveWebSocketURL: () => 'wss://app.catsco.cc/api/stt/realtime?ticket=ticket-2',
      onError: (error) => errors.push(error.message),
    });

    await session.start();
    socket.open();
    socket.receive({ type: 'ready' });
    socket.bufferedAmount = 200_000;
    emitFrame(new Uint8Array([1, 2]).buffer);

    expect(errors).toEqual(['网络较慢，语音输入已停止']);
    expect(socket.readyState).toBe(FakeWebSocket.CLOSED);
  });

  it('stops capture when cancelled while session admission is pending', async () => {
    let resolveSession;
    const capture = { stop: vi.fn().mockResolvedValue(undefined) };
    const session = new StreamingSTTSession({
      createSession: vi.fn(() => new Promise((resolve) => { resolveSession = resolve; })),
      createCapture: vi.fn().mockResolvedValue(capture),
    });

    const starting = session.start();
    await Promise.resolve();
    session.cancel();
    resolveSession({ ticket: 'unused-ticket' });
    await starting;

    expect(session.createCapture).toHaveBeenCalledTimes(1);
    expect(capture.stop).toHaveBeenCalledTimes(1);
    expect(session.state).toBe('cancelled');
  });

  it('drains preconnect audio and then sends stop when hold-to-talk is released during admission', async () => {
    let resolveSession;
    let emitFrame;
    let socket;
    const earlyFrame = new Uint8Array([1, 2, 3, 4]).buffer;
    const trailingFrame = new Uint8Array([5, 6, 7, 8]).buffer;
    const capture = {
      stop: vi.fn(async () => {
        emitFrame(trailingFrame);
      }),
    };
    const session = new StreamingSTTSession({
      createSession: vi.fn(() => new Promise((resolve) => { resolveSession = resolve; })),
      createCapture: vi.fn(async ({ onFrame }) => {
        emitFrame = onFrame;
        return capture;
      }),
      createWebSocket: () => {
        socket = new FakeWebSocket('wss://app.catsco.cc/api/stt/realtime');
        return socket;
      },
    });

    const starting = session.start();
    await flushMicrotasks();
    emitFrame(earlyFrame);
    const stopping = session.stop();
    resolveSession({ ticket: 'ticket-stop-during-admission' });
    await starting;
    socket.open();
    socket.receive({ type: 'ready' });
    await stopping;

    expect(session.createCapture).toHaveBeenCalledTimes(1);
    expect(capture.stop).toHaveBeenCalledTimes(1);
    expect(socket.sent).toEqual([
      earlyFrame,
      trailingFrame,
      JSON.stringify({ type: 'stop' }),
    ]);
    socket.receive({ type: 'final', text: '首段也应保留' });
    expect(session.state).toBe('complete');
  });

  it('stops capture when the page became hidden while capture setup was pending', async () => {
    const originalVisibility = Object.getOwnPropertyDescriptor(document, 'visibilityState');
    let resolveCapture;
    const capture = { stop: vi.fn().mockResolvedValue(undefined) };
    const session = new StreamingSTTSession({
      createSession: vi.fn().mockResolvedValue({ ticket: 'ticket-hidden' }),
      createCapture: vi.fn(() => new Promise((resolve) => { resolveCapture = resolve; })),
    });

    try {
      const preparing = session.prepare();
      Object.defineProperty(document, 'visibilityState', { configurable: true, value: 'hidden' });
      resolveCapture(capture);
      await preparing;

      expect(capture.stop).toHaveBeenCalledTimes(1);
    } finally {
      session.cancel();
      if (originalVisibility) Object.defineProperty(document, 'visibilityState', originalVisibility);
      else delete document.visibilityState;
    }
  });

  it('does not forward a capture flush that arrives after the audio context is suspended', async () => {
    let emitFrame;
    let suspendCapture;
    let socket;
    const capturedBeforeSuspension = new Uint8Array([1, 2, 3, 4]).buffer;
    const flushedAfterSuspension = new Uint8Array([5, 6, 7, 8]).buffer;
    const capture = {
      stop: vi.fn(async () => {
        emitFrame(flushedAfterSuspension);
      }),
    };
    const session = new StreamingSTTSession({
      createSession: vi.fn().mockResolvedValue({ ticket: 'ticket-suspended' }),
      createCapture: vi.fn(async ({ onFrame, onSuspended }) => {
        emitFrame = onFrame;
        suspendCapture = onSuspended;
        return capture;
      }),
      createWebSocket: () => {
        socket = new FakeWebSocket('wss://app.catsco.cc/api/stt/realtime');
        return socket;
      },
    });

    await session.start();
    socket.open();
    socket.receive({ type: 'ready' });
    emitFrame(capturedBeforeSuspension);

    suspendCapture();
    await flushMicrotasks();

    expect(capture.stop).toHaveBeenCalledTimes(1);
    expect(socket.sent).toEqual([
      capturedBeforeSuspension,
      JSON.stringify({ type: 'stop', stop_reason: 'lifecycle_stop' }),
    ]);
    session.cancel();
  });

  it('uses the quota-reduced duration returned by the realtime ready event', async () => {
    vi.useFakeTimers();
    let socket;
    const capture = { stop: vi.fn().mockResolvedValue(undefined) };
    const session = new StreamingSTTSession({
      createSession: vi.fn().mockResolvedValue({ ticket: 'ticket-quota', max_session_seconds: 90 }),
      createCapture: vi.fn().mockResolvedValue(capture),
      createWebSocket: () => {
        socket = new FakeWebSocket('wss://app.catsco.cc/api/stt/realtime');
        return socket;
      },
    });

    await session.start();
    socket.open();
    socket.receive({ type: 'ready', max_session_seconds: 2 });
    await vi.advanceTimersByTimeAsync(2000);

    expect(capture.stop).toHaveBeenCalledTimes(1);
    expect(socket.sent).toContain(JSON.stringify({ type: 'stop', stop_reason: 'duration_limit' }));
    session.cancel();
    vi.useRealTimers();
  });

  it('honors a sub-second quota returned by the realtime ready event', async () => {
    vi.useFakeTimers();
    let socket;
    const capture = { stop: vi.fn().mockResolvedValue(undefined) };
    const session = new StreamingSTTSession({
      createSession: vi.fn().mockResolvedValue({ ticket: 'ticket-subsecond', max_session_ms: 90_000 }),
      createCapture: vi.fn().mockResolvedValue(capture),
      createWebSocket: () => {
        socket = new FakeWebSocket('wss://app.catsco.cc/api/stt/realtime');
        return socket;
      },
    });

    await session.start();
    socket.open();
    socket.receive({ type: 'ready', max_session_ms: 250 });
    await vi.advanceTimersByTimeAsync(249);
    expect(capture.stop).not.toHaveBeenCalled();
    await vi.advanceTimersByTimeAsync(1);

    expect(capture.stop).toHaveBeenCalledTimes(1);
    expect(socket.sent).toContain(JSON.stringify({ type: 'stop', stop_reason: 'duration_limit' }));
    session.cancel();
    vi.useRealTimers();
  });

  it('keeps the session duration when an older ready event omits duration fields', async () => {
    vi.useFakeTimers();
    let socket;
    const capture = { stop: vi.fn().mockResolvedValue(undefined) };
    const session = new StreamingSTTSession({
      createSession: vi.fn().mockResolvedValue({ ticket: 'ticket-legacy-ready', max_session_ms: 25 }),
      createCapture: vi.fn().mockResolvedValue(capture),
      createWebSocket: () => {
        socket = new FakeWebSocket('wss://app.catsco.cc/api/stt/realtime');
        return socket;
      },
    });

    try {
      await session.start();
      socket.open();
      socket.receive({ type: 'ready' });
      await vi.advanceTimersByTimeAsync(25);

      expect(session.state).toBe('finalizing');
      expect(capture.stop).toHaveBeenCalledTimes(1);
    } finally {
      session.cancel();
      vi.useRealTimers();
    }
  });

  it('warns ten seconds before the limit and reports whether speech is still active', async () => {
    vi.useFakeTimers();
    let socket;
    let emitLevel;
    const warnings = [];
    const capture = { stop: vi.fn().mockResolvedValue(undefined) };
    const session = new StreamingSTTSession({
      createSession: vi.fn().mockResolvedValue({ ticket: 'ticket-duration-warning', max_session_ms: 15_000 }),
      createCapture: vi.fn(async ({ onLevel }) => {
        emitLevel = onLevel;
        return capture;
      }),
      createWebSocket: () => {
        socket = new FakeWebSocket('wss://app.catsco.cc/api/stt/realtime');
        return socket;
      },
      onDurationWarning: (payload) => warnings.push(payload),
    });

    try {
      await session.start();
      socket.open();
      socket.receive({ type: 'ready', max_session_ms: 15_000 });
      await vi.advanceTimersByTimeAsync(4_000);
      emitLevel(0.02);
      await vi.advanceTimersByTimeAsync(1_000);

      expect(warnings).toHaveLength(1);
      expect(warnings[0].remainingMs).toBe(10_000);
      expect(warnings[0].hasRecentInput).toBe(true);
      expect(capture.stop).not.toHaveBeenCalled();
    } finally {
      session.cancel();
      vi.useRealTimers();
    }
  });

  it('refreshes the warning countdown by whole seconds without duplicate updates', async () => {
    vi.useFakeTimers();
    let socket;
    let emitLevel;
    const warnings = [];
    const capture = { stop: vi.fn().mockResolvedValue(undefined) };
    const session = new StreamingSTTSession({
      createSession: vi.fn().mockResolvedValue({ ticket: 'ticket-duration-countdown', max_session_ms: 15_000 }),
      createCapture: vi.fn(async ({ onLevel }) => {
        emitLevel = onLevel;
        return capture;
      }),
      createWebSocket: () => {
        socket = new FakeWebSocket('wss://app.catsco.cc/api/stt/realtime');
        return socket;
      },
      onDurationWarning: (payload) => warnings.push(payload),
    });

    try {
      await session.start();
      socket.open();
      socket.receive({ type: 'ready', max_session_ms: 15_000 });
      await vi.advanceTimersByTimeAsync(4_000);
      emitLevel(0.02);
      await vi.advanceTimersByTimeAsync(1_000);

      expect(warnings).toHaveLength(1);
      expect(warnings[0]).toEqual(expect.objectContaining({
        remainingMs: 10_000,
        remainingSeconds: 10,
        hasRecentInput: true,
      }));

      await vi.advanceTimersByTimeAsync(500);
      emitLevel(0.02);
      await vi.advanceTimersByTimeAsync(500);

      expect(warnings).toHaveLength(2);
      expect(warnings[1]).toEqual(expect.objectContaining({
        remainingMs: 9_000,
        remainingSeconds: 9,
        hasRecentInput: true,
      }));

      await vi.advanceTimersByTimeAsync(250);
      expect(warnings).toHaveLength(2);
    } finally {
      session.cancel();
      vi.useRealTimers();
    }
  });

  it('can recover a delayed warning from an audio activity callback', async () => {
    vi.useFakeTimers();
    let socket;
    let emitLevel;
    const warnings = [];
    const session = new StreamingSTTSession({
      createSession: vi.fn().mockResolvedValue({ ticket: 'ticket-delayed-warning', max_session_ms: 15_000 }),
      createCapture: vi.fn(async ({ onLevel }) => {
        emitLevel = onLevel;
        return { stop: vi.fn().mockResolvedValue(undefined) };
      }),
      createWebSocket: () => {
        socket = new FakeWebSocket('wss://app.catsco.cc/api/stt/realtime');
        return socket;
      },
      onDurationWarning: (payload) => warnings.push(payload),
    });

    try {
      await session.start();
      socket.open();
      socket.receive({ type: 'ready', max_session_ms: 15_000 });
      window.clearTimeout(session.durationWarningTimer);
      session.durationWarningTimer = null;
      await vi.advanceTimersByTimeAsync(7_000);
      emitLevel(0.02);

      expect(warnings).toHaveLength(1);
      expect(warnings[0].remainingMs).toBe(8_000);
      expect(warnings[0].hasRecentInput).toBe(true);
    } finally {
      session.cancel();
      vi.useRealTimers();
    }
  });

  it('refreshes the warning when speech resumes after a quiet reminder', async () => {
    vi.useFakeTimers();
    let socket;
    let emitLevel;
    const warnings = [];
    const session = new StreamingSTTSession({
      createSession: vi.fn().mockResolvedValue({ ticket: 'ticket-warning-refresh', max_session_ms: 15_000 }),
      createCapture: vi.fn(async ({ onLevel }) => {
        emitLevel = onLevel;
        return { stop: vi.fn().mockResolvedValue(undefined) };
      }),
      createWebSocket: () => {
        socket = new FakeWebSocket('wss://app.catsco.cc/api/stt/realtime');
        return socket;
      },
      onDurationWarning: (payload) => warnings.push(payload),
    });

    try {
      await session.start();
      socket.open();
      socket.receive({ type: 'ready', max_session_ms: 15_000 });
      await vi.advanceTimersByTimeAsync(5_000);

      expect(warnings).toHaveLength(1);
      expect(warnings[0].hasRecentInput).toBe(false);

      emitLevel(0.02);

      expect(warnings).toHaveLength(2);
      expect(warnings[1].hasRecentInput).toBe(true);
    } finally {
      session.cancel();
      vi.useRealTimers();
    }
  });

  it('still stops a short session when the socket is not ready by its deadline', async () => {
    vi.useFakeTimers();
    let socket;
    const capture = { stop: vi.fn().mockResolvedValue(undefined) };
    const session = new StreamingSTTSession({
      createSession: vi.fn().mockResolvedValue({ ticket: 'ticket-short-before-ready', max_session_ms: 1_000 }),
      createCapture: vi.fn().mockResolvedValue(capture),
      createWebSocket: () => {
        socket = new FakeWebSocket('wss://app.catsco.cc/api/stt/realtime');
        return socket;
      },
    });

    try {
      await session.start();
      await vi.advanceTimersByTimeAsync(1_000);

      expect(capture.stop).toHaveBeenCalledTimes(1);
      expect(session.state).toBe('finalizing');
      expect(socket.sent).toEqual([]);
    } finally {
      session.cancel();
      vi.useRealTimers();
    }
  });

  it('waits for a natural quiet boundary after the duration warning', async () => {
    vi.useFakeTimers();
    let socket;
    let emitLevel;
    const limits = [];
    const capture = { stop: vi.fn().mockResolvedValue(undefined) };
    const session = new StreamingSTTSession({
      createSession: vi.fn().mockResolvedValue({ ticket: 'ticket-duration-boundary', max_session_ms: 15_000 }),
      createCapture: vi.fn(async ({ onLevel }) => {
        emitLevel = onLevel;
        return capture;
      }),
      createWebSocket: () => {
        socket = new FakeWebSocket('wss://app.catsco.cc/api/stt/realtime');
        return socket;
      },
      onDurationLimit: (payload) => limits.push(payload),
    });

    try {
      await session.start();
      socket.open();
      socket.receive({ type: 'ready', max_session_ms: 15_000 });
      await vi.advanceTimersByTimeAsync(4_000);
      emitLevel(0.02);
      await vi.advanceTimersByTimeAsync(1_000);

      expect(capture.stop).not.toHaveBeenCalled();
      await vi.advanceTimersByTimeAsync(3_000);

      expect(capture.stop).toHaveBeenCalledTimes(1);
      expect(limits).toHaveLength(1);
      expect(limits[0].hadRecentInput).toBe(false);
      expect(limits[0].stoppedAtNaturalBoundary).toBe(true);
    } finally {
      session.cancel();
      vi.useRealTimers();
    }
  });

  it('does not end an untouched session early just because the warning window opened', async () => {
    vi.useFakeTimers();
    let socket;
    const capture = { stop: vi.fn().mockResolvedValue(undefined) };
    const warnings = [];
    const session = new StreamingSTTSession({
      createSession: vi.fn().mockResolvedValue({ ticket: 'ticket-duration-no-input', max_session_ms: 15_000 }),
      createCapture: vi.fn().mockResolvedValue(capture),
      createWebSocket: () => {
        socket = new FakeWebSocket('wss://app.catsco.cc/api/stt/realtime');
        return socket;
      },
      onDurationWarning: (payload) => warnings.push(payload),
    });

    try {
      await session.start();
      socket.open();
      socket.receive({ type: 'ready', max_session_ms: 15_000 });
      await vi.advanceTimersByTimeAsync(8_000);

      expect(warnings[0].hasRecentInput).toBe(false);
      expect(capture.stop).not.toHaveBeenCalled();
      await vi.advanceTimersByTimeAsync(7_000);
      expect(capture.stop).toHaveBeenCalledTimes(1);
    } finally {
      session.cancel();
      vi.useRealTimers();
    }
  });

  it('keeps recording through the warning while speech continues, then stops at the hard limit', async () => {
    vi.useFakeTimers();
    let socket;
    let emitLevel;
    const limits = [];
    const capture = { stop: vi.fn().mockResolvedValue(undefined) };
    const session = new StreamingSTTSession({
      createSession: vi.fn().mockResolvedValue({ ticket: 'ticket-duration-active', max_session_ms: 15_000 }),
      createCapture: vi.fn(async ({ onLevel }) => {
        emitLevel = onLevel;
        return capture;
      }),
      createWebSocket: () => {
        socket = new FakeWebSocket('wss://app.catsco.cc/api/stt/realtime');
        return socket;
      },
      onDurationLimit: (payload) => limits.push(payload),
    });

    try {
      await session.start();
      socket.open();
      socket.receive({ type: 'ready', max_session_ms: 15_000 });
      for (let elapsed = 0; elapsed < 15_000; elapsed += 500) {
        emitLevel(0.02);
        await vi.advanceTimersByTimeAsync(500);
      }

      expect(capture.stop).toHaveBeenCalledTimes(1);
      expect(limits).toHaveLength(1);
      expect(limits[0].hadRecentInput).toBe(true);
      expect(limits[0].stoppedAtNaturalBoundary).toBe(false);
    } finally {
      session.cancel();
      vi.useRealTimers();
    }
  });

  it('treats a transport close after a missed deadline as a recoverable boundary', async () => {
    vi.useFakeTimers();
    let socket;
    const errors = [];
    const notices = [];
    const session = new StreamingSTTSession({
      createSession: vi.fn().mockResolvedValue({ ticket: 'ticket-missed-deadline', max_session_ms: 1_000 }),
      createCapture: vi.fn().mockResolvedValue({ stop: vi.fn().mockResolvedValue(undefined) }),
      createWebSocket: () => {
        socket = new FakeWebSocket('wss://app.catsco.cc/api/stt/realtime');
        return socket;
      },
      onDurationLimit: (payload) => notices.push(payload),
      onError: (error, transcript, details) => errors.push({ error, transcript, details }),
    });

    try {
      await session.start();
      socket.open();
      socket.receive({ type: 'ready', max_session_ms: 1_000 });
      socket.receive({ type: 'partial', text: '硬上限前的内容' });
      window.clearTimeout(session.durationWarningTimer);
      window.clearTimeout(session.durationTimer);
      window.clearTimeout(session.durationBoundaryTimer);
      session.durationWarningTimer = null;
      session.durationTimer = null;
      session.durationBoundaryTimer = null;
      await vi.advanceTimersByTimeAsync(1_001);
      socket.close();

      expect(notices).toHaveLength(1);
      expect(errors[0].details.reason).toBe('duration_limit');
      expect(errors[0].transcript).toBe('硬上限前的内容');
    } finally {
      session.cancel();
      vi.useRealTimers();
    }
  });

  it('classifies a provider final that arrives after a throttled deadline as a duration boundary', async () => {
    vi.useFakeTimers();
    let socket;
    const finals = [];
    const limits = [];
    const session = new StreamingSTTSession({
      createSession: vi.fn().mockResolvedValue({ ticket: 'ticket-late-final', max_session_ms: 1_000 }),
      createCapture: vi.fn().mockResolvedValue({ stop: vi.fn().mockResolvedValue(undefined) }),
      createWebSocket: () => {
        socket = new FakeWebSocket('wss://app.catsco.cc/api/stt/realtime');
        return socket;
      },
      onDurationLimit: (payload) => limits.push(payload),
      onFinal: (text, details) => finals.push({ text, details }),
    });

    try {
      await session.start();
      socket.open();
      socket.receive({ type: 'ready', max_session_ms: 1_000 });
      socket.receive({ type: 'partial', text: '截止前的内容' });
      window.clearTimeout(session.durationWarningTimer);
      window.clearTimeout(session.durationTimer);
      session.durationWarningTimer = null;
      session.durationTimer = null;

      await vi.advanceTimersByTimeAsync(1_001);
      socket.receive({ type: 'final', text: '截止前的内容。' });

      expect(limits).toHaveLength(1);
      expect(finals).toEqual([{
        text: '截止前的内容。',
        details: { reason: 'duration_limit' },
      }]);
    } finally {
      session.cancel();
      vi.useRealTimers();
    }
  });

  it('fails and releases the session when finalizing never reaches a terminal event', async () => {
    vi.useFakeTimers();
    let socket;
    const errors = [];
    const capture = { stop: vi.fn().mockResolvedValue(undefined) };
    const session = new StreamingSTTSession({
      createSession: vi.fn().mockResolvedValue({ ticket: 'ticket-stalled-final', max_session_ms: 150_000 }),
      createCapture: vi.fn().mockResolvedValue(capture),
      createWebSocket: () => {
        socket = new FakeWebSocket('wss://app.catsco.cc/api/stt/realtime');
        return socket;
      },
      onError: (error) => errors.push(error.message),
    });

    try {
      await session.start();
      socket.open();
      socket.receive({ type: 'ready', max_session_ms: 150_000 });
      await session.stop();

      expect(session.state).toBe('finalizing');
      await vi.advanceTimersByTimeAsync(5000);

      expect(session.state).toBe('error');
      expect(errors).toEqual(['语音识别结束超时，请重试']);
      expect(socket.readyState).toBe(FakeWebSocket.CLOSED);
    } finally {
      session.cancel();
      vi.useRealTimers();
    }
  });

  it('returns the latest transcript snapshot when finalization times out', async () => {
    vi.useFakeTimers();
    let socket;
    const errors = [];
    const session = new StreamingSTTSession({
      createSession: vi.fn().mockResolvedValue({ ticket: 'ticket-timeout-snapshot', max_session_ms: 1_000 }),
      createCapture: vi.fn().mockResolvedValue({ stop: vi.fn().mockResolvedValue(undefined) }),
      createWebSocket: () => {
        socket = new FakeWebSocket('wss://app.catsco.cc/api/stt/realtime');
        return socket;
      },
      onError: (error, transcript, details) => errors.push({ error, transcript, details }),
    });

    try {
      await session.start();
      socket.open();
      socket.receive({ type: 'ready', max_session_ms: 1_000 });
      socket.receive({ type: 'partial', text: '已经识别的长语音内容' });
      await session.stop();
      await vi.advanceTimersByTimeAsync(5000);

      expect(session.state).toBe('error');
      expect(errors).toHaveLength(1);
      expect(errors[0].error.message).toBe('语音识别结束超时，请重试');
      expect(errors[0].transcript).toBe('已经识别的长语音内容');
      expect(errors[0].details.reason).toBe('user_stop');
    } finally {
      session.cancel();
      vi.useRealTimers();
    }
  });

  it('recovers a coalesced partial when the provider fails before the next paint', async () => {
    const animationFrames = installAnimationFrameStub();
    let socket;
    const partials = [];
    const errors = [];
    const session = new StreamingSTTSession({
      createSession: vi.fn().mockResolvedValue({ ticket: 'ticket-provider-error', max_session_ms: 150_000 }),
      createCapture: vi.fn().mockResolvedValue({ stop: vi.fn().mockResolvedValue(undefined) }),
      createWebSocket: () => {
        socket = new FakeWebSocket('wss://app.catsco.cc/api/stt/realtime');
        return socket;
      },
      onPartial: (text) => partials.push(text),
      onError: (error, transcript) => errors.push({ error, transcript }),
    });

    try {
      await session.start();
      socket.open();
      socket.receive({ type: 'ready', max_session_ms: 150_000 });
      socket.receive({ type: 'partial', text: '尚未绘制但不能丢失的内容' });
      expect(animationFrames.size).toBe(1);

      socket.receive({ type: 'error', code: 'provider_send_failed' });

      expect(session.state).toBe('error');
      expect(partials).toEqual(['尚未绘制但不能丢失的内容']);
      expect(errors).toHaveLength(1);
      expect(errors[0].transcript).toBe('尚未绘制但不能丢失的内容');
    } finally {
      session.cancel();
      vi.unstubAllGlobals();
    }
  });

  it('recovers the latest snapshot when the websocket disconnects', async () => {
    let socket;
    const errors = [];
    const session = new StreamingSTTSession({
      createSession: vi.fn().mockResolvedValue({ ticket: 'ticket-provider-close', max_session_ms: 150_000 }),
      createCapture: vi.fn().mockResolvedValue({ stop: vi.fn().mockResolvedValue(undefined) }),
      createWebSocket: () => {
        socket = new FakeWebSocket('wss://app.catsco.cc/api/stt/realtime');
        return socket;
      },
      onError: (error, transcript) => errors.push({ error, transcript }),
    });

    try {
      await session.start();
      socket.open();
      socket.receive({ type: 'ready', max_session_ms: 150_000 });
      socket.receive({ type: 'definite', text: '断开前已经稳定的内容' });
      socket.close();

      expect(session.state).toBe('error');
      expect(errors).toHaveLength(1);
      expect(errors[0].error.message).toBe('语音识别连接已断开');
      expect(errors[0].transcript).toBe('断开前已经稳定的内容');
    } finally {
      session.cancel();
    }
  });

  it('ignores messages that arrive after a terminal error', async () => {
    let socket;
    const partials = [];
    const session = new StreamingSTTSession({
      createSession: vi.fn().mockResolvedValue({ ticket: 'ticket-terminal-message', max_session_ms: 150_000 }),
      createCapture: vi.fn().mockResolvedValue({ stop: vi.fn().mockResolvedValue(undefined) }),
      createWebSocket: () => {
        socket = new FakeWebSocket('wss://app.catsco.cc/api/stt/realtime');
        return socket;
      },
      onPartial: (text) => partials.push(text),
    });

    try {
      await session.start();
      socket.open();
      socket.receive({ type: 'ready', max_session_ms: 150_000 });
      socket.receive({ type: 'error', code: 'provider_send_failed' });
      socket.receive({ type: 'partial', text: 'late text must be ignored' });

      expect(partials).toEqual([]);
      expect(session.state).toBe('error');
    } finally {
      session.cancel();
    }
  });

  it('classifies a hidden PWA finalization timeout after the deadline as a recoverable boundary', async () => {
    vi.useFakeTimers();
    let socket;
    const errors = [];
    const limits = [];
    const session = new StreamingSTTSession({
      createSession: vi.fn().mockResolvedValue({ ticket: 'ticket-pwa-timeout', max_session_ms: 1_000 }),
      createCapture: vi.fn().mockResolvedValue({ stop: vi.fn().mockResolvedValue(undefined) }),
      createWebSocket: () => {
        socket = new FakeWebSocket('wss://app.catsco.cc/api/stt/realtime');
        return socket;
      },
      onDurationLimit: (payload) => limits.push(payload),
      onError: (error, transcript, details) => errors.push({ error, transcript, details }),
    });

    try {
      await session.start();
      socket.open();
      socket.receive({ type: 'ready', max_session_ms: 1_000 });
      socket.receive({ type: 'partial', text: 'PWA 切到后台前的完整内容' });

      setDocumentVisibility('hidden');
      document.dispatchEvent(new Event('visibilitychange'));
      await vi.advanceTimersByTimeAsync(5000);

      expect(session.state).toBe('error');
      expect(errors).toHaveLength(1);
      expect(errors[0].transcript).toBe('PWA 切到后台前的完整内容');
      expect(errors[0].details.reason).toBe('duration_limit');
      expect(limits).toHaveLength(1);
    } finally {
      session.cancel();
      setDocumentVisibility('visible');
      vi.useRealTimers();
    }
  });

  it('classifies a server lifecycle finalization timeout after the deadline as a recoverable boundary', async () => {
    vi.useFakeTimers();
    let socket;
    const errors = [];
    const limits = [];
    const session = new StreamingSTTSession({
      createSession: vi.fn().mockResolvedValue({ ticket: 'ticket-pwa-server-timeout', max_session_ms: 1_000 }),
      createCapture: vi.fn().mockResolvedValue({ stop: vi.fn().mockResolvedValue(undefined) }),
      createWebSocket: () => {
        socket = new FakeWebSocket('wss://app.catsco.cc/api/stt/realtime');
        return socket;
      },
      onDurationLimit: (payload) => limits.push(payload),
      onError: (error, transcript, details) => errors.push({ error, transcript, details }),
    });

    try {
      await session.start();
      socket.open();
      socket.receive({ type: 'ready', max_session_ms: 1_000 });
      socket.receive({ type: 'partial', text: 'PWA 服务端超时前的内容' });
      setDocumentVisibility('hidden');
      document.dispatchEvent(new Event('visibilitychange'));

      await vi.advanceTimersByTimeAsync(1_001);
      socket.receive({
        type: 'error',
        code: 'final_timeout',
        stop_reason: 'lifecycle_stop',
      });

      expect(socket.sent).toContain(JSON.stringify({
        type: 'stop',
        stop_reason: 'lifecycle_stop',
      }));
      expect(limits).toHaveLength(1);
      expect(errors[0].details.reason).toBe('duration_limit');
      expect(errors[0].transcript).toBe('PWA 服务端超时前的内容');
    } finally {
      session.cancel();
      setDocumentVisibility('visible');
      vi.useRealTimers();
    }
  });

  it('classifies a late lifecycle final as a duration boundary', async () => {
    vi.useFakeTimers();
    let socket;
    const finals = [];
    const limits = [];
    const session = new StreamingSTTSession({
      createSession: vi.fn().mockResolvedValue({ ticket: 'ticket-pwa-late-final', max_session_ms: 1_000 }),
      createCapture: vi.fn().mockResolvedValue({ stop: vi.fn().mockResolvedValue(undefined) }),
      createWebSocket: () => {
        socket = new FakeWebSocket('wss://app.catsco.cc/api/stt/realtime');
        return socket;
      },
      onDurationLimit: (payload) => limits.push(payload),
      onFinal: (text, details) => finals.push({ text, details }),
    });

    try {
      await session.start();
      socket.open();
      socket.receive({ type: 'ready', max_session_ms: 1_000 });
      socket.receive({ type: 'partial', text: '生命周期边界前的内容' });
      setDocumentVisibility('hidden');
      document.dispatchEvent(new Event('visibilitychange'));

      await vi.advanceTimersByTimeAsync(1_001);
      socket.receive({
        type: 'final',
        text: '生命周期边界前的内容。',
        stop_reason: 'lifecycle_stop',
      });

      expect(limits).toHaveLength(1);
      expect(finals).toEqual([{
        text: '生命周期边界前的内容。',
        details: { reason: 'duration_limit' },
      }]);
    } finally {
      session.cancel();
      setDocumentVisibility('visible');
      vi.useRealTimers();
    }
  });

  it('maps structured websocket admission errors to actionable messages', async () => {
    let socket;
    const errors = [];
    const session = new StreamingSTTSession({
      createSession: vi.fn().mockResolvedValue({ ticket: 'ticket-admission', max_session_ms: 150_000 }),
      createCapture: vi.fn().mockResolvedValue({ stop: vi.fn().mockResolvedValue(undefined) }),
      createWebSocket: () => {
        socket = new FakeWebSocket('wss://app.catsco.cc/api/stt/realtime');
        return socket;
      },
      onError: (error) => errors.push(error.message),
    });

    await session.start();
    socket.open();
    socket.receive({ type: 'error', code: 'quota_exhausted', message: 'internal detail' });

    expect(session.state).toBe('error');
    expect(errors).toEqual(['语音输入额度已用完，请稍后再试']);
  });

  it('treats a server hard-limit timeout as a recoverable duration boundary', async () => {
    let socket;
    const errors = [];
    const session = new StreamingSTTSession({
      createSession: vi.fn().mockResolvedValue({ ticket: 'ticket-server-hard-limit', max_session_ms: 150_000 }),
      createCapture: vi.fn().mockResolvedValue({ stop: vi.fn().mockResolvedValue(undefined) }),
      createWebSocket: () => {
        socket = new FakeWebSocket('wss://app.catsco.cc/api/stt/realtime');
        return socket;
      },
      onError: (error, transcript, details) => errors.push({ error, transcript, details }),
    });

    await session.start();
    socket.open();
    socket.receive({ type: 'ready', max_session_ms: 150_000 });
    socket.receive({ type: 'partial', text: '服务器硬上限前的内容' });
    socket.receive({
      type: 'error',
      code: 'final_timeout',
      stop_reason: 'hard_timeout',
    });

    expect(errors[0].details.reason).toBe('duration_limit');
    expect(errors[0].transcript).toBe('服务器硬上限前的内容');
    session.cancel();
  });

  it('keeps an automatic duration stop recoverable when finalization times out', async () => {
    vi.useFakeTimers();
    let socket;
    const errors = [];
    const session = new StreamingSTTSession({
      createSession: vi.fn().mockResolvedValue({ ticket: 'ticket-duration-server-timeout', max_session_ms: 1_000 }),
      createCapture: vi.fn().mockResolvedValue({ stop: vi.fn().mockResolvedValue(undefined) }),
      createWebSocket: () => {
        socket = new FakeWebSocket('wss://app.catsco.cc/api/stt/realtime');
        return socket;
      },
      onError: (error, transcript, details) => errors.push({ error, transcript, details }),
    });

    try {
      await session.start();
      socket.open();
      socket.receive({ type: 'ready', max_session_ms: 1_000 });
      socket.receive({ type: 'partial', text: '自动分段前的内容' });
      window.clearTimeout(session.durationWarningTimer);
      window.clearTimeout(session.durationBoundaryTimer);
      session.durationWarningTimer = null;
      session.durationBoundaryTimer = null;

      await vi.advanceTimersByTimeAsync(1_000);
      socket.receive({
        type: 'error',
        code: 'final_timeout',
        stop_reason: 'duration_limit',
      });

      expect(socket.sent).toContain(JSON.stringify({
        type: 'stop',
        stop_reason: 'duration_limit',
      }));
      expect(errors[0].details.reason).toBe('duration_limit');
      expect(errors[0].transcript).toBe('自动分段前的内容');
    } finally {
      session.cancel();
      vi.useRealTimers();
    }
  });

  it('keeps an idle-timeout provider error as an error while preserving text', async () => {
    let socket;
    const errors = [];
    const session = new StreamingSTTSession({
      createSession: vi.fn().mockResolvedValue({ ticket: 'ticket-idle-error', max_session_ms: 150_000 }),
      createCapture: vi.fn().mockResolvedValue({ stop: vi.fn().mockResolvedValue(undefined) }),
      createWebSocket: () => {
        socket = new FakeWebSocket('wss://app.catsco.cc/api/stt/realtime');
        return socket;
      },
      onError: (error, transcript, details) => errors.push({ error, transcript, details }),
    });

    await session.start();
    socket.open();
    socket.receive({ type: 'ready', max_session_ms: 150_000 });
    socket.receive({ type: 'partial', text: '静音前的内容' });
    socket.receive({
      type: 'error',
      code: 'final_timeout',
      stop_reason: 'idle_timeout',
    });

    expect(errors).toHaveLength(1);
    expect(errors[0].details.reason).toBe('idle_timeout');
    expect(errors[0].transcript).toBe('静音前的内容');
    session.cancel();
  });

  it('does not let a late idle-timeout error become a hard duration boundary', async () => {
    vi.useFakeTimers();
    let socket;
    const errors = [];
    const limits = [];
    const session = new StreamingSTTSession({
      createSession: vi.fn().mockResolvedValue({ ticket: 'ticket-late-idle-error', max_session_ms: 1_000 }),
      createCapture: vi.fn().mockResolvedValue({ stop: vi.fn().mockResolvedValue(undefined) }),
      createWebSocket: () => {
        socket = new FakeWebSocket('wss://app.catsco.cc/api/stt/realtime');
        return socket;
      },
      onDurationLimit: (payload) => limits.push(payload),
      onError: (error, transcript, details) => errors.push({ error, transcript, details }),
    });

    try {
      await session.start();
      socket.open();
      socket.receive({ type: 'ready', max_session_ms: 1_000 });
      socket.receive({ type: 'partial', text: '静音边界前的内容' });
      window.clearTimeout(session.durationWarningTimer);
      window.clearTimeout(session.durationTimer);
      window.clearTimeout(session.durationBoundaryTimer);
      session.durationWarningTimer = null;
      session.durationTimer = null;
      session.durationBoundaryTimer = null;

      await vi.advanceTimersByTimeAsync(1_001);
      socket.receive({
        type: 'error',
        code: 'final_timeout',
        stop_reason: 'idle_timeout',
      });

      expect(limits).toHaveLength(0);
      expect(errors).toHaveLength(1);
      expect(errors[0].details.reason).toBe('idle_timeout');
      expect(errors[0].transcript).toBe('静音边界前的内容');
    } finally {
      session.cancel();
      vi.useRealTimers();
    }
  });
});
