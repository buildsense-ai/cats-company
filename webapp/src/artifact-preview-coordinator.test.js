import {
  ARTIFACT_PREVIEW_COORDINATION_CONTRACT,
  ARTIFACT_VIEWER_CONTEXT_TIMEOUT_MS,
  ARTIFACT_VIEWER_HEARTBEAT_TTL_MS,
  ARTIFACT_VIEWER_PATH,
  createArtifactPreviewChatCoordinator,
  createArtifactPreviewLeaseStore,
  createArtifactPreviewMessage,
  createArtifactViewerURL,
  normalizeArtifactPreviewIdentity,
  normalizeArtifactPreviewMessage,
  parseArtifactViewerLocation,
  sameArtifactPreviewIdentity,
} from './artifact-preview-coordinator';

const identity = {
  topicId: 'p2p_7_440',
  agentUid: 440,
  artifactId: 'project-risk-register',
  displayedVersion: 2,
};

test('builds a CatsCo Viewer URL from identity without accepting an Artifact URL', () => {
  const value = createArtifactViewerURL({
    ...identity,
    url: 'https://untrusted.example/ignored/',
  }, {
    handoffId: 'handoff_12345678',
    origin: 'https://app.catsco.cc',
  });
  const url = new URL(value);

  expect(url.origin).toBe('https://app.catsco.cc');
  expect(url.pathname).toBe(ARTIFACT_VIEWER_PATH);
  expect(Object.fromEntries(url.searchParams)).toEqual({
    topic: 'p2p_7_440',
    agent: '440',
    artifact: 'project-risk-register',
    version: '2',
    handoff: 'handoff_12345678',
  });
  expect(value).not.toContain('untrusted.example');
});

test('parses only complete, bounded Viewer identities', () => {
  expect(parseArtifactViewerLocation({
    pathname: ARTIFACT_VIEWER_PATH,
    search: '?topic=p2p_7_440&agent=440&artifact=project-risk-register&version=2&handoff=handoff_12345678',
  })).toEqual({ ...identity, handoffId: 'handoff_12345678' });

  expect(parseArtifactViewerLocation({
    pathname: ARTIFACT_VIEWER_PATH,
    search: '?topic=p2p_7_440&agent=440&artifact=../../bad&version=2&handoff=handoff_12345678',
  })).toBeNull();
  expect(parseArtifactViewerLocation({ pathname: '/other', search: '' })).toBeNull();
});

test('normalizes coordination messages and keeps exact versions in the identity', () => {
  const message = createArtifactPreviewMessage('viewer_ready', identity, {
    viewer_id: 'viewer_12345678',
    handoff_id: 'handoff_12345678',
    context_ref: `acr_${'x'.repeat(43)}`,
    sent_at: 123,
  });

  expect(message.contract_version).toBe(ARTIFACT_PREVIEW_COORDINATION_CONTRACT);
  expect(normalizeArtifactPreviewMessage(message)).toEqual({
    type: 'viewer_ready',
    ...identity,
    viewerId: 'viewer_12345678',
    handoffId: 'handoff_12345678',
    contextRef: `acr_${'x'.repeat(43)}`,
    sentAt: 123,
  });
  expect(sameArtifactPreviewIdentity(identity, { ...identity, displayedVersion: 3 })).toBe(false);
  expect(normalizeArtifactPreviewIdentity({ ...identity, agentUid: 0 })).toBeNull();
});

test('stores only a complete exact Viewer lease for coordinator recovery', () => {
  const values = new Map();
  const storage = {
    getItem: (key) => values.get(key) || null,
    setItem: (key, value) => values.set(key, value),
    removeItem: (key) => values.delete(key),
  };
  const store = createArtifactPreviewLeaseStore(storage);

  expect(store.write({
    ...identity,
    viewerId: 'viewer_12345678',
    handoffId: 'handoff_12345678',
  })).toBe(true);
  expect(store.read()).toEqual({
    ...identity,
    viewerId: 'viewer_12345678',
    handoffId: 'handoff_12345678',
  });
  expect(store.write({ ...identity, viewerId: 'viewer_12345678' })).toBe(false);
  store.clear();
  expect(store.read()).toBeNull();
});

function fakeChannel() {
  return {
    onmessage: null,
    posted: [],
    closed: false,
    postMessage(message) {
      this.posted.push(message);
    },
    close() {
      this.closed = true;
    },
    receive(message) {
      this.onmessage?.({ data: message });
    },
  };
}

test('accepts a handoff only from the exact Viewer identity and version', async () => {
  vi.useFakeTimers();
  try {
    const channel = fakeChannel();
    const coordinator = createArtifactPreviewChatCoordinator({ channel });
    const control = coordinator.beginHandoff(identity, 'handoff_12345678');

    channel.receive(createArtifactPreviewMessage('viewer_ready', {
      ...identity,
      displayedVersion: 3,
    }, {
      viewer_id: 'viewer_wrongversion',
      handoff_id: 'handoff_12345678',
    }));
    expect(coordinator.getActiveViewer()).toBeNull();

    channel.receive(createArtifactPreviewMessage('viewer_ready', identity, {
      viewer_id: 'viewer_12345678',
      handoff_id: 'handoff_12345678',
    }));
    await expect(control.promise).resolves.toMatchObject({
      ...identity,
      viewerId: 'viewer_12345678',
    });
    expect(coordinator.getActiveViewer(identity)).toMatchObject({
      handoffId: 'handoff_12345678',
      viewerId: 'viewer_12345678',
    });
    coordinator.close();
  } finally {
    vi.useRealTimers();
  }
});

test('requests fresh context even when background-tab heartbeats are throttled', async () => {
  vi.useFakeTimers();
  try {
    let currentTime = 1000;
    const channel = fakeChannel();
    const coordinator = createArtifactPreviewChatCoordinator({
      channel,
      now: () => currentTime,
    });
    const control = coordinator.beginHandoff(identity, 'handoff_12345678');
    channel.receive(createArtifactPreviewMessage('viewer_ready', identity, {
      viewer_id: 'viewer_12345678',
      handoff_id: 'handoff_12345678',
    }));
    await control.promise;

    const contextPromise = coordinator.requestContext(identity);
    const request = channel.posted.find((message) => message.type === 'context_request');
    expect(request).toMatchObject({
      viewer_id: 'viewer_12345678',
      artifact_id: identity.artifactId,
      displayed_version: 2,
    });
    channel.receive(createArtifactPreviewMessage('context_response', identity, {
      viewer_id: 'viewer_12345678',
      handoff_id: 'handoff_12345678',
      request_id: request.request_id,
      context_ref: `acr_${'z'.repeat(43)}`,
    }));
    await expect(contextPromise).resolves.toBe(`acr_${'z'.repeat(43)}`);

    currentTime += ARTIFACT_VIEWER_HEARTBEAT_TTL_MS + 1;
    expect(coordinator.getActiveViewer()).toMatchObject({
      viewerId: 'viewer_12345678',
      heartbeatStale: true,
    });
    const throttledContextPromise = coordinator.requestContext(identity);
    const throttledRequest = channel.posted.at(-1);
    channel.receive(createArtifactPreviewMessage('context_response', identity, {
      viewer_id: 'viewer_12345678',
      handoff_id: 'handoff_12345678',
      request_id: throttledRequest.request_id,
      context_ref: `acr_${'q'.repeat(43)}`,
    }));
    await expect(throttledContextPromise).resolves.toBe(`acr_${'q'.repeat(43)}`);
    expect(coordinator.getActiveViewer()?.heartbeatStale).toBe(false);
    coordinator.close();
  } finally {
    vi.useRealTimers();
  }
});

test('allows a slow but valid context response within the full Viewer capture budget', async () => {
  vi.useFakeTimers();
  try {
    const channel = fakeChannel();
    const coordinator = createArtifactPreviewChatCoordinator({ channel });
    const control = coordinator.beginHandoff(identity, 'handoff_12345678');
    channel.receive(createArtifactPreviewMessage('viewer_ready', identity, {
      viewer_id: 'viewer_12345678',
      handoff_id: 'handoff_12345678',
    }));
    await control.promise;

    let settled = false;
    const contextPromise = coordinator.requestContext(identity).then((value) => {
      settled = true;
      return value;
    });
    const request = channel.posted.find((message) => message.type === 'context_request');
    await vi.advanceTimersByTimeAsync(ARTIFACT_VIEWER_CONTEXT_TIMEOUT_MS - 750);

    expect(settled).toBe(false);
    expect(coordinator.getActiveViewer()).toMatchObject({ viewerId: 'viewer_12345678' });
    channel.receive(createArtifactPreviewMessage('context_response', identity, {
      viewer_id: 'viewer_12345678',
      handoff_id: 'handoff_12345678',
      request_id: request.request_id,
      context_ref: `acr_${'s'.repeat(43)}`,
    }));

    await expect(contextPromise).resolves.toBe(`acr_${'s'.repeat(43)}`);
    coordinator.close();
  } finally {
    vi.useRealTimers();
  }
});

test('serializes rapid context requests so each gets its own capture timeout budget', async () => {
  vi.useFakeTimers();
  try {
    const channel = fakeChannel();
    const coordinator = createArtifactPreviewChatCoordinator({ channel });
    const control = coordinator.beginHandoff(identity, 'handoff_12345678');
    channel.receive(createArtifactPreviewMessage('viewer_ready', identity, {
      viewer_id: 'viewer_12345678',
      handoff_id: 'handoff_12345678',
    }));
    await control.promise;

    const firstPromise = coordinator.requestContext(identity);
    const secondPromise = coordinator.requestContext(identity);
    expect(channel.posted.filter((message) => message.type === 'context_request')).toHaveLength(1);

    await vi.advanceTimersByTimeAsync(ARTIFACT_VIEWER_CONTEXT_TIMEOUT_MS - 1000);
    const firstRequest = channel.posted.find((message) => message.type === 'context_request');
    channel.receive(createArtifactPreviewMessage('context_response', identity, {
      viewer_id: 'viewer_12345678',
      handoff_id: 'handoff_12345678',
      request_id: firstRequest.request_id,
      context_ref: `acr_${'f'.repeat(43)}`,
    }));
    await expect(firstPromise).resolves.toBe(`acr_${'f'.repeat(43)}`);

    const requests = channel.posted.filter((message) => message.type === 'context_request');
    expect(requests).toHaveLength(2);
    await vi.advanceTimersByTimeAsync(ARTIFACT_VIEWER_CONTEXT_TIMEOUT_MS - 1000);
    channel.receive(createArtifactPreviewMessage('context_response', identity, {
      viewer_id: 'viewer_12345678',
      handoff_id: 'handoff_12345678',
      request_id: requests[1].request_id,
      context_ref: `acr_${'g'.repeat(43)}`,
    }));
    await expect(secondPromise).resolves.toBe(`acr_${'g'.repeat(43)}`);
    coordinator.close();
  } finally {
    vi.useRealTimers();
  }
});

test('keeps one timeout recoverable and accepts an exact late response after stale detachment', async () => {
  vi.useFakeTimers();
  try {
    let currentTime = 1000;
    const channel = fakeChannel();
    const coordinator = createArtifactPreviewChatCoordinator({
      channel,
      now: () => currentTime,
    });
    const control = coordinator.beginHandoff(identity, 'handoff_12345678');
    channel.receive(createArtifactPreviewMessage('viewer_ready', identity, {
      viewer_id: 'viewer_12345678',
      handoff_id: 'handoff_12345678',
    }));
    await control.promise;

    currentTime += ARTIFACT_VIEWER_HEARTBEAT_TTL_MS + 1;
    const firstPromise = coordinator.requestContext(identity, { timeoutMs: 25 });
    await vi.advanceTimersByTimeAsync(25);
    await expect(firstPromise).resolves.toBe('');
    expect(coordinator.getActiveViewer()).toMatchObject({ viewerId: 'viewer_12345678' });

    const secondPromise = coordinator.requestContext(identity, { timeoutMs: 25 });
    const secondRequest = channel.posted.filter((message) => message.type === 'context_request').at(-1);
    await vi.advanceTimersByTimeAsync(25);
    await expect(secondPromise).resolves.toBe('');
    expect(coordinator.getActiveViewer()).toBeNull();

    channel.receive(createArtifactPreviewMessage('context_response', identity, {
      viewer_id: 'viewer_12345678',
      handoff_id: 'handoff_12345678',
      request_id: secondRequest.request_id,
      context_ref: `acr_${'l'.repeat(43)}`,
    }));
    expect(coordinator.getActiveViewer()).toMatchObject({ viewerId: 'viewer_12345678' });

    coordinator.close();
  } finally {
    vi.useRealTimers();
  }
});

test('rediscovers an open Viewer after coordinator remount without accepting another page', async () => {
  vi.useFakeTimers();
  try {
    let storedLease = null;
    const firstChannel = fakeChannel();
    const firstCoordinator = createArtifactPreviewChatCoordinator({
      channel: firstChannel,
      onViewerLeaseChange: (lease) => { storedLease = lease; },
    });
    const control = firstCoordinator.beginHandoff(identity, 'handoff_12345678');
    firstChannel.receive(createArtifactPreviewMessage('viewer_ready', identity, {
      viewer_id: 'viewer_before_reload',
      handoff_id: 'handoff_12345678',
    }));
    await control.promise;
    firstCoordinator.close();

    const secondChannel = fakeChannel();
    const secondCoordinator = createArtifactPreviewChatCoordinator({
      channel: secondChannel,
      recoveryLease: storedLease,
      onViewerLeaseChange: (lease) => { storedLease = lease; },
    });
    expect(secondCoordinator.getActiveViewer()).toBeNull();
    expect(secondChannel.posted[0]).toMatchObject({
      type: 'request_current_preview',
      viewer_id: 'viewer_before_reload',
      handoff_id: 'handoff_12345678',
    });

    secondChannel.receive(createArtifactPreviewMessage('viewer_hello', identity, {
      viewer_id: 'viewer_after_reload',
      handoff_id: 'handoff_12345678',
    }));
    const discovery = secondChannel.posted.find((message) => (
      message.type === 'request_current_preview' && message.viewer_id === 'viewer_after_reload'
    ));
    expect(discovery).toBeTruthy();

    secondChannel.receive(createArtifactPreviewMessage('current_preview', {
      ...identity,
      displayedVersion: 3,
    }, {
      viewer_id: 'viewer_after_reload',
      handoff_id: 'handoff_12345678',
      request_id: discovery.request_id,
    }));
    secondChannel.receive(createArtifactPreviewMessage('current_preview', identity, {
      viewer_id: 'viewer_wrongpage1',
      handoff_id: 'handoff_12345678',
      request_id: discovery.request_id,
    }));
    expect(secondCoordinator.getActiveViewer()).toBeNull();

    secondChannel.receive(createArtifactPreviewMessage('current_preview', identity, {
      viewer_id: 'viewer_after_reload',
      handoff_id: 'handoff_12345678',
      request_id: discovery.request_id,
      context_ref: `acr_${'r'.repeat(43)}`,
    }));
    expect(secondCoordinator.getActiveViewer()).toMatchObject({
      ...identity,
      viewerId: 'viewer_after_reload',
      handoffId: 'handoff_12345678',
    });
    expect(storedLease).toMatchObject({ viewerId: 'viewer_after_reload' });

    const oldDiscovery = secondChannel.posted[0];
    secondChannel.receive(createArtifactPreviewMessage('current_preview', identity, {
      viewer_id: 'viewer_before_reload',
      handoff_id: 'handoff_12345678',
      request_id: oldDiscovery.request_id,
      context_ref: `acr_${'o'.repeat(43)}`,
    }));
    expect(secondCoordinator.getActiveViewer()).toMatchObject({ viewerId: 'viewer_after_reload' });
    secondCoordinator.close();
  } finally {
    vi.useRealTimers();
  }
});

test('claims every Viewer on the recovering handoff before a refreshed candidate is confirmed', () => {
  vi.useFakeTimers();
  try {
    let storedLease = {
      ...identity,
      viewerId: 'viewer_before_reload',
      handoffId: 'handoff_12345678',
    };
    const channel = fakeChannel();
    const coordinator = createArtifactPreviewChatCoordinator({
      channel,
      recoveryLease: storedLease,
      onViewerLeaseChange: (lease) => { storedLease = lease; },
    });

    channel.receive(createArtifactPreviewMessage('viewer_hello', identity, {
      viewer_id: 'viewer_after_reload',
      handoff_id: 'handoff_12345678',
    }));
    const discovery = channel.posted.find((message) => (
      message.type === 'request_current_preview' && message.viewer_id === 'viewer_after_reload'
    ));
    expect(discovery).toBeTruthy();
    expect(coordinator.getActiveViewer()).toBeNull();

    coordinator.claimSidebar();

    const claim = channel.posted.at(-1);
    expect(claim).toMatchObject({
      type: 'sidebar_claimed',
      topic_id: identity.topicId,
      agent_uid: identity.agentUid,
      artifact_id: identity.artifactId,
      displayed_version: identity.displayedVersion,
      handoff_id: 'handoff_12345678',
    });
    expect(claim).not.toHaveProperty('viewer_id');
    expect(storedLease).toBeNull();

    channel.receive(createArtifactPreviewMessage('current_preview', identity, {
      viewer_id: 'viewer_after_reload',
      handoff_id: 'handoff_12345678',
      request_id: discovery.request_id,
      context_ref: `acr_${'x'.repeat(43)}`,
    }));
    channel.receive(createArtifactPreviewMessage('viewer_ready', identity, {
      viewer_id: 'viewer_after_reload',
      handoff_id: 'handoff_12345678',
      context_ref: `acr_${'y'.repeat(43)}`,
    }));
    expect(coordinator.getActiveViewer()).toBeNull();
    coordinator.close();
  } finally {
    vi.useRealTimers();
  }
});

test('claiming the sidebar releases the Viewer and cancels pending context requests', async () => {
  vi.useFakeTimers();
  try {
    const channel = fakeChannel();
    const coordinator = createArtifactPreviewChatCoordinator({ channel });
    const control = coordinator.beginHandoff(identity, 'handoff_12345678');
    channel.receive(createArtifactPreviewMessage('viewer_ready', identity, {
      viewer_id: 'viewer_12345678',
      handoff_id: 'handoff_12345678',
    }));
    await control.promise;
    const contextPromise = coordinator.requestContext(identity);

    coordinator.claimSidebar();

    await expect(contextPromise).resolves.toBe('');
    expect(coordinator.getActiveViewer()).toBeNull();
    expect(channel.posted.at(-1)).toMatchObject({
      type: 'sidebar_claimed',
      viewer_id: 'viewer_12345678',
      handoff_id: 'handoff_12345678',
    });
    coordinator.close();
  } finally {
    vi.useRealTimers();
  }
});

test('lets the same Viewer lease reconnect with a new session after a page refresh', async () => {
  vi.useFakeTimers();
  try {
    const channel = fakeChannel();
    const coordinator = createArtifactPreviewChatCoordinator({ channel });
    const control = coordinator.beginHandoff(identity, 'handoff_12345678');
    channel.receive(createArtifactPreviewMessage('viewer_ready', identity, {
      viewer_id: 'viewer_before_refresh',
      handoff_id: 'handoff_12345678',
    }));
    await control.promise;

    channel.receive(createArtifactPreviewMessage('viewer_closed', identity, {
      viewer_id: 'viewer_before_refresh',
      handoff_id: 'handoff_12345678',
    }));
    expect(coordinator.getActiveViewer()).toBeNull();

    channel.receive(createArtifactPreviewMessage('viewer_ready', identity, {
      viewer_id: 'viewer_after_refresh',
      handoff_id: 'handoff_12345678',
    }));
    expect(coordinator.getActiveViewer()).toMatchObject({
      viewerId: 'viewer_after_refresh',
      handoffId: 'handoff_12345678',
    });
    coordinator.close();
  } finally {
    vi.useRealTimers();
  }
});
