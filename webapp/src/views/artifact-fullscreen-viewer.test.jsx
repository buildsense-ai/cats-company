import React, { act } from 'react';
import { createRoot } from 'react-dom/client';

const mocks = vi.hoisted(() => ({
  sessionReady: false,
  wsHandler: null,
  getCloudArtifacts: vi.fn(),
  createArtifactContextSnapshot: vi.fn(),
  invalidateArtifactContextSnapshot: vi.fn(),
  connectWS: vi.fn(),
  reconnectWS: vi.fn(),
  disconnectWS: vi.fn(),
  sendWSActiveTopic: vi.fn(),
  sendWSPageFocus: vi.fn(),
  sendWSPageVisibility: vi.fn(),
  setToken: vi.fn(),
  wsSendArtifactResultReceipt: vi.fn(),
  requestArtifactPageContext: vi.fn(),
  requestArtifactResultApply: vi.fn(),
}));

vi.mock('../api', () => ({
  api: {
    getCloudArtifacts: mocks.getCloudArtifacts,
    createArtifactContextSnapshot: mocks.createArtifactContextSnapshot,
    invalidateArtifactContextSnapshot: mocks.invalidateArtifactContextSnapshot,
  },
  connectWS: mocks.connectWS,
  reconnectWS: mocks.reconnectWS,
  disconnectWS: mocks.disconnectWS,
  hasArtifactPreviewSession: () => mocks.sessionReady,
  sendWSActiveTopic: mocks.sendWSActiveTopic,
  sendWSPageFocus: mocks.sendWSPageFocus,
  sendWSPageVisibility: mocks.sendWSPageVisibility,
  setToken: mocks.setToken,
  wsSendArtifactResultReceipt: mocks.wsSendArtifactResultReceipt,
}));

vi.mock('../artifact-context', () => ({
  artifactContextRefFromSnapshot: (value) => (
    value?.contract_version === 'catsco.artifact-context-ref.v1'
      && /^acr_[A-Za-z0-9_-]{43}$/.test(String(value?.context_ref || ''))
      ? value.context_ref
      : ''
  ),
  artifactRefFromPreviewFile: (file, expectedAgentUID) => (
    file?.artifact_id && Number(file?.artifact_agent_uid) === Number(expectedAgentUID)
      ? {
          contract_version: 'catsco.artifact-ref.v1',
          id: file.artifact_id,
          displayed_version: Number(file.publish_version),
          currently_visible: true,
        }
      : null
  ),
  artifactURLForVersion: (url, version) => String(url || '')
    .replace(/\/(?:latest|v\d+)\/$/, `/v${Number(version)}/`),
  normalizeArtifactResultDelivery: (value) => value || null,
  requestArtifactPageContext: mocks.requestArtifactPageContext,
  requestArtifactResultApply: mocks.requestArtifactResultApply,
}));

vi.mock('../widgets/chat-message', () => ({
  createCloudArtifactPreviewFile: (artifact) => ({
    name: artifact.title || artifact.id,
    url: artifact.url,
    mime_type: 'text/html',
    artifact_id: artifact.id,
    publish_version: artifact.publish_version,
    artifact_agent_uid: Number(artifact.agent_uid),
  }),
  previewFileDescriptor: (file) => ({
    url: file?.url || '',
    isRemoteArtifact: Boolean(file?.artifact_id),
    isSameOriginRemoteArtifact: false,
  }),
}));

vi.mock('../widgets/controlled-artifact-preview', () => ({
  default: function MockControlledArtifactPreview({ file, onBindingChange }) {
    const binding = React.useMemo(() => ({
      frame: { contentWindow: {} },
      artifactId: file.artifact_id,
      agentUid: file.artifact_agent_uid,
      url: file.url,
      signal: new AbortController().signal,
    }), [file]);
    React.useEffect(() => {
      onBindingChange?.(binding);
      return () => onBindingChange?.(null);
    }, [binding, onBindingChange]);
    return <iframe title="mock-artifact" data-url={file.url} />;
  },
}));

import ArtifactFullscreenViewer from './artifact-fullscreen-viewer';
import { createArtifactPreviewMessage } from '../artifact-preview-coordinator';

const channels = [];

class MockBroadcastChannel {
  constructor(name) {
    this.name = name;
    this.onmessage = null;
    this.posted = [];
    this.closed = false;
    channels.push(this);
  }

  postMessage(message) {
    this.posted.push(message);
  }

  receive(message) {
    this.onmessage?.({ data: message });
  }

  close() {
    this.closed = true;
  }
}

const identity = {
  topicId: 'p2p_1_440',
  agentUid: 440,
  artifactId: 'risk-register',
  displayedVersion: 2,
};

const location = {
  pathname: '/artifact-viewer',
  search: '?topic=p2p_1_440&agent=440&artifact=risk-register&version=2&handoff=handoff_12345678',
};

async function flushPromises(count = 8) {
  for (let index = 0; index < count; index += 1) await Promise.resolve();
}

describe('ArtifactFullscreenViewer', () => {
  let container;
  let root;

  beforeEach(() => {
    global.IS_REACT_ACT_ENVIRONMENT = true;
    channels.length = 0;
    mocks.sessionReady = false;
    mocks.wsHandler = null;
    mocks.getCloudArtifacts.mockResolvedValue({
      artifacts: [{
        id: 'risk-register',
        title: '项目风险台账',
        url: 'https://artifacts.example.test/by-agent/440/risk-register/latest/',
        publish_version: 3,
        agent_uid: 440,
      }],
    });
    mocks.createArtifactContextSnapshot.mockResolvedValue({
      contract_version: 'catsco.artifact-context-ref.v1',
      context_ref: `acr_${'a'.repeat(43)}`,
    });
    mocks.invalidateArtifactContextSnapshot.mockResolvedValue({ ok: true });
    mocks.requestArtifactPageContext.mockResolvedValue({
      contract_version: 'catsco.artifact-page-context.v1',
      observed_at: '2026-08-26T08:00:00Z',
      semantic_context: { view: 'risk-register' },
    });
    mocks.requestArtifactResultApply.mockResolvedValue({
      contract_version: 'catsco.artifact-result-receipt.v1',
      result_id: `arr_${'r'.repeat(43)}`,
      status: 'applied',
    });
    mocks.connectWS.mockImplementation((handler) => {
      mocks.wsHandler = handler;
      return true;
    });
    vi.stubGlobal('BroadcastChannel', MockBroadcastChannel);
    container = document.createElement('div');
    document.body.appendChild(container);
    root = createRoot(container);
  });

  afterEach(async () => {
    await act(async () => root.unmount());
    container.remove();
    vi.unstubAllGlobals();
    vi.clearAllMocks();
  });

  it('loads exact vN and reports ready only after its own preview session and snapshot exist', async () => {
    await act(async () => {
      root.render(<ArtifactFullscreenViewer location={location} />);
      await flushPromises();
    });

    expect(container.querySelector('iframe[data-url]')?.getAttribute('data-url')).toBe(
      'https://artifacts.example.test/by-agent/440/risk-register/v2/',
    );
    expect(channels[0].posted.some((message) => message.type === 'viewer_ready')).toBe(false);
    expect(mocks.sendWSActiveTopic).toHaveBeenCalledWith('p2p_1_440');

    await act(async () => {
      mocks.sessionReady = true;
      mocks.wsHandler?.({ ctrl: { params: { artifact_preview_session: {} } } });
      await flushPromises();
    });

    const ready = channels[0].posted.find((message) => message.type === 'viewer_ready');
    expect(ready).toMatchObject({
      topic_id: 'p2p_1_440',
      agent_uid: 440,
      artifact_id: 'risk-register',
      displayed_version: 2,
      handoff_id: 'handoff_12345678',
      context_ref: `acr_${'a'.repeat(43)}`,
    });
    expect(mocks.createArtifactContextSnapshot).toHaveBeenCalledWith({
      topic_id: 'p2p_1_440',
      artifact_ref: {
        contract_version: 'catsco.artifact-ref.v1',
        id: 'risk-register',
        displayed_version: 2,
        currently_visible: true,
      },
      page_context: expect.objectContaining({
        contract_version: 'catsco.artifact-page-context.v1',
      }),
    }, { timeoutMs: 3000 });
  });

  it('creates fresh context on request, applies V3.2 results, and releases ownership to the sidebar', async () => {
    mocks.createArtifactContextSnapshot
      .mockResolvedValueOnce({
        contract_version: 'catsco.artifact-context-ref.v1',
        context_ref: `acr_${'a'.repeat(43)}`,
      })
      .mockResolvedValueOnce({
        contract_version: 'catsco.artifact-context-ref.v1',
        context_ref: `acr_${'b'.repeat(43)}`,
      });
    await act(async () => {
      root.render(<ArtifactFullscreenViewer location={location} />);
      await flushPromises();
    });
    await act(async () => {
      mocks.sessionReady = true;
      mocks.wsHandler?.({ ctrl: { params: { artifact_preview_session: {} } } });
      await flushPromises();
    });
    const channel = channels[0];
    const ready = channel.posted.find((message) => message.type === 'viewer_ready');

    await act(async () => {
      channel.receive(createArtifactPreviewMessage('request_current_preview', identity, {
        viewer_id: 'viewer_wrongpage1',
        handoff_id: 'handoff_12345678',
        request_id: 'discover_wrong123',
      }));
      channel.receive(createArtifactPreviewMessage('request_current_preview', identity, {
        viewer_id: ready.viewer_id,
        handoff_id: 'handoff_12345678',
        request_id: 'discover_12345678',
      }));
      await flushPromises();
    });
    expect(channel.posted.some((message) => (
      message.type === 'current_preview' && message.request_id === 'discover_wrong123'
    ))).toBe(false);
    expect(channel.posted.find((message) => (
      message.type === 'current_preview' && message.request_id === 'discover_12345678'
    ))).toMatchObject({
      viewer_id: ready.viewer_id,
      handoff_id: 'handoff_12345678',
      context_ref: `acr_${'a'.repeat(43)}`,
    });

    await act(async () => {
      channel.receive(createArtifactPreviewMessage('context_request', identity, {
        viewer_id: ready.viewer_id,
        handoff_id: 'handoff_12345678',
        request_id: 'request_12345678',
      }));
      await flushPromises();
    });
    expect(channel.posted.find((message) => (
      message.type === 'context_response' && message.request_id === 'request_12345678'
    ))).toMatchObject({
      viewer_id: ready.viewer_id,
      context_ref: `acr_${'b'.repeat(43)}`,
    });

    const delivery = {
      contextRef: `acr_${'b'.repeat(43)}`,
      writebackRef: `awr_${'w'.repeat(43)}`,
      topicId: 'p2p_1_440',
      agentUid: 440,
      artifactId: 'risk-register',
      displayedVersion: 2,
      resultId: `arr_${'r'.repeat(43)}`,
      originNodeId: 'node-440',
    };
    await act(async () => {
      mocks.wsHandler?.({ artifact_result: delivery });
      await flushPromises();
    });
    expect(mocks.requestArtifactResultApply).toHaveBeenCalledWith(
      expect.objectContaining({ artifactId: 'risk-register', agentUid: 440 }),
      delivery,
    );
    expect(mocks.wsSendArtifactResultReceipt).toHaveBeenCalledWith(expect.objectContaining({
      context_ref: `acr_${'b'.repeat(43)}`,
      result_id: `arr_${'r'.repeat(43)}`,
      receipt: expect.objectContaining({ status: 'applied' }),
    }));

    await act(async () => {
      channel.receive(createArtifactPreviewMessage('sidebar_claimed', identity, {
        viewer_id: ready.viewer_id,
        handoff_id: 'handoff_stale123',
      }));
      channel.receive(createArtifactPreviewMessage('sidebar_claimed', identity, {
        viewer_id: 'viewer_stale123',
        handoff_id: 'handoff_12345678',
      }));
      await flushPromises();
    });
    expect(container.textContent).not.toContain('应用已经回到对话侧边栏');

    await act(async () => {
      channel.receive(createArtifactPreviewMessage('sidebar_claimed', identity, {
        viewer_id: ready.viewer_id,
        handoff_id: 'handoff_12345678',
      }));
      await flushPromises();
    });
    expect(container.textContent).toContain('应用已经回到对话侧边栏');
    expect(mocks.disconnectWS).toHaveBeenCalled();
    expect(channel.posted.some((message) => message.type === 'viewer_released')).toBe(true);
  });

  it('withdraws stale ownership on disconnect and becomes ready again with a fresh snapshot', async () => {
    mocks.createArtifactContextSnapshot
      .mockResolvedValueOnce({
        contract_version: 'catsco.artifact-context-ref.v1',
        context_ref: `acr_${'a'.repeat(43)}`,
      })
      .mockResolvedValueOnce({
        contract_version: 'catsco.artifact-context-ref.v1',
        context_ref: `acr_${'c'.repeat(43)}`,
      });
    await act(async () => {
      root.render(<ArtifactFullscreenViewer location={location} />);
      await flushPromises();
    });
    await act(async () => {
      mocks.sessionReady = true;
      mocks.wsHandler?.({ ctrl: { params: { artifact_preview_session: {} } } });
      await flushPromises();
    });
    const channel = channels[0];
    expect(channel.posted.filter((message) => message.type === 'viewer_ready')).toHaveLength(1);

    await act(async () => {
      mocks.sessionReady = false;
      mocks.wsHandler?.({ _type: 'ws_close', attempt: 1, retryInMs: 1000 });
      await flushPromises();
    });
    expect(channel.posted.some((message) => (
      message.type === 'viewer_closed' && message.error === 'connection_lost'
    ))).toBe(true);
    expect(container.textContent).toContain('正在打开应用');
    expect(mocks.invalidateArtifactContextSnapshot).toHaveBeenCalledWith(
      `acr_${'a'.repeat(43)}`,
      { timeoutMs: 3000 },
    );

    await act(async () => {
      mocks.sessionReady = true;
      mocks.wsHandler?.({ ctrl: { params: { artifact_preview_session: {} } } });
      await flushPromises();
    });
    const readyMessages = channel.posted.filter((message) => message.type === 'viewer_ready');
    expect(readyMessages).toHaveLength(2);
    expect(readyMessages[1]).toMatchObject({
      handoff_id: 'handoff_12345678',
      context_ref: `acr_${'c'.repeat(43)}`,
    });
  });

  it('rebuilds its preview session and snapshot after returning from BFCache', async () => {
    mocks.createArtifactContextSnapshot
      .mockResolvedValueOnce({
        contract_version: 'catsco.artifact-context-ref.v1',
        context_ref: `acr_${'a'.repeat(43)}`,
      })
      .mockResolvedValueOnce({
        contract_version: 'catsco.artifact-context-ref.v1',
        context_ref: `acr_${'d'.repeat(43)}`,
      });
    await act(async () => {
      root.render(<ArtifactFullscreenViewer location={location} />);
      await flushPromises();
    });
    await act(async () => {
      mocks.sessionReady = true;
      mocks.wsHandler?.({ ctrl: { params: { artifact_preview_session: {} } } });
      await flushPromises();
    });
    const channel = channels[0];
    expect(channel.posted.filter((message) => message.type === 'viewer_ready')).toHaveLength(1);

    const pageHide = new Event('pagehide');
    Object.defineProperty(pageHide, 'persisted', { value: true });
    await act(async () => {
      window.dispatchEvent(pageHide);
      await flushPromises();
    });
    expect(channel.posted.some((message) => (
      message.type === 'viewer_closed' && message.error === 'bfcache'
    ))).toBe(true);
    expect(mocks.invalidateArtifactContextSnapshot).toHaveBeenCalledWith(
      `acr_${'a'.repeat(43)}`,
      { timeoutMs: 3000 },
    );

    const pageShow = new Event('pageshow');
    Object.defineProperty(pageShow, 'persisted', { value: true });
    await act(async () => {
      window.dispatchEvent(pageShow);
      await flushPromises();
    });
    expect(mocks.reconnectWS).toHaveBeenCalledWith(expect.any(Function));
    expect(channel.posted.filter((message) => message.type === 'viewer_hello')).toHaveLength(2);
    expect(channel.posted.filter((message) => message.type === 'viewer_ready')).toHaveLength(1);

    await act(async () => {
      mocks.sessionReady = true;
      mocks.wsHandler?.({ ctrl: { params: { artifact_preview_session: {} } } });
      await flushPromises();
    });
    const readyMessages = channel.posted.filter((message) => message.type === 'viewer_ready');
    expect(readyMessages).toHaveLength(2);
    expect(readyMessages[1]).toMatchObject({
      handoff_id: 'handoff_12345678',
      context_ref: `acr_${'d'.repeat(43)}`,
    });
  });
});
