import { StreamingSTTSession } from './stt-client';

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

describe('StreamingSTTSession', () => {
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
