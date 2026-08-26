import { describe, expect, it, vi } from 'vitest';
import {
  ARTIFACT_CONTEXT_REF_CONTRACT,
  ARTIFACT_CONTEXT_RESPONSE_TYPE,
  ARTIFACT_FRAME_BRIDGE_CONTRACT,
  ARTIFACT_FRAME_BRIDGE_READY_TYPE,
  ARTIFACT_FRAME_BRIDGE_REQUEST_TYPE,
  ARTIFACT_PAGE_CONTEXT_CONTRACT,
  ARTIFACT_REF_CONTRACT,
  ARTIFACT_RESULT_RECEIPT_CONTRACT,
  ARTIFACT_RESULT_RESPONSE_TYPE,
  artifactContextRefFromSnapshot,
  artifactFrameURLWithBridgeNonce,
  artifactRefFromPreviewFile,
  artifactURLForVersion,
  normalizeArtifactPageContext,
  normalizeArtifactResultDelivery,
  requestArtifactPageContext,
  requestArtifactResultApply,
  withArtifactContextRef,
} from './artifact-context';

function installFakeMessageChannel() {
  const original = globalThis.MessageChannel;
  class FakePort {
    constructor() {
      this.peer = null;
      this.onmessage = null;
      this.closed = false;
    }

    start() {}

    close() {
      this.closed = true;
    }

    postMessage(data) {
      if (this.closed || !this.peer || this.peer.closed) return;
      window.setTimeout(() => {
        if (!this.peer?.closed) this.peer.onmessage?.({ data });
      }, 0);
    }
  }
  class FakeMessageChannel {
    constructor() {
      this.port1 = new FakePort();
      this.port2 = new FakePort();
      this.port1.peer = this.port2;
      this.port2.peer = this.port1;
    }
  }
  globalThis.MessageChannel = FakeMessageChannel;
  return () => {
    if (original === undefined) delete globalThis.MessageChannel;
    else globalThis.MessageChannel = original;
  };
}

function opaqueBridgeFrameWindow(onRequest = () => null, expectedNonce = 'bridge-nonce') {
  const frameWindow = {
    postMessage(message, targetOrigin, transfer) {
      expect(targetOrigin).toBe('*');
      expect(message.type).toBe(ARTIFACT_FRAME_BRIDGE_REQUEST_TYPE);
      expect(message.contract_version).toBe(ARTIFACT_FRAME_BRIDGE_CONTRACT);
      expect(message.bridge_nonce).toBeUndefined();
      expect(message.result).toBeUndefined();
      const framePort = transfer?.[0];
      if (!framePort) return;
      framePort.postMessage({
        type: ARTIFACT_FRAME_BRIDGE_READY_TYPE,
        contract_version: ARTIFACT_FRAME_BRIDGE_CONTRACT,
        bridge_id: message.bridge_id,
        bridge_nonce: expectedNonce,
      });
      framePort.onmessage = ({ data }) => {
        const response = onRequest(data);
        if (response) framePort.postMessage(response);
      };
      framePort.start?.();
    },
  };
  return frameWindow;
}

describe('artifact context snapshot handoff', () => {
  it('builds a narrow reference from a visible cloud artifact preview', () => {
    expect(artifactRefFromPreviewFile({
      artifact_id: 'lesson-game',
      publish_version: 2,
      mime_type: 'text/html',
      url: 'https://agent-440.artifacts.catsco.fun:19991/artifacts/lesson-game/latest/',
      name: '课堂小游戏',
    })).toEqual({
      contract_version: ARTIFACT_REF_CONTRACT,
      id: 'lesson-game',
      displayed_version: 2,
      currently_visible: true,
    });
  });

  it('rejects ordinary files and invalid artifact identities', () => {
    expect(artifactRefFromPreviewFile({
      artifact_id: 'lesson-game',
      mime_type: 'application/pdf',
      url: 'https://example.test/report.pdf',
    })).toBeNull();
    expect(artifactRefFromPreviewFile({
      artifact_id: '../lesson-game',
      mime_type: 'text/html',
      url: 'https://example.test/report.html',
    })).toBeNull();
    expect(artifactRefFromPreviewFile({
      artifact_id: ' lesson-game ',
      mime_type: 'text/html',
      url: 'https://example.test/report.html',
    })).toBeNull();
    expect(artifactRefFromPreviewFile({
      artifact_id: `a${'b'.repeat(64)}`,
      mime_type: 'text/html',
      url: 'https://example.test/report.html',
    })).toBeNull();
    expect(artifactRefFromPreviewFile({
      artifact_id: `a${'b'.repeat(63)}`,
      mime_type: 'text/html',
      url: 'https://example.test/report.html',
    })?.id).toHaveLength(64);
  });

  it('only returns a focused reference for the Agent that owns the preview', () => {
    const file = {
      artifact_id: 'lesson-game',
      artifact_agent_uid: 440,
      publish_version: 2,
      mime_type: 'text/html',
      url: 'https://example.test/by-agent/440/lesson-game/latest/',
    };
    expect(artifactRefFromPreviewFile(file, 440)?.id).toBe('lesson-game');
    expect(artifactRefFromPreviewFile(file, 441)).toBeNull();
    expect(artifactRefFromPreviewFile(file, 0)).toBeNull();
    expect(artifactRefFromPreviewFile({ ...file, artifact_agent_uid: undefined }, 440)).toBeNull();
  });

  it('loads managed Artifacts from an immutable version path', () => {
    expect(artifactURLForVersion(
      'https://agent-440.artifacts.catsco.fun:19991/artifacts/lesson-game/latest/?view=compact',
      3,
    )).toBe(
      'https://agent-440.artifacts.catsco.fun:19991/artifacts/lesson-game/v3/?view=compact',
    );
    expect(artifactURLForVersion(
      'https://agent-440.artifacts.catsco.fun:19991/artifacts/lesson-game/v2/?artifact_version=2',
      3,
    )).toBe(
      'https://agent-440.artifacts.catsco.fun:19991/artifacts/lesson-game/v3/',
    );
    expect(artifactURLForVersion(
      'https://example.test/custom/latest/',
      3,
    )).toBe('https://example.test/custom/latest/?artifact_version=3');
    expect(artifactURLForVersion('file:///tmp/lesson-game/index.html', 3)).toBe('');
    expect(artifactURLForVersion('https://example.test/latest/', 0)).toBe('');
  });

  it('accepts only an opaque context_ref response with the exact contract', () => {
    const contextRef = `acr_${'x'.repeat(43)}`;
    expect(artifactContextRefFromSnapshot({
      contract_version: ARTIFACT_CONTEXT_REF_CONTRACT,
      context_ref: contextRef,
    })).toBe(contextRef);
    expect(artifactContextRefFromSnapshot({
      contract_version: 'catsco.artifact-context-ref.v0',
      context_ref: contextRef,
    })).toBe('');
    expect(artifactContextRefFromSnapshot({
      contract_version: ARTIFACT_CONTEXT_REF_CONTRACT,
      context_ref: 'lesson-game',
    })).toBe('');
  });

  it('adds only the opaque reference without changing visible message content', () => {
    const contextRef = `acr_${'x'.repeat(43)}`;
    expect(withArtifactContextRef('把标题改短一点', contextRef)).toEqual({
      type: 'text',
      content: '把标题改短一点',
      metadata: {
        artifact_context_ref: contextRef,
      },
    });
  });

  it('preserves unrelated payload metadata and rejects malformed refs', () => {
    const contextRef = `acr_${'y'.repeat(43)}`;
    expect(withArtifactContextRef({
      type: 'text',
      content: '分析这些',
      metadata: { trace: 'kept' },
    }, contextRef)).toEqual({
      type: 'text',
      content: '分析这些',
      metadata: {
        trace: 'kept',
        artifact_context_ref: contextRef,
      },
    });
    expect(withArtifactContextRef('分析这些', 'lesson-game')).toBe('分析这些');
  });

  it('keeps page observations in the snapshot contract rather than message metadata', () => {
    const pageContext = normalizeArtifactPageContext({
      contract_version: ARTIFACT_PAGE_CONTEXT_CONTRACT,
      observed_at: '2026-08-07T12:00:00.000Z',
      selected_text: '企业客户',
      controls: [
        { type: 'checkbox', name: 'feedback', value: 'f12', checked: true },
        { type: 'password', name: 'secret', value: 'do-not-send' },
      ],
      dirty: true,
      artifact_version: 7,
      semantic_context: {
        view: 'customer-comparison',
        selection: ['c12', 'c18'],
        filters: { region: 'east' },
        ignored: () => 'not serializable',
      },
      local_storage: { token: 'forged' },
    });
    expect(pageContext).toEqual({
      contract_version: ARTIFACT_PAGE_CONTEXT_CONTRACT,
      observed_at: '2026-08-07T12:00:00.000Z',
      selected_text: '企业客户',
      controls: [{ type: 'checkbox', name: 'feedback', value: 'f12', checked: true }],
      dirty: true,
      artifact_version: 7,
      semantic_context: {
        filters: { region: 'east' },
        selection: ['c12', 'c18'],
        view: 'customer-comparison',
      },
    });
  });

  it('drops invalid or oversized semantic state without losing the generic observation', () => {
    const cyclic = { view: 'feedback-list' };
    cyclic.self = cyclic;
    expect(normalizeArtifactPageContext({
      contract_version: ARTIFACT_PAGE_CONTEXT_CONTRACT,
      observed_at: '2026-08-07T12:00:00Z',
      selected_text: 'keep this',
      semantic_context: cyclic,
    })).toEqual({
      contract_version: ARTIFACT_PAGE_CONTEXT_CONTRACT,
      observed_at: '2026-08-07T12:00:00Z',
      selected_text: 'keep this',
      semantic_context: { view: 'feedback-list' },
    });

    const oversized = Object.fromEntries(Array.from({ length: 20 }, (_, index) => [
      `field_${index}`,
      'x'.repeat(1000),
    ]));
    expect(normalizeArtifactPageContext({
      contract_version: ARTIFACT_PAGE_CONTEXT_CONTRACT,
      observed_at: '2026-08-07T12:00:00Z',
      selected_text: 'keep this',
      semantic_context: oversized,
    })).toEqual({
      contract_version: ARTIFACT_PAGE_CONTEXT_CONTRACT,
      observed_at: '2026-08-07T12:00:00Z',
      selected_text: 'keep this',
    });

    const revoked = Proxy.revocable([], {});
    revoked.revoke();
    expect(normalizeArtifactPageContext({
      contract_version: ARTIFACT_PAGE_CONTEXT_CONTRACT,
      observed_at: '2026-08-07T12:00:00Z',
      selected_text: 'keep this',
      semantic_context: revoked.proxy,
    })).toEqual({
      contract_version: ARTIFACT_PAGE_CONTEXT_CONTRACT,
      observed_at: '2026-08-07T12:00:00Z',
      selected_text: 'keep this',
    });

    const DisguisedObject = class Object {};
    const classInstance = new DisguisedObject();
    classInstance.view = 'must-not-pass';
    expect(normalizeArtifactPageContext({
      contract_version: ARTIFACT_PAGE_CONTEXT_CONTRACT,
      observed_at: '2026-08-07T12:00:00Z',
      selected_text: 'keep this',
      semantic_context: classInstance,
    })).toEqual({
      contract_version: ARTIFACT_PAGE_CONTEXT_CONTRACT,
      observed_at: '2026-08-07T12:00:00Z',
      selected_text: 'keep this',
    });
  });

  it('bounds traversal work and preserves complete Unicode characters', () => {
    let branching = { leaf: true };
    for (let depth = 0; depth < 6; depth += 1) {
      branching = Array.from({ length: 50 }, () => branching);
    }
    expect(normalizeArtifactPageContext({
      contract_version: ARTIFACT_PAGE_CONTEXT_CONTRACT,
      observed_at: '2026-08-07T12:00:00Z',
      selected_text: 'keep this',
      semantic_context: branching,
    })).toEqual({
      contract_version: ARTIFACT_PAGE_CONTEXT_CONTRACT,
      observed_at: '2026-08-07T12:00:00Z',
      selected_text: 'keep this',
    });

    const result = normalizeArtifactPageContext({
      contract_version: ARTIFACT_PAGE_CONTEXT_CONTRACT,
      observed_at: '2026-08-07T12:00:00Z',
      semantic_context: { note: `${'x'.repeat(999)}😀z` },
    });
    expect(Array.from(result.semantic_context.note)).toHaveLength(1000);
    expect(result.semantic_context.note.endsWith('😀')).toBe(true);
  });

  it('drops only semantic state when the combined page context exceeds 16 KB', () => {
    const controls = Array.from({ length: 20 }, (_, index) => ({
      type: 'text',
      name: `field_${index}`,
      value: 'v'.repeat(512),
      text: 't'.repeat(128),
    }));
    const semanticContext = Object.fromEntries(
      Array.from({ length: 6 }, (_, index) => [`section_${index}`, 's'.repeat(1000)]),
    );
    const result = normalizeArtifactPageContext({
      contract_version: ARTIFACT_PAGE_CONTEXT_CONTRACT,
      observed_at: '2026-08-07T12:00:00Z',
      selected_text: 'x'.repeat(1000),
      controls,
      semantic_context: semanticContext,
    });

    expect(result.controls).toHaveLength(20);
    expect(result.selected_text).toHaveLength(1000);
    expect(result.semantic_context).toBeUndefined();
  });

  it('rejects invalid or observation-only page context envelopes', () => {
    expect(normalizeArtifactPageContext({
      contract_version: ARTIFACT_PAGE_CONTEXT_CONTRACT,
      observed_at: 'not-a-date',
      selected_text: 'x',
    })).toBeNull();
    expect(normalizeArtifactPageContext({
      contract_version: ARTIFACT_PAGE_CONTEXT_CONTRACT,
      observed_at: '2026-08-07T12:00:00Z',
      controls: [{ type: 'password', value: 'secret' }],
    })).toBeNull();
  });

  it('accepts a response only from the active iframe and expected origin', async () => {
    const origin = 'https://agent-440.artifacts.catsco.fun:19991';
    const frameWindow = {
      postMessage(message, targetOrigin) {
        expect(targetOrigin).toBe(origin);
        window.setTimeout(() => {
          const event = new Event('message');
          Object.defineProperties(event, {
            source: { value: frameWindow },
            origin: { value: origin },
            data: {
              value: {
                type: ARTIFACT_CONTEXT_RESPONSE_TYPE,
                request_id: message.request_id,
                context: {
                  contract_version: ARTIFACT_PAGE_CONTEXT_CONTRACT,
                  observed_at: '2026-08-07T12:00:00Z',
                  selected_text: '当前选区',
                  semantic_context: { view: 'feedback-list', selection: ['f12'] },
                },
              },
            },
          });
          window.dispatchEvent(event);
        }, 0);
      },
    };
    const result = await requestArtifactPageContext({
      frame: { contentWindow: frameWindow },
      artifactId: 'lesson-game',
      url: `${origin}/artifacts/lesson-game/latest/`,
    }, {
      contract_version: ARTIFACT_REF_CONTRACT,
      id: 'lesson-game',
      currently_visible: true,
    }, 50);
    expect(result?.selected_text).toBe('当前选区');
    expect(result?.semantic_context).toEqual({ selection: ['f12'], view: 'feedback-list' });
  });

  it('supports an opaque same-origin Artifact iframe for page context', async () => {
    const restore = installFakeMessageChannel();
    try {
      const frameWindow = opaqueBridgeFrameWindow((message) => ({
        type: ARTIFACT_CONTEXT_RESPONSE_TYPE,
        request_id: message.request_id,
        context: {
          contract_version: ARTIFACT_PAGE_CONTEXT_CONTRACT,
          observed_at: '2026-08-26T12:00:00Z',
          selected_text: '同源选区',
        },
      }));
      const result = await requestArtifactPageContext({
        frame: { contentWindow: frameWindow },
        artifactId: 'same-origin-game',
        url: artifactFrameURLWithBridgeNonce(
          `${window.location.origin}/artifacts/same-origin-game/latest/`,
          'bridge-nonce',
        ),
        bridge: ARTIFACT_FRAME_BRIDGE_CONTRACT,
        bridgeNonce: 'bridge-nonce',
        bridgeReady: true,
      }, {
        contract_version: ARTIFACT_REF_CONTRACT,
        id: 'same-origin-game',
        currently_visible: true,
      }, 50);

      expect(result?.selected_text).toBe('同源选区');
    } finally {
      restore();
    }
  });

  it('rejects an opaque bridge READY from a document without the bound URL nonce', async () => {
    const restore = installFakeMessageChannel();
    try {
      const frameWindow = opaqueBridgeFrameWindow((message) => ({
        type: ARTIFACT_CONTEXT_RESPONSE_TYPE,
        request_id: message.request_id,
        context: {
          contract_version: ARTIFACT_PAGE_CONTEXT_CONTRACT,
          observed_at: '2026-08-26T12:00:00Z',
          selected_text: '不应接受',
        },
      }), 'attacker-nonce');
      const result = await requestArtifactPageContext({
        frame: { contentWindow: frameWindow },
        artifactId: 'same-origin-game',
        url: artifactFrameURLWithBridgeNonce(
          `${window.location.origin}/artifacts/same-origin-game/latest/`,
          'expected-nonce',
        ),
        bridge: ARTIFACT_FRAME_BRIDGE_CONTRACT,
        bridgeNonce: 'expected-nonce',
        bridgeReady: true,
      }, {
        contract_version: ARTIFACT_REF_CONTRACT,
        id: 'same-origin-game',
        currently_visible: true,
      }, 20);

      expect(result).toBeNull();
    } finally {
      restore();
    }
  });

  it('does not send an opaque handshake until the iframe document has loaded', async () => {
    const postMessage = vi.fn();
    const result = await requestArtifactPageContext({
      frame: { contentWindow: { postMessage } },
      artifactId: 'same-origin-game',
      url: artifactFrameURLWithBridgeNonce(
        `${window.location.origin}/artifacts/same-origin-game/latest/`,
        'bridge-nonce',
      ),
      bridge: ARTIFACT_FRAME_BRIDGE_CONTRACT,
      bridgeNonce: 'bridge-nonce',
      bridgeReady: false,
    }, {
      contract_version: ARTIFACT_REF_CONTRACT,
      id: 'same-origin-game',
      currently_visible: true,
    }, 20);

    expect(result).toBeNull();
    expect(postMessage).not.toHaveBeenCalled();
  });

  it('cancels an opaque bridge request when its document binding is invalidated', async () => {
    const restore = installFakeMessageChannel();
    try {
      const controller = new AbortController();
      const frameWindow = {
        postMessage(message, targetOrigin, transfer) {
          expect(targetOrigin).toBe('*');
          const framePort = transfer?.[0];
          framePort?.postMessage({
            type: ARTIFACT_FRAME_BRIDGE_READY_TYPE,
            contract_version: ARTIFACT_FRAME_BRIDGE_CONTRACT,
            bridge_id: message.bridge_id,
            bridge_nonce: 'bridge-nonce',
          });
          framePort.onmessage = vi.fn();
          framePort.start?.();
        },
      };
      const resultPromise = requestArtifactPageContext({
        frame: { contentWindow: frameWindow },
        artifactId: 'same-origin-game',
        url: artifactFrameURLWithBridgeNonce(
          `${window.location.origin}/artifacts/same-origin-game/latest/`,
          'bridge-nonce',
        ),
        bridge: ARTIFACT_FRAME_BRIDGE_CONTRACT,
        bridgeNonce: 'bridge-nonce',
        bridgeReady: true,
        signal: controller.signal,
      }, {
        contract_version: ARTIFACT_REF_CONTRACT,
        id: 'same-origin-game',
        currently_visible: true,
      }, 100);
      controller.abort();

      expect(await resultPromise).toBeNull();
    } finally {
      restore();
    }
  });

  it('adds the bridge nonce as a fragment without changing the artifact URL path or query', () => {
    const url = artifactFrameURLWithBridgeNonce(
      `${window.location.origin}/artifacts/same-origin-game/latest/?artifact_version=1`,
      'nonce-123',
    );
    const parsed = new URL(url);
    expect(parsed.origin).toBe(window.location.origin);
    expect(parsed.pathname).toBe('/artifacts/same-origin-game/latest/');
    expect(parsed.search).toBe('?artifact_version=1');
    expect(parsed.hash).toContain('catsco_bridge_nonce=nonce-123');
  });

  it('retains an existing hash route before the reserved bridge parameter', () => {
    const url = artifactFrameURLWithBridgeNonce(
      `${window.location.origin}/artifacts/same-origin-game/latest/#route=dashboard&filter=open`,
      'nonce-123',
    );
    const parsed = new URL(url);
    expect(parsed.hash).toBe('#route=dashboard&filter=open&catsco_bridge_nonce=nonce-123');
  });

  it('does not use the opaque channel without the explicit bridge binding', async () => {
    const postMessage = vi.fn();
    const result = await requestArtifactPageContext({
      frame: { contentWindow: { postMessage } },
      artifactId: 'same-origin-game',
      url: `${window.location.origin}/artifacts/same-origin-game/latest/`,
    }, {
      contract_version: ARTIFACT_REF_CONTRACT,
      id: 'same-origin-game',
      currently_visible: true,
    }, 50);

    expect(result).toBeNull();
    expect(postMessage).not.toHaveBeenCalled();
  });

  it('falls back without blocking when the iframe does not answer', async () => {
    const result = await requestArtifactPageContext({
      frame: { contentWindow: { postMessage() {} } },
      artifactId: 'lesson-game',
      url: 'https://example.test/artifacts/lesson-game/latest/',
    }, {
      contract_version: ARTIFACT_REF_CONTRACT,
      id: 'lesson-game',
      currently_visible: true,
    }, 1);
    expect(result).toBeNull();
  });

  it('normalizes a bounded result delivery and excludes routing secrets from the iframe call', async () => {
    const delivery = normalizeArtifactResultDelivery({
      type: 'request',
      origin_node_id: 'catsco-node-1',
      context_ref: `acr_${'c'.repeat(43)}`,
      writeback_ref: `awr_${'w'.repeat(43)}`,
      topic_id: 'p2p_7_440',
      agent_uid: '440',
      artifact_id: 'risk-register',
      displayed_version: 3,
      sink_id: 'risk-items.upsert.v1',
      result_id: `arr_${'r'.repeat(43)}`,
      expected_state_revision: '42',
      payload: { items: [{ title: '延期风险' }] },
    });
    expect(delivery?.artifactId).toBe('risk-register');

    const origin = 'https://agent-440.artifacts.catsco.fun:19991';
    const frameWindow = {
      postMessage(message, targetOrigin) {
        expect(targetOrigin).toBe(origin);
        expect(message.result.writeback_ref).toBeUndefined();
        expect(message.result.context_ref).toBeUndefined();
        expect(message.result.payload.items[0].title).toBe('延期风险');
        window.setTimeout(() => {
          const event = new Event('message');
          Object.defineProperties(event, {
            source: { value: frameWindow },
            origin: { value: origin },
            data: {
              value: {
                type: ARTIFACT_RESULT_RESPONSE_TYPE,
                request_id: message.request_id,
                receipt: {
                  contract_version: ARTIFACT_RESULT_RECEIPT_CONTRACT,
                  result_id: delivery.resultId,
                  status: 'applied',
                  receipt: { created: 1, state_revision: '43' },
                },
              },
            },
          });
          window.dispatchEvent(event);
        }, 0);
      },
    };
    const receipt = await requestArtifactResultApply({
      frame: { contentWindow: frameWindow },
      artifactId: 'risk-register',
      agentUid: 440,
      url: `${origin}/artifacts/risk-register/latest/?artifact_version=3`,
    }, delivery, 50);
    expect(receipt).toEqual({
      contract_version: ARTIFACT_RESULT_RECEIPT_CONTRACT,
      result_id: delivery.resultId,
      status: 'applied',
      receipt: { created: 1, state_revision: '43' },
    });
  });

  it('supports result writeback through the bound opaque MessagePort', async () => {
    const delivery = normalizeArtifactResultDelivery({
      type: 'request',
      origin_node_id: 'catsco-node-1',
      context_ref: `acr_${'c'.repeat(43)}`,
      writeback_ref: `awr_${'w'.repeat(43)}`,
      topic_id: 'p2p_7_440',
      agent_uid: '440',
      artifact_id: 'same-origin-game',
      displayed_version: 1,
      sink_id: 'items.upsert.v1',
      result_id: `arr_${'r'.repeat(43)}`,
      payload: { items: [{ title: '同源结果' }] },
    });
    const restore = installFakeMessageChannel();
    try {
      const frameWindow = opaqueBridgeFrameWindow((message) => {
        expect(message.result.payload.items[0].title).toBe('同源结果');
        return {
          type: ARTIFACT_RESULT_RESPONSE_TYPE,
          request_id: message.request_id,
          receipt: {
            contract_version: ARTIFACT_RESULT_RECEIPT_CONTRACT,
            result_id: delivery.resultId,
            status: 'applied',
          },
        };
      });
      const receipt = await requestArtifactResultApply({
        frame: { contentWindow: frameWindow },
        artifactId: 'same-origin-game',
        agentUid: 440,
        url: artifactFrameURLWithBridgeNonce(
          `${window.location.origin}/artifacts/same-origin-game/latest/`,
          'bridge-nonce',
        ),
        bridge: ARTIFACT_FRAME_BRIDGE_CONTRACT,
        bridgeNonce: 'bridge-nonce',
        bridgeReady: true,
      }, delivery, 50);

      expect(receipt).toEqual({
        contract_version: ARTIFACT_RESULT_RECEIPT_CONTRACT,
        result_id: delivery.resultId,
        status: 'applied',
      });
    } finally {
      restore();
    }
  });

  it('returns an explicit failure when an opaque result bridge is unavailable', async () => {
    const delivery = normalizeArtifactResultDelivery({
      type: 'request',
      origin_node_id: 'catsco-node-1',
      context_ref: `acr_${'c'.repeat(43)}`,
      writeback_ref: `awr_${'w'.repeat(43)}`,
      topic_id: 'p2p_7_440',
      agent_uid: '440',
      artifact_id: 'same-origin-game',
      displayed_version: 1,
      sink_id: 'items.upsert.v1',
      result_id: `arr_${'s'.repeat(43)}`,
      payload: { items: [{ title: '不应发送' }] },
    });
    const postMessage = vi.fn();
    const receipt = await requestArtifactResultApply({
      frame: { contentWindow: { postMessage } },
      artifactId: 'same-origin-game',
      agentUid: 440,
      url: `${window.location.origin}/artifacts/same-origin-game/latest/`,
    }, delivery, 50);

    expect(receipt).toEqual({
      contract_version: ARTIFACT_RESULT_RECEIPT_CONTRACT,
      result_id: delivery.resultId,
      status: 'failed',
      code: 'opaque_frame_bridge_required',
    });
    expect(postMessage).not.toHaveBeenCalled();
  });

  it('returns an explicit failure for an old opaque page without sending the result over WindowProxy', async () => {
    const restore = installFakeMessageChannel();
    try {
      const calls = [];
      const frameWindow = {
        postMessage(message, targetOrigin, transfer) {
          calls.push({ message, targetOrigin, transfer });
          expect(targetOrigin).toBe('*');
          expect(message.type).toBe(ARTIFACT_FRAME_BRIDGE_REQUEST_TYPE);
          expect(message.result).toBeUndefined();
          // Simulate a legacy page: it ignores the handshake and never gets
          // the transferred port, so no result payload can leak to it.
        },
      };
      const delivery = normalizeArtifactResultDelivery({
        type: 'request',
        origin_node_id: 'catsco-node-1',
        context_ref: `acr_${'c'.repeat(43)}`,
        writeback_ref: `awr_${'w'.repeat(43)}`,
        topic_id: 'p2p_7_440',
        agent_uid: '440',
        artifact_id: 'same-origin-game',
        displayed_version: 1,
        sink_id: 'items.upsert.v1',
        result_id: `arr_${'o'.repeat(43)}`,
        payload: { items: [{ title: '不应发送' }] },
      });
      const receipt = await requestArtifactResultApply({
        frame: { contentWindow: frameWindow },
        artifactId: 'same-origin-game',
        agentUid: 440,
        url: artifactFrameURLWithBridgeNonce(
          `${window.location.origin}/artifacts/same-origin-game/latest/`,
          'bridge-nonce',
        ),
        bridge: ARTIFACT_FRAME_BRIDGE_CONTRACT,
        bridgeNonce: 'bridge-nonce',
        bridgeReady: true,
      }, delivery, 5);

      expect(receipt).toEqual({
        contract_version: ARTIFACT_RESULT_RECEIPT_CONTRACT,
        result_id: delivery.resultId,
        status: 'failed',
        code: 'opaque_frame_bridge_required',
      });
      expect(calls).toHaveLength(1);
      expect(calls[0].message.result).toBeUndefined();
    } finally {
      restore();
    }
  });

  it('does not accept a forged opaque response sent through the navigated WindowProxy', async () => {
    const restore = installFakeMessageChannel();
    try {
      let receivedRequest = null;
      const frameWindow = opaqueBridgeFrameWindow((message) => {
        receivedRequest = message;
        return {
          type: ARTIFACT_CONTEXT_RESPONSE_TYPE,
          request_id: message.request_id,
          context: {
            contract_version: ARTIFACT_PAGE_CONTEXT_CONTRACT,
            observed_at: '2026-08-26T12:00:00Z',
            selected_text: '伪造内容',
          },
        };
      }, 'attacker-nonce');
      const result = await requestArtifactPageContext({
        frame: { contentWindow: frameWindow },
        artifactId: 'same-origin-game',
        url: artifactFrameURLWithBridgeNonce(
          `${window.location.origin}/artifacts/same-origin-game/latest/`,
          'expected-nonce',
        ),
        bridge: ARTIFACT_FRAME_BRIDGE_CONTRACT,
        bridgeNonce: 'expected-nonce',
        bridgeReady: true,
      }, {
        contract_version: ARTIFACT_REF_CONTRACT,
        id: 'same-origin-game',
        currently_visible: true,
      }, 5);
      expect(result).toBeNull();
      expect(receivedRequest).toBeNull();
    } finally {
      restore();
    }
  });

  it('rejects malformed result routes and keeps a bridge timeout non-terminal', async () => {
    expect(normalizeArtifactResultDelivery({
      type: 'request',
      origin_node_id: 'node-1',
      context_ref: `acr_${'c'.repeat(43)}`,
      writeback_ref: `awr_${'w'.repeat(43)}`,
      topic_id: 'p2p_7_440',
      agent_uid: '440',
      artifact_id: 'risk-register',
      displayed_version: 3,
      sink_id: 'unversioned-sink',
      result_id: `arr_${'r'.repeat(43)}`,
      payload: {},
    })).toBeNull();

    const delivery = normalizeArtifactResultDelivery({
      type: 'request',
      origin_node_id: 'node-1',
      context_ref: `acr_${'c'.repeat(43)}`,
      writeback_ref: `awr_${'w'.repeat(43)}`,
      topic_id: 'p2p_7_440',
      agent_uid: '440',
      artifact_id: 'risk-register',
      displayed_version: 3,
      sink_id: 'risk-items.upsert.v1',
      result_id: `arr_${'r'.repeat(43)}`,
      payload: {},
    });
    const receipt = await requestArtifactResultApply({
      frame: { contentWindow: { postMessage() {} } },
      artifactId: 'risk-register',
      agentUid: 440,
      url: 'https://example.test/artifacts/risk-register/latest/',
    }, delivery, 1);
    expect(receipt).toBeNull();
  });
});
