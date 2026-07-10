class MockWebSocket {
  static CONNECTING = 0;

  static OPEN = 1;

  static CLOSING = 2;

  static CLOSED = 3;

  static instances = [];

  constructor(url) {
    this.url = url;
    this.readyState = MockWebSocket.CONNECTING;
    this.send = jest.fn();
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

  beforeEach(() => {
    jest.useFakeTimers();
    jest.resetModules();
    localStorage.clear();
    MockWebSocket.instances = [];
    global.WebSocket = MockWebSocket;
    api = require('./api');
    api.setToken('test-token');
  });

  afterEach(() => {
    api.disconnectWS();
    jest.useRealTimers();
  });

  test('reuses an open or connecting socket', () => {
    const onMessage = jest.fn();

    expect(api.connectWS(onMessage)).toBe(true);
    expect(api.connectWS(onMessage)).toBe(false);
    expect(MockWebSocket.instances).toHaveLength(1);

    MockWebSocket.instances[0].open();
    expect(api.connectWS(onMessage)).toBe(false);
    expect(MockWebSocket.instances).toHaveLength(1);
    expect(onMessage).toHaveBeenCalledWith({ _type: 'ws_open' });
  });

  test('retries quickly with capped backoff after a dropped socket', () => {
    const onMessage = jest.fn();
    api.connectWS(onMessage);
    MockWebSocket.instances[0].open();

    MockWebSocket.instances[0].serverClose();
    expect(onMessage).toHaveBeenCalledWith({
      _type: 'ws_close',
      attempt: 1,
      retryInMs: 1000,
    });

    jest.advanceTimersByTime(999);
    expect(MockWebSocket.instances).toHaveLength(1);
    jest.advanceTimersByTime(1);
    expect(MockWebSocket.instances).toHaveLength(2);

    MockWebSocket.instances[1].serverClose();
    expect(onMessage).toHaveBeenCalledWith({
      _type: 'ws_close',
      attempt: 2,
      retryInMs: 2000,
    });
  });

  test('forces a fresh socket when the page resumes', () => {
    const onMessage = jest.fn();
    api.connectWS(onMessage);
    const staleSocket = MockWebSocket.instances[0];
    staleSocket.open();

    expect(api.reconnectWS(onMessage)).toBe(true);
    expect(staleSocket.readyState).toBe(MockWebSocket.CLOSED);
    expect(MockWebSocket.instances).toHaveLength(2);

    jest.runOnlyPendingTimers();
    expect(MockWebSocket.instances).toHaveLength(2);
  });

  test('manual disconnect cancels a scheduled retry', () => {
    const onMessage = jest.fn();
    api.connectWS(onMessage);
    MockWebSocket.instances[0].serverClose();

    api.disconnectWS();
    jest.runOnlyPendingTimers();

    expect(MockWebSocket.instances).toHaveLength(1);
  });

  test('stops reconnecting when the saved session has expired', () => {
    const onMessage = jest.fn();
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
