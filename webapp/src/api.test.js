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

  receive(message) {
    this.onmessage?.({ data: JSON.stringify(message) });
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
    expect(MockWebSocket.instances).toHaveLength(1);
    expect(onMessage).toHaveBeenCalledWith({ _type: 'ws_open' });
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

  test('routes structured external history requests and resolves the matching device result', async () => {
    api.connectWS(vi.fn());
    const socket = MockWebSocket.instances[0];
    socket.open();

    const pending = api.requestExternalHistory('device-1', {
      action: 'preview',
      provider: 'codex',
      updatedSince: '7d',
    });
    const sent = JSON.parse(socket.send.mock.calls.at(-1)[0]);
    expect(sent.device_rpc.operation).toBe('external_history');
    expect(sent.device_rpc.device_id).toBe('device-1');
    expect(sent.device_rpc.payload).toEqual({ action: 'preview', provider: 'codex', updatedSince: '7d' });

    socket.receive({
      device_rpc: {
        type: 'result',
        request_id: sent.device_rpc.request_id,
        result: { ok: true, content: JSON.stringify({ selectedCount: 12 }) },
      },
    });

    await expect(pending).resolves.toEqual({ selectedCount: 12 });
  });

  test('reports precise external history progress and refreshes the inactivity timeout', async () => {
    api.connectWS(vi.fn());
    const socket = MockWebSocket.instances[0];
    socket.open();
    const onProgress = vi.fn();

    const pending = api.requestExternalHistory('device-1', { action: 'execute', provider: 'codex' }, {
      timeoutMs: 1000,
      onProgress,
    });
    const sent = JSON.parse(socket.send.mock.calls.at(-1)[0]);

    vi.advanceTimersByTime(900);
    socket.receive({
      device_rpc: {
        type: 'progress',
        request_id: sent.device_rpc.request_id,
        progress: { processed: 37.9, total: 100.8, provider: 'codex', phase: 'importing' },
      },
    });
    expect(onProgress).toHaveBeenCalledWith({
      processed: 37,
      total: 100,
      provider: 'codex',
      phase: 'importing',
    });

    vi.advanceTimersByTime(900);
    socket.receive({
      device_rpc: {
        type: 'result',
        request_id: sent.device_rpc.request_id,
        result: { ok: true, content: JSON.stringify({ processedResources: 100, status: 'completed' }) },
      },
    });

    await expect(pending).resolves.toEqual({ processedResources: 100, status: 'completed' });
  });

  test('structured oversized error reaches Web immediately with correct error code', async () => {
    api.connectWS(vi.fn());
    const socket = MockWebSocket.instances[0];
    socket.open();

    const pending = api.requestExternalHistory('device-1', { action: 'execute', provider: 'codex' }, {
      timeoutMs: 5000,
    });
    const sent = JSON.parse(socket.send.mock.calls.at(-1)[0]);

    // Structured error arrives promptly — not as a 55s timeout.
    socket.receive({
      device_rpc: {
        type: 'result',
        request_id: sent.device_rpc.request_id,
        error: {
          code: 'external_history_record_too_large',
          message: 'history record too large',
          details: { provider: 'codex', limitBytes: 4194304, commandKind: 'read', resumable: true },
        },
      },
    });

    await expect(pending).rejects.toMatchObject({
      code: 'external_history_record_too_large',
      oversized: true,
      resumable: true,
      provider: 'codex',
      message: expect.stringContaining('历史记录超过安全限制'),
    });
  });

  test('nullable total progress means discovering/indeterminate phase', async () => {
    api.connectWS(vi.fn());
    const socket = MockWebSocket.instances[0];
    socket.open();
    const onProgress = vi.fn();

    const pending = api.requestExternalHistory('device-1', { action: 'execute', provider: 'pi' }, {
      timeoutMs: 5000,
      onProgress,
    });
    const sent = JSON.parse(socket.send.mock.calls.at(-1)[0]);

    // Progress with total=null means discovering — must not be rejected.
    socket.receive({
      device_rpc: {
        type: 'progress',
        request_id: sent.device_rpc.request_id,
        progress: { processed: 0, total: null, provider: 'pi', phase: 'discovering' },
      },
    });
    expect(onProgress).toHaveBeenCalledWith(
      expect.objectContaining({ processed: 0, total: null, phase: 'discovering' })
    );

    socket.receive({
      device_rpc: {
        type: 'result',
        request_id: sent.device_rpc.request_id,
        result: { ok: true, content: JSON.stringify({ status: 'completed' }) },
      },
    });
    await expect(pending).resolves.toEqual({ status: 'completed' });
  });

  test('determinate total=0 (stable empty catalog) stays 0, not clamped to 1', async () => {
    api.connectWS(vi.fn());
    const socket = MockWebSocket.instances[0];
    socket.open();
    const onProgress = vi.fn();

    const pending = api.requestExternalHistory('device-1', { action: 'execute', provider: 'codex' }, {
      timeoutMs: 5000,
      onProgress,
    });
    const sent = JSON.parse(socket.send.mock.calls.at(-1)[0]);

    socket.receive({
      device_rpc: {
        type: 'progress',
        request_id: sent.device_rpc.request_id,
        progress: { processed: 0, total: 0, provider: 'codex', phase: 'importing' },
      },
    });
    expect(onProgress).toHaveBeenCalledWith(
      expect.objectContaining({ processed: 0, total: 0, phase: 'importing' })
    );

    socket.receive({
      device_rpc: {
        type: 'result',
        request_id: sent.device_rpc.request_id,
        result: { ok: true, content: JSON.stringify({ status: 'completed' }) },
      },
    });
    await expect(pending).resolves.toEqual({ status: 'completed' });
  });

  test('external_history_source_failed surfaces as a structured source failure error', async () => {
    api.connectWS(vi.fn());
    const socket = MockWebSocket.instances[0];
    socket.open();

    const pending = api.requestExternalHistory('device-1', { action: 'execute', provider: 'pi' }, {
      timeoutMs: 5000,
    });
    const sent = JSON.parse(socket.send.mock.calls.at(-1)[0]);

    socket.receive({
      device_rpc: {
        type: 'result',
        request_id: sent.device_rpc.request_id,
        error: {
          code: 'external_history_source_failed',
          message: 'source failed',
          details: { provider: 'pi', status: 'source_failed' },
        },
      },
    });

    await expect(pending).rejects.toMatchObject({
      code: 'external_history_source_failed',
      sourceFailed: true,
      provider: 'pi',
      message: expect.stringContaining('外部历史来源执行失败'),
    });
  });

  test('oversized record with resumable:false says cannot-import, not continue-remaining', async () => {
    api.connectWS(vi.fn());
    const socket = MockWebSocket.instances[0];
    socket.open();

    const pending = api.requestExternalHistory('device-1', { action: 'execute', provider: 'codex' }, {
      timeoutMs: 5000,
    });
    const sent = JSON.parse(socket.send.mock.calls.at(-1)[0]);

    socket.receive({
      device_rpc: {
        type: 'result',
        request_id: sent.device_rpc.request_id,
        error: {
          code: 'external_history_record_too_large',
          message: 'too large',
          details: { provider: 'codex', limitBytes: 4194304, commandKind: 'read', resumable: false },
        },
      },
    });

    await expect(pending).rejects.toMatchObject({
      code: 'external_history_record_too_large',
      oversized: true,
      resumable: false,
      message: expect.stringContaining('该条记录目前无法导入'),
    });
  });
});
