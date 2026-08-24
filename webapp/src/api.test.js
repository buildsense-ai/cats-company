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
    sessionStorage.clear();
    Object.defineProperty(document, 'visibilityState', { configurable: true, value: 'visible' });
    window.name = '';
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

  test('keeps a SkillHub device request pending after its ack and resolves its result', async () => {
    api.connectWS(vi.fn());
    const socket = MockWebSocket.instances[0];
    socket.open();

    const request = api.requestSkillHubDeviceTool({
      deviceId: 'alice-device',
      ownerUserId: 7,
      toolName: 'skillhub.localWorkspace.get',
      payload: { bot_uid: '42' },
      timeoutMs: 5_000,
    });
    const envelope = JSON.parse(socket.send.mock.calls.at(-1)[0]);
    const settled = vi.fn();
    request.then(settled);

    socket.onmessage({ data: JSON.stringify({
      ctrl: { id: envelope.thin_tool_rpc.id, code: 200, text: 'accepted' },
    }) });
    await Promise.resolve();
    expect(settled).not.toHaveBeenCalled();

    socket.onmessage({ data: JSON.stringify({
      thin_tool_rpc: {
        type: 'result',
        request_id: envelope.thin_tool_rpc.request_id,
        result: { bot_uid: '42' },
      },
    }) });
    await expect(request).resolves.toEqual({ bot_uid: '42' });
  });

  test('rejects a SkillHub device request when the socket disconnects', async () => {
    api.connectWS(vi.fn());
    const socket = MockWebSocket.instances[0];
    socket.open();
    const request = api.requestSkillHubDeviceTool({
      deviceId: 'alice-device',
      ownerUserId: 7,
      toolName: 'skillhub.localWorkspace.get',
      payload: { bot_uid: '42' },
    });

    socket.serverClose();

    await expect(request).rejects.toMatchObject({ code: 'skillhub_websocket_disconnected' });
  });

  test('rejects a pending SkillHub device request before a forced reconnect replaces its route', async () => {
    const onMessage = vi.fn();
    api.connectWS(onMessage);
    MockWebSocket.instances[0].open();
    const request = api.requestSkillHubDeviceTool({
      deviceId: 'alice-device',
      ownerUserId: 7,
      toolName: 'skillhub.localSkill.share',
      payload: { bot_uid: '42' },
    });

    api.reconnectWS(onMessage);

    await expect(request).rejects.toMatchObject({ code: 'skillhub_websocket_disconnected' });
    expect(MockWebSocket.instances).toHaveLength(2);
  });

  test('rejects a pending SkillHub device request on a programmatic disconnect', async () => {
    api.connectWS(vi.fn());
    MockWebSocket.instances[0].open();
    const request = api.requestSkillHubDeviceTool({
      deviceId: 'alice-device',
      ownerUserId: 7,
      toolName: 'skillhub.localSkill.finalize',
      payload: { bot_uid: '42' },
    });

    api.disconnectWS();

    await expect(request).rejects.toMatchObject({ code: 'skillhub_websocket_disconnected' });
  });

  test('times out a SkillHub device request that never returns a result', async () => {
    api.connectWS(vi.fn());
    const socket = MockWebSocket.instances[0];
    socket.open();
    const request = api.requestSkillHubDeviceTool({
      deviceId: 'alice-device',
      ownerUserId: 7,
      toolName: 'skillhub.localWorkspace.get',
      payload: { bot_uid: '42' },
      timeoutMs: 5_000,
    });

    vi.advanceTimersByTime(6_001);

    await expect(request).rejects.toMatchObject({ code: 'skillhub_device_timeout' });
  });

  test('honors a sub-second SkillHub device request budget without an extra grace period', async () => {
    api.connectWS(vi.fn());
    const socket = MockWebSocket.instances[0];
    socket.open();
    const request = api.requestSkillHubDeviceTool({
      deviceId: 'alice-device',
      ownerUserId: 7,
      toolName: 'skillhub.localWorkspace.get',
      payload: { bot_uid: '42' },
      timeoutMs: 250,
    });
    const rejection = request.catch((error) => error);

    await vi.advanceTimersByTimeAsync(249);
    expect(await Promise.race([request.then(() => 'settled', () => 'settled'), Promise.resolve('pending')]))
      .toBe('pending');
    await vi.advanceTimersByTimeAsync(1);

    await expect(rejection).resolves.toMatchObject({ code: 'skillhub_device_timeout' });
  });

  test('sends messaging attention in the handshake and on state changes', () => {
    Object.defineProperty(document, 'visibilityState', { configurable: true, value: 'hidden' });
    api.connectWS(vi.fn());
    const socket = MockWebSocket.instances[0];
    socket.open();

    const handshake = JSON.parse(socket.send.mock.calls[0][0]);
    expect(handshake.hi.visibility).toBe('hidden');
    expect(handshake.hi.focused).toBe(false);
    expect(handshake.hi.active_topic).toBe('');
    expect(handshake.hi.push_subscription_id).toBe('');

    api.sendWSPageVisibility('visible');
    expect(JSON.parse(socket.send.mock.calls.at(-1)[0])).toEqual({
      note: {
        what: 'attention',
        visibility: 'visible',
        focused: false,
        active_topic: '',
        push_subscription_id: '',
      },
    });

    api.sendWSActiveTopic('grp_7');
    api.sendWSPageFocus(true);
    expect(JSON.parse(socket.send.mock.calls.at(-1)[0]).note).toMatchObject({
      what: 'attention',
      active_topic: 'grp_7',
      focused: true,
    });

    api.sendWSPageVisibility('unknown');
    expect(JSON.parse(socket.send.mock.calls.at(-1)[0]).note.visibility).toBe('hidden');
  });

  test('derives a stable subscription identity from the push endpoint', async () => {
    const endpoint = 'https://push.example.test/subscription/browser-profile';
    const first = await api.pushSubscriptionIDForEndpoint(endpoint);
    const second = await api.pushSubscriptionIDForEndpoint(endpoint);

    expect(first).toBe(second);
    expect(first).toBe('WUIrC4yppUY8v9TxFnhjVvwOgkISFt0ZOdGvyL0nals');
    expect(first).toMatch(/^[A-Za-z0-9_-]{43}$/);
    expect(await api.pushSubscriptionIDForEndpoint(`${endpoint}-other`)).not.toBe(first);
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

  test('strips response-only Skill metadata before updating BotDefinition', async () => {
    const fetchMock = vi.fn().mockResolvedValue({
      ok: true,
      json: vi.fn().mockResolvedValue({ revision: 4, skills: [] }),
    });
    vi.stubGlobal('fetch', fetchMock);

    await api.api.updateBotDefinitionSkills('42', 3, [{
      source: 'skillhub',
      skillId: 'priv_local1',
      version: 'private-v1',
      contentHash: 'a'.repeat(64),
      displayName: 'read-pdf',
      localName: 'read-pdf',
      localDetails: { path: 'C:\\xiaoba\\skills\\read-pdf' },
    }]);

    expect(fetchMock).toHaveBeenCalledWith(
      '/api/bots/definition/skills?uid=42',
      expect.objectContaining({
        method: 'PATCH',
        body: JSON.stringify({
          revision: 3,
          skills: [{
            source: 'skillhub',
            skillId: 'priv_local1',
            version: 'private-v1',
            contentHash: 'a'.repeat(64),
          }],
        }),
      }),
    );
  });

  test('can remove every tab registration for the current account endpoint', async () => {
    const fetchMock = vi.fn().mockResolvedValue({
      ok: true,
      json: vi.fn().mockResolvedValue({ subscribed: false }),
    });
    vi.stubGlobal('fetch', fetchMock);

    await api.api.unsubscribeAllPushRegistrations('https://push.example/shared');

    expect(JSON.parse(fetchMock.mock.calls[0][1].body)).toEqual({
      endpoint: 'https://push.example/shared',
      all_registrations: true,
    });
  });

  test('can remove an orphaned server registration without a local endpoint', async () => {
    const fetchMock = vi.fn().mockResolvedValue({
      ok: true,
      json: vi.fn().mockResolvedValue({ subscribed: false }),
    });
    vi.stubGlobal('fetch', fetchMock);

    await api.api.unsubscribePushRegistration('registration-current');

    expect(JSON.parse(fetchMock.mock.calls[0][1].body)).toEqual({
      registration_id: 'registration-current',
    });
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

  test('distinguishes a reissued identical token from the previous session', () => {
    const token = 'same-token';
    api.setToken(token);
    const previousRevision = api.getAuthRevision();

    api.setToken(null);
    api.setToken(token);

    expect(api.isCurrentAuthSession(token, previousRevision)).toBe(false);
    expect(api.isCurrentAuthSession(token, api.getAuthRevision())).toBe(true);
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

  test('uses the authenticated account as the push prompt owner', () => {
    const payload = btoa(JSON.stringify({ userId: 42 }))
      .replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '');

    api.setToken(`header.${payload}.signature`);

    expect(api.getPushPromptOwner()).toBe('user:42');
    api.setToken('opaque-token');
    expect(api.getPushPromptOwner()).toBe('');
  });

  test('does not persist a token without a user id as push ownership data', () => {
    api.setToken('opaque-token');

    const firstRegistrationID = api.getPushRegistrationID();

    expect(api.getPushRegistrationID()).not.toBe(firstRegistrationID);
  });

  test('keeps push registration ids scoped to the current tab', () => {
    const payload = btoa(JSON.stringify({ userId: 42 }))
      .replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '');
    api.setToken(`header.${payload}.signature`);

    localStorage.setItem('oc_push_registration_id', 'generation-from-other-tab');
    localStorage.setItem('oc_push_registration_owner', 'user:42');

    expect(api.getPushRegistrationID()).not.toBe('generation-from-other-tab');
  });

  test('keeps a legacy registration id only as a cleanup alias', () => {
    const payload = btoa(JSON.stringify({ userId: 42 }))
      .replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '');
    localStorage.setItem('oc_push_registration_id', 'legacy-registration');
    localStorage.setItem('oc_push_registration_owner', 'user:42');
    api.setToken(`header.${payload}.signature`);

    const tabRegistrationID = api.getPushRegistrationID();

    expect(tabRegistrationID).not.toBe('legacy-registration');
    expect(api.getPushCleanupRegistrationIDs()).toEqual([
      tabRegistrationID,
      'legacy-registration',
    ]);
  });

  test('rotates a tab registration before a new session for the same account', () => {
    const tokenFor = (nonce) => {
      const payload = btoa(JSON.stringify({ userId: 42, nonce }))
        .replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '');
      return `header.${payload}.signature`;
    };
    api.setToken(tokenFor('first'));
    const firstRegistrationID = api.getPushRegistrationID();

    api.setToken(null);
    api.setToken(tokenFor('second'));

    expect(api.getPushRegistrationID()).not.toBe(firstRegistrationID);
  });

  test('keeps the tab registration id after a page reload', async () => {
    const payload = btoa(JSON.stringify({ userId: 42 }))
      .replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '');
    api.setToken(`header.${payload}.signature`);
    const firstRegistrationID = api.getPushRegistrationID();

    vi.resetModules();
    const peer = await import('./api');

    expect(peer.getPushRegistrationID()).toBe(firstRegistrationID);
    peer.disconnectWS();
  });

  test('keeps a session registration through an OAuth return that resets window.name', async () => {
    const payload = btoa(JSON.stringify({ userId: 42 }))
      .replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '');
    api.setToken(`header.${payload}.signature`);
    const firstRegistrationID = api.getPushRegistrationID();

    window.name = 'feishu-oauth-return';
    vi.resetModules();
    const peer = await import('./api');

    expect(peer.getPushRegistrationID()).toBe(firstRegistrationID);
    peer.disconnectWS();
  });

  test('uses the pre-reload registration id for an immediate logout request', async () => {
    const payload = btoa(JSON.stringify({ userId: 42 }))
      .replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '');
    const authenticatedToken = `header.${payload}.signature`;
    api.setToken(authenticatedToken);
    const firstRegistrationID = api.getPushRegistrationID();

    vi.resetModules();
    const peer = await import('./api');
    const logoutRegistrationID = peer.getPushRegistrationID();
    peer.setToken(null);
    const fetchMock = vi.fn().mockResolvedValue({ ok: true, json: vi.fn().mockResolvedValue({}) });
    vi.stubGlobal('fetch', fetchMock);

    await peer.api.unsubscribePush(
      'https://push.example/subscription',
      authenticatedToken,
      logoutRegistrationID,
    );

    expect(logoutRegistrationID).toBe(firstRegistrationID);
    expect(JSON.parse(fetchMock.mock.calls[0][1].body)).toMatchObject({
      registration_id: firstRegistrationID,
    });
    peer.disconnectWS();
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

describe('cloud worker operation request controls', () => {
  let apiModule;

  beforeEach(async () => {
    vi.useFakeTimers();
    vi.resetModules();
    localStorage.clear();
    apiModule = await import('./api');
  });

  afterEach(() => {
    vi.restoreAllMocks();
    vi.useRealTimers();
  });

  test('bounds a long-running update without timing out before the server limit', async () => {
    global.fetch = vi.fn((_url, options) => new Promise((_resolve, reject) => {
      options.signal.addEventListener('abort', () => {
        const error = new Error('aborted');
        error.name = 'AbortError';
        reject(error);
      }, { once: true });
    }));

    const request = apiModule.api.updateCloudWorker('bot-a', { version: '1.4.9' });
    const rejection = expect(request).rejects.toMatchObject({ code: 'REQUEST_TIMEOUT' });
    await vi.advanceTimersByTimeAsync(629_999);
    expect(global.fetch.mock.calls[0][1].signal.aborted).toBe(false);
    await vi.advanceTimersByTimeAsync(1);
    await rejection;
  });
});

describe('agent file requests', () => {
  let apiModule;

  beforeEach(async () => {
    vi.resetModules();
    localStorage.clear();
    global.fetch = vi.fn().mockResolvedValue({
      ok: true,
      json: vi.fn().mockResolvedValue({ files: [] }),
    });
    apiModule = await import('./api');
  });

  afterEach(() => {
    vi.restoreAllMocks();
  });

  test('includes the current conversation and stable cursor in the query', async () => {
    await apiModule.api.getAgentFiles(440, {
      topicId: 'grp_80',
      beforeId: 820,
      limit: 40,
    });

    expect(global.fetch).toHaveBeenCalledWith(
      '/api/agents/440/files?topic_id=grp_80&limit=40&before_id=820',
      expect.objectContaining({ method: 'GET' }),
    );
  });
});

describe('local XiaoBa SkillHub bridge', () => {
  let apiModule;

  beforeEach(async () => {
    vi.resetModules();
    localStorage.clear();
    apiModule = await import('./api');
  });

  afterEach(() => {
    vi.restoreAllMocks();
  });

  test('exchanges the current CatsCo identity before every local SkillHub share', async () => {
    global.fetch = vi.fn()
      .mockResolvedValueOnce({
        ok: true,
        status: 200,
        json: vi.fn().mockResolvedValue({ authenticated: true }),
      })
      .mockResolvedValueOnce({
        ok: true,
        status: 201,
        json: vi.fn().mockResolvedValue({ ok: true, skill: { id: 'owner/demo' } }),
      });

    await expect(apiModule.api.shareLocalSkill('demo', '218', '85')).resolves.toMatchObject({
      ok: true,
      skill: { id: 'owner/demo' },
    });
    expect(global.fetch.mock.calls.map(([url]) => url)).toEqual([
      '/local-xiaoba/api/skillhub/auth/catsco',
      '/local-xiaoba/api/skillhub/share-local-skill',
    ]);
    expect(JSON.parse(global.fetch.mock.calls[1][1].body)).toEqual({
      skillName: 'demo',
      expectedBotUid: '218',
      expectedUserUid: '85',
    });
  });

  test('forwards explicit confirmation when publishing a new local Skill version', async () => {
    global.fetch = vi.fn()
      .mockResolvedValueOnce({
        ok: true,
        status: 200,
        json: vi.fn().mockResolvedValue({ authenticated: true }),
      })
      .mockResolvedValueOnce({
        ok: true,
        status: 201,
        json: vi.fn().mockResolvedValue({ ok: true, skill: { id: 'owner/demo' } }),
      });

    await apiModule.api.shareLocalSkill('demo', '', '85', { confirmPublish: true });

    expect(JSON.parse(global.fetch.mock.calls[1][1].body)).toEqual({
      skillName: 'demo',
      expectedBotUid: '',
      expectedUserUid: '85',
      confirmPublish: true,
    });
  });

  test('falls back to the compatible local Skill list when Dashboard 1.4.6 returns empty objects', async () => {
    const skills = [{ name: 'deep-research', source: 'user' }];
    global.fetch = vi.fn()
      .mockResolvedValueOnce({
        ok: true,
        status: 200,
        json: vi.fn().mockResolvedValue([{}, {}]),
      })
      .mockResolvedValueOnce({
        ok: true,
        status: 200,
        json: vi.fn().mockResolvedValue(skills),
      });

    await expect(apiModule.api.getLocalSkills()).resolves.toEqual(skills);
    expect(global.fetch.mock.calls.map(([url]) => url)).toEqual([
      '/local-xiaoba/api/store',
      '/local-xiaoba/api/skills-all',
    ]);
  });

  test('keeps using the primary local Skill store when it returns named entries', async () => {
    const skills = [{ name: 'agent-browser', source: 'user' }];
    global.fetch = vi.fn().mockResolvedValue({
      ok: true,
      status: 200,
      json: vi.fn().mockResolvedValue(skills),
    });

    await expect(apiModule.api.getLocalSkills()).resolves.toEqual(skills);
    expect(global.fetch).toHaveBeenCalledTimes(1);
    expect(global.fetch.mock.calls[0][0]).toBe('/local-xiaoba/api/store');
  });

  test.each([502, 503, 504])('uses gateway status copy for a local XiaoBa response with HTTP %i', async (status) => {
    const previousFetch = global.fetch;
    global.fetch = vi.fn().mockResolvedValue({
      ok: false,
      status,
      json: vi.fn().mockResolvedValue({ error: '后端暂时无法载入' }),
    });

    try {
      await expect(apiModule.api.getLocalSkills()).rejects.toMatchObject({
        message: '服务暂时不可用，请稍后重试',
        status,
        data: { error: '后端暂时无法载入' },
      });
    } finally {
      global.fetch = previousFetch;
    }
  });

  test('loads all files from the current conversation without an Agent scope', async () => {
    await apiModule.api.getTopicFiles('grp_80', {
      beforeId: 820,
      limit: 40,
    });

    expect(global.fetch).toHaveBeenCalledWith(
      '/api/topics/grp_80/files?limit=40&before_id=820',
      expect.objectContaining({ method: 'GET' }),
    );
  });

  test('publishes an Agent result through the immediate-publish endpoint', async () => {
    await apiModule.api.publishCloudArtifact(440, {
      title: '课堂网页',
      kind: 'html',
      url: 'https://app.example/uploads/files/result.html',
      source_topic_id: 'p2p_7_440',
    });

    expect(global.fetch).toHaveBeenCalledWith(
      '/api/agents/440/artifacts',
      expect.objectContaining({
        method: 'POST',
        body: JSON.stringify({
          title: '课堂网页',
          kind: 'html',
          url: 'https://app.example/uploads/files/result.html',
          source_topic_id: 'p2p_7_440',
        }),
      }),
    );
  });
});

describe('upload transport', () => {
  let apiModule;

  const response = (status, data, raw = JSON.stringify(data)) => ({
    ok: status >= 200 && status < 300,
    status,
    json: vi.fn().mockResolvedValue(data),
    text: vi.fn().mockResolvedValue(raw),
  });

  beforeEach(async () => {
    vi.resetModules();
    localStorage.clear();
    sessionStorage.clear();
    apiModule = await import('./api');
  });

  afterEach(() => {
    apiModule.disconnectWS();
    vi.restoreAllMocks();
  });

  test('sends authenticated composer uploads as the original file body', async () => {
    global.fetch = vi.fn().mockResolvedValue(response(200, {
      file_key: 'stored.jpg',
      name: '试卷 01.jpg',
      size: 5,
      type: 'image',
      url: '/uploads/images/stored.jpg',
    }));
    apiModule.setToken('upload-token');
    const file = new File(['paper'], '试卷 01.jpg', { type: 'image/jpeg' });

    await apiModule.api.uploadFile(file, 'image');

    expect(global.fetch).toHaveBeenCalledTimes(1);
    const [url, options] = global.fetch.mock.calls[0];
    expect(url).toBe('/api/upload?type=image&raw=1');
    expect(options.body).toBe(file);
    expect(options.headers).toMatchObject({
      Authorization: 'Bearer upload-token',
      'Content-Type': 'image/jpeg',
      'X-CatsCo-File-Name': encodeURIComponent(file.name),
      'X-CatsCo-File-Size': String(file.size),
    });
  });

  test('retries a phone upload once when the server confirms no complete file was stored', async () => {
    global.fetch = vi.fn()
      .mockResolvedValueOnce(response(400, {
        code: 'upload_incomplete',
        error: 'upload request is incomplete; please retry',
        retryable: true,
      }))
      .mockResolvedValueOnce(response(200, {
        file_key: 'stored.jpg',
        name: 'paper.jpg',
        size: 5,
        type: 'image',
        url: '/uploads/images/stored.jpg',
      }));
    const file = new File(['paper'], 'paper.jpg', { type: 'image/jpeg' });

    await expect(apiModule.api.uploadMobileSessionFile('session-1', file, 'image')).resolves.toMatchObject({
      file_key: 'stored.jpg',
    });

    expect(global.fetch).toHaveBeenCalledTimes(2);
    for (const [url, options] of global.fetch.mock.calls) {
      expect(url).toBe('/api/mobile-upload/sessions/session-1/files?type=image&raw=1');
      expect(options.body).toBe(file);
      expect(options.headers).toMatchObject({
        'Content-Type': 'image/jpeg',
        'X-CatsCo-File-Name': encodeURIComponent(file.name),
        'X-CatsCo-File-Size': String(file.size),
      });
    }
  });

  test('stops after one retry and returns an actionable mobile error', async () => {
    const incomplete = {
      code: 'upload_incomplete',
      error: 'upload request is incomplete; please retry',
      retryable: true,
    };
    global.fetch = vi.fn()
      .mockResolvedValueOnce(response(400, incomplete))
      .mockResolvedValueOnce(response(400, incomplete));
    const file = new File(['paper'], 'paper.jpg', { type: 'image/jpeg' });

    await expect(apiModule.api.uploadMobileSessionFile('session-1', file, 'image')).rejects.toMatchObject({
      code: 'upload_incomplete',
      message: '上传过程中断，请重新选择该文件后重试。',
    });
    expect(global.fetch).toHaveBeenCalledTimes(2);
  });

  test('does not retry when the upload response body is interrupted', async () => {
    const bodyReadFailure = new TypeError('Load failed');
    global.fetch = vi.fn().mockResolvedValue({
      ok: true,
      status: 200,
      text: vi.fn().mockRejectedValue(bodyReadFailure),
    });
    const file = new File(['paper'], 'paper.jpg', { type: 'image/jpeg' });

    await expect(apiModule.api.uploadMobileSessionFile('session-1', file, 'image')).rejects.toMatchObject({
      code: 'upload_response_interrupted',
      message: '上传响应中断，无法确认是否成功；请刷新页面查看“已上传”列表，确认后再重试。',
      cause: bodyReadFailure,
    });
    expect(global.fetch).toHaveBeenCalledTimes(1);
  });

  test('uses a generic actionable error when a composer upload response is interrupted', async () => {
    global.fetch = vi.fn().mockResolvedValue({
      ok: true,
      status: 200,
      text: vi.fn().mockRejectedValue(new TypeError('Load failed')),
    });
    const file = new File(['paper'], 'paper.jpg', { type: 'image/jpeg' });

    await expect(apiModule.api.uploadFile(file, 'image')).rejects.toMatchObject({
      code: 'upload_response_interrupted',
      message: '上传响应中断，无法确认是否成功；请检查网络后重新选择该文件。',
    });
    expect(global.fetch).toHaveBeenCalledTimes(1);
  });

  test.each([502, 503, 504])('uses gateway status copy for an upload JSON response with HTTP %i', async (status) => {
    global.fetch = vi.fn().mockResolvedValue(response(status, {
      error: '后端服务暂时异常',
    }));
    const file = new File(['paper'], 'paper.jpg', { type: 'image/jpeg' });

    await expect(apiModule.api.uploadFile(file, 'image')).rejects.toMatchObject({
      message: '服务暂时不可用，请稍后重试',
      status,
      data: { error: '后端服务暂时异常' },
    });
  });
});

describe('direct remote response errors', () => {
  let apiModule;

  beforeEach(async () => {
    vi.resetModules();
    localStorage.clear();
    sessionStorage.clear();
    apiModule = await import('./api');
  });

  afterEach(() => {
    apiModule.disconnectWS();
    vi.restoreAllMocks();
  });

  test.each([502, 503, 504])('uses gateway status copy for friend acceptance with HTTP %i', async (status) => {
    global.fetch = vi.fn().mockResolvedValue({
      ok: false,
      status,
      json: vi.fn().mockResolvedValue({ error: '后端服务暂时异常' }),
    });

    await expect(apiModule.api.acceptFriendAsBot('api-key', 42)).rejects.toMatchObject({
      message: '服务暂时不可用，请稍后重试',
      status,
      data: { error: '后端服务暂时异常' },
    });
  });

  test('normalizes a friend-acceptance network failure', async () => {
    global.fetch = vi.fn().mockRejectedValue(new TypeError('Failed to fetch'));

    await expect(apiModule.api.acceptFriendAsBot('api-key', 42)).rejects.toMatchObject({
      code: 'NETWORK_ERROR',
      message: '暂时无法连接服务，请稍后重试',
    });
  });
});
