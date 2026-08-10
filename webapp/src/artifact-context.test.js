import { describe, expect, it, vi } from 'vitest';
import {
  ARTIFACT_CONTEXT_RESPONSE_TYPE,
  ARTIFACT_PAGE_CONTEXT_CONTRACT,
  ARTIFACT_REF_CONTRACT,
  artifactRefFromPreviewFile,
  artifactURLForVersion,
  normalizeArtifactPageContext,
  requestArtifactPageContext,
  withArtifactRef,
} from './artifact-context';

describe('artifact context message metadata', () => {
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

  it('adds a cache-busting version without changing the Artifact path', () => {
    expect(artifactURLForVersion(
      'https://agent-440.artifacts.catsco.fun:19991/artifacts/lesson-game/latest/?view=compact',
      3,
    )).toBe(
      'https://agent-440.artifacts.catsco.fun:19991/artifacts/lesson-game/latest/?view=compact&artifact_version=3',
    );
    expect(artifactURLForVersion('file:///tmp/lesson-game/index.html', 3)).toBe('');
    expect(artifactURLForVersion('https://example.test/latest/', 0)).toBe('');
  });

  it('adds the reference without changing visible message content', () => {
    expect(withArtifactRef('把标题改短一点', {
      contract_version: ARTIFACT_REF_CONTRACT,
      id: 'lesson-game',
      currently_visible: true,
    })).toEqual({
      type: 'text',
      content: '把标题改短一点',
      metadata: {
        artifact_ref: {
          contract_version: ARTIFACT_REF_CONTRACT,
          id: 'lesson-game',
          currently_visible: true,
        },
      },
    });
  });

  it('adds a bounded page observation beside the Artifact reference', () => {
    const pageContext = {
      contract_version: ARTIFACT_PAGE_CONTEXT_CONTRACT,
      observed_at: '2026-08-07T12:00:00.000Z',
      selected_text: '企业客户',
      controls: [
        { type: 'checkbox', name: 'feedback', value: 'f12', checked: true },
        { type: 'password', name: 'secret', value: 'do-not-send' },
      ],
      local_storage: { token: 'forged' },
    };
    expect(withArtifactRef('分析这些', {
      contract_version: ARTIFACT_REF_CONTRACT,
      id: 'lesson-game',
      currently_visible: true,
    }, pageContext)).toEqual({
      type: 'text',
      content: '分析这些',
      metadata: {
        artifact_ref: {
          contract_version: ARTIFACT_REF_CONTRACT,
          id: 'lesson-game',
          currently_visible: true,
        },
        artifact_page_context: {
          contract_version: ARTIFACT_PAGE_CONTEXT_CONTRACT,
          observed_at: '2026-08-07T12:00:00.000Z',
          selected_text: '企业客户',
          controls: [{ type: 'checkbox', name: 'feedback', value: 'f12', checked: true }],
        },
      },
    });
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
});
