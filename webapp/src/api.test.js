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
    vi.unstubAllGlobals();
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

  test('uses the captured logout token for push unsubscription', async () => {
    const fetchMock = vi.fn().mockResolvedValue({
      ok: true,
      json: vi.fn().mockResolvedValue({ subscribed: false }),
    });
    vi.stubGlobal('fetch', fetchMock);
    api.setToken(null);

    const request = api.api.unsubscribePush('https://push.example/sub', 'captured-token');
    await request;

    expect(fetchMock).toHaveBeenCalledWith('/api/push/subscriptions', expect.objectContaining({
      method: 'DELETE',
      headers: expect.objectContaining({ Authorization: 'Bearer captured-token' }),
    }));
  });

  test('publishes session revisions', () => {
    const onAuthChanged = vi.fn();
    window.addEventListener('cc:auth-changed', onAuthChanged);

    api.setToken(null);

    expect(onAuthChanged).toHaveBeenLastCalledWith(expect.objectContaining({
      detail: expect.objectContaining({
        loggedIn: false,
        revision: api.getAuthRevision(),
      }),
    }));
    window.removeEventListener('cc:auth-changed', onAuthChanged);
  });

  test('reuses one push generation for renewed tokens of the same account', () => {
    const tokenFor = (userId, nonce) => {
      const payload = btoa(JSON.stringify({ userId, nonce }))
        .replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '');
      return `header.${payload}.signature`;
    };

    api.setToken(tokenFor(42, 'first'));
    const firstRegistrationID = api.getPushRegistrationID();
    api.setToken(tokenFor(42, 'renewed'));

    expect(api.getPushRegistrationID()).toBe(firstRegistrationID);
    api.setToken(tokenFor(43, 'different-account'));
    expect(api.getPushRegistrationID()).not.toBe(firstRegistrationID);
  });

  test('adopts a same-account push generation written by another tab', () => {
    const payload = btoa(JSON.stringify({ userId: 42 }))
      .replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '');
    api.setToken(`header.${payload}.signature`);

    localStorage.setItem('oc_push_registration_id', 'generation-from-other-tab');
    localStorage.setItem('oc_push_registration_owner', 'user:42');

    expect(api.getPushRegistrationID()).toBe('generation-from-other-tab');
  });

  test('rotates the push generation after the last tab retires it', () => {
    const payload = btoa(JSON.stringify({ userId: 42 }))
      .replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '');
    const token = `header.${payload}.signature`;
    api.setToken(token);
    const retiredRegistrationID = api.getPushRegistrationID();

    expect(api.retirePushRegistrationID(retiredRegistrationID)).toBe(true);
    api.setToken(token);

    expect(api.getPushRegistrationID()).not.toBe(retiredRegistrationID);
  });

  test('allows a session change to abort push reconciliation requests', async () => {
    const fetchMock = vi.fn().mockImplementation((_url, options) => new Promise((_resolve, reject) => {
      options.signal.addEventListener('abort', () => reject(new DOMException('aborted', 'AbortError')));
    }));
    vi.stubGlobal('fetch', fetchMock);
    const controller = new AbortController();

    const request = api.api.getPushConfig(controller.signal);
    const rejection = expect(request).rejects.toMatchObject({ code: 'REQUEST_ABORTED' });
    controller.abort();

    await rejection;
    const requestSignal = fetchMock.mock.calls[0][1].signal;
    expect(requestSignal).not.toBe(controller.signal);
    expect(requestSignal.aborted).toBe(true);
  });

  test('aborts push unsubscription after the cleanup timeout', async () => {
    const fetchMock = vi.fn().mockImplementation((_url, options) => new Promise((_resolve, reject) => {
      options.signal.addEventListener('abort', () => reject(new DOMException('aborted', 'AbortError')));
    }));
    vi.stubGlobal('fetch', fetchMock);

    const request = api.api.unsubscribePush('https://push.example/sub', 'captured-token');
    const rejection = expect(request).rejects.toMatchObject({ code: 'REQUEST_ABORTED' });
    await vi.advanceTimersByTimeAsync(3000);

    await rejection;
  });
});

describe('message history request controls', () => {
  let api;

  beforeEach(async () => {
    vi.useFakeTimers();
    vi.resetModules();
    localStorage.clear();
    api = await import('./api');
  });

  afterEach(() => {
    vi.restoreAllMocks();
    vi.useRealTimers();
  });

  test('aborts a history request when its timeout expires', async () => {
    global.fetch = vi.fn((url, options) => new Promise((resolve, reject) => {
      options.signal.addEventListener('abort', () => {
        const error = new Error('aborted');
        error.name = 'AbortError';
        reject(error);
      }, { once: true });
    }));

    const request = api.api.getMessages(
      'p2p_1_2',
      50,
      0,
      true,
      0,
      { timeoutMs: 15000 },
    );
    const rejection = expect(request).rejects.toMatchObject({ code: 'REQUEST_TIMEOUT' });

    await vi.advanceTimersByTimeAsync(15000);
    await rejection;
    expect(global.fetch.mock.calls[0][1].signal.aborted).toBe(true);
  });

  test('distinguishes caller cancellation from a timeout', async () => {
    global.fetch = vi.fn((url, options) => new Promise((resolve, reject) => {
      options.signal.addEventListener('abort', () => {
        const error = new Error('aborted');
        error.name = 'AbortError';
        reject(error);
      }, { once: true });
    }));
    const controller = new AbortController();
    const request = api.api.getMessages(
      'p2p_1_2',
      50,
      0,
      true,
      0,
      { signal: controller.signal, timeoutMs: 15000 },
    );
    const rejection = expect(request).rejects.toMatchObject({ code: 'REQUEST_ABORTED' });

    controller.abort();
    await rejection;
  });
});
