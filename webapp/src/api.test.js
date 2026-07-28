class MockWebSocket {
  static CONNECTING = 0;

  static OPEN = 1;

  static CLOSING = 2;

  static CLOSED = 3;

  static instances = [];

  constructor(url) {
    this.url = url;
    this.readyState = MockWebSocket.CONNECTING;
    this.send = vi.fn();
    MockWebSocket.instances.push(this);
  }

  open() {
    this.readyState = MockWebSocket.OPEN;
    this.onopen?.();
  }

  serverClose() {
    this.readyState = MockWebSocket.CLOSED;
    this.onclose?.({ code: 1006 });
  }

  close() {
    this.readyState = MockWebSocket.CLOSED;
    this.onclose?.({ code: 1000 });
  }
}

describe('WebSocket connection recovery', () => {
  let api;

  beforeEach(async () => {
    vi.useFakeTimers();
    vi.resetModules();
    localStorage.clear();
    MockWebSocket.instances = [];
    global.WebSocket = MockWebSocket;
    api = await import('./api');
    api.setToken('test-token');
  });

  afterEach(() => {
    api.disconnectWS();
    vi.useRealTimers();
  });

  test('reuses an open or connecting socket', () => {
    const onMessage = vi.fn();

    expect(api.connectWS(onMessage)).toBe(true);
    expect(api.connectWS(onMessage)).toBe(false);
    expect(MockWebSocket.instances).toHaveLength(1);

    MockWebSocket.instances[0].open();
    expect(api.connectWS(onMessage)).toBe(false);
    vi.advanceTimersByTime(10000);
    expect(MockWebSocket.instances).toHaveLength(1);
    expect(onMessage).toHaveBeenCalledWith({ _type: 'ws_open' });
  });

  test('notifies onWSMessage subscribers when the socket opens', () => {
    const onMessage = vi.fn();
    const subscriber = vi.fn();
    const unsubscribe = api.onWSMessage(subscriber);

    api.connectWS(onMessage);
    MockWebSocket.instances[0].open();

    expect(subscriber).toHaveBeenCalledTimes(1);
    expect(subscriber).toHaveBeenCalledWith({ _type: 'ws_open' });

    unsubscribe();
  });

  test('includes the authoritative target agent in stream cancel metadata', async () => {
    api.connectWS(vi.fn());
    const socket = MockWebSocket.instances[0];
    socket.open();

    await api.wsSendStreamCancel('grp_80', 42);

    const envelope = JSON.parse(socket.send.mock.calls.at(-1)[0]);
    expect(envelope.pub).toMatchObject({
      topic: 'grp_80',
      type: 'stream_cancel',
      metadata: {
        stream_event: 'cancel',
        control: 'interrupt',
        target_bot_uid: 42,
      },
    });
  });

  test('retries quickly with capped backoff after a dropped socket', () => {
    const onMessage = vi.fn();
    api.connectWS(onMessage);
    MockWebSocket.instances[0].open();

    MockWebSocket.instances[0].serverClose();
    expect(onMessage).toHaveBeenCalledWith({
      _type: 'ws_close',
      attempt: 1,
      retryInMs: 1000,
    });

    vi.advanceTimersByTime(999);
    expect(MockWebSocket.instances).toHaveLength(1);
    vi.advanceTimersByTime(1);
    expect(MockWebSocket.instances).toHaveLength(2);

    MockWebSocket.instances[1].serverClose();
    expect(onMessage).toHaveBeenCalledWith({
      _type: 'ws_close',
      attempt: 2,
      retryInMs: 2000,
    });
  });

  test('abandons a socket stuck while connecting and retries', () => {
    const onMessage = vi.fn();
    api.connectWS(onMessage);
    const staleSocket = MockWebSocket.instances[0];

    vi.advanceTimersByTime(9999);
    expect(MockWebSocket.instances).toHaveLength(1);
    vi.advanceTimersByTime(1);
    expect(staleSocket.readyState).toBe(MockWebSocket.CLOSED);
    expect(onMessage).toHaveBeenCalledWith({
      _type: 'ws_close',
      attempt: 1,
      retryInMs: 1000,
    });

    vi.advanceTimersByTime(1000);
    expect(MockWebSocket.instances).toHaveLength(2);
  });

  test('forces a fresh socket when the page resumes', () => {
    const onMessage = vi.fn();
    api.connectWS(onMessage);
    const staleSocket = MockWebSocket.instances[0];
    staleSocket.open();

    expect(api.reconnectWS(onMessage)).toBe(true);
    expect(staleSocket.readyState).toBe(MockWebSocket.CLOSED);
    expect(MockWebSocket.instances).toHaveLength(2);

    vi.runOnlyPendingTimers();
    expect(MockWebSocket.instances).toHaveLength(2);
  });

  test('manual disconnect cancels a scheduled retry', () => {
    const onMessage = vi.fn();
    api.connectWS(onMessage);
    MockWebSocket.instances[0].serverClose();

    api.disconnectWS();
    vi.runOnlyPendingTimers();

    expect(MockWebSocket.instances).toHaveLength(1);
  });

  test('manual disconnect cancels the connecting-socket watchdog', () => {
    const onMessage = vi.fn();
    api.connectWS(onMessage);

    api.disconnectWS();
    vi.advanceTimersByTime(11000);

    expect(MockWebSocket.instances).toHaveLength(1);
  });

  test('stops reconnecting when the saved session has expired', () => {
    const onMessage = vi.fn();
    const payload = btoa(JSON.stringify({ exp: Math.floor(Date.now() / 1000) - 60 }))
      .replace(/=/g, '')
      .replace(/\+/g, '-')
      .replace(/\//g, '_');
    api.setToken(`header.${payload}.signature`);

    expect(api.connectWS(onMessage)).toBe(false);
    expect(MockWebSocket.instances).toHaveLength(0);
    expect(onMessage).toHaveBeenCalledWith({ _type: 'ws_auth_expired' });
  });
});
