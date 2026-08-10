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

  it('stops capture when cancelled while microphone startup is pending', async () => {
    let resolveCapture;
    const capture = { stop: vi.fn() };
    const session = new StreamingSTTSession({
      createCapture: vi.fn(() => new Promise((resolve) => { resolveCapture = resolve; })),
      createSession: vi.fn(),
    });

    const starting = session.start();
    session.cancel();
    resolveCapture(capture);
    await starting;

    expect(capture.stop).toHaveBeenCalledTimes(1);
    expect(session.createSession).not.toHaveBeenCalled();
    expect(session.state).toBe('cancelled');
  });
});
