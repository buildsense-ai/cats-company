import {
  createPCM16Capture,
  releaseReusableMicrophoneStream,
  StreamingSTTSession,
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

  it('acquires an authenticated session before requesting microphone capture', async () => {
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

  it('does not request microphone capture when session admission fails', async () => {
    const errors = [];
    const session = new StreamingSTTSession({
      createSession: vi.fn().mockRejectedValue(Object.assign(new Error('quota'), { status: 429 })),
      createCapture: vi.fn(),
      onError: (error) => errors.push(error.message),
    });

    await session.start();

    expect(session.createCapture).not.toHaveBeenCalled();
    expect(errors).toEqual(['语音输入额度已用完，请稍后再试']);
  });

  it('does not start capture when the PWA becomes hidden during session admission', async () => {
    let resolveSession;
    const session = new StreamingSTTSession({
      createSession: vi.fn(() => new Promise((resolve) => { resolveSession = resolve; })),
      createCapture: vi.fn(),
    });

    const starting = session.start();
    setDocumentVisibility('hidden');
    document.dispatchEvent(new Event('visibilitychange'));
    resolveSession({ ticket: 'unused-ticket' });
    await starting;

    expect(session.createCapture).not.toHaveBeenCalled();
    expect(session.state).toBe('complete');
  });

  it('surfaces a non-lifecycle AbortError from microphone capture', async () => {
    const errors = [];
    const session = new StreamingSTTSession({
      createSession: vi.fn().mockResolvedValue({ ticket: 'ticket-abort-error' }),
      createCapture: vi.fn().mockRejectedValue(Object.assign(new Error('设备初始化失败'), { name: 'AbortError' })),
      onError: (error) => errors.push(error.message),
    });

    await session.start();

    expect(session.state).toBe('error');
    expect(errors).toEqual(['设备初始化失败']);
  });

  it('buffers PCM before ready and publishes only the final transcript', async () => {
    let emitFrame;
    const capture = { stop: vi.fn() };
    const partials = [];
    const finals = [];
    const sockets = [];
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
    expect(partials).toEqual(['你好']);
    expect(finals).toEqual([]);

    sockets[0].receive({ type: 'final', text: '你好世界' });
    expect(finals).toEqual(['你好世界']);
    expect(capture.stop).toHaveBeenCalledTimes(1);
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

  it('does not start capture when cancelled while session admission is pending', async () => {
    let resolveSession;
    const session = new StreamingSTTSession({
      createSession: vi.fn(() => new Promise((resolve) => { resolveSession = resolve; })),
      createCapture: vi.fn(),
    });

    const starting = session.start();
    session.cancel();
    resolveSession({ ticket: 'unused-ticket' });
    await starting;

    expect(session.createCapture).not.toHaveBeenCalled();
    expect(session.state).toBe('cancelled');
  });

  it('stops without opening microphone capture when hold-to-talk is released during admission', async () => {
    let resolveSession;
    const session = new StreamingSTTSession({
      createSession: vi.fn(() => new Promise((resolve) => { resolveSession = resolve; })),
      createCapture: vi.fn(),
    });

    const starting = session.start();
    const stopping = session.stop();
    resolveSession({ ticket: 'unused-ticket' });
    await Promise.all([starting, stopping]);

    expect(session.createCapture).not.toHaveBeenCalled();
    expect(session.state).toBe('complete');
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
    expect(socket.sent).toContain(JSON.stringify({ type: 'stop' }));
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
    expect(socket.sent).toContain(JSON.stringify({ type: 'stop' }));
    session.cancel();
    vi.useRealTimers();
  });
});
