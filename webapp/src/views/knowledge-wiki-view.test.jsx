import React, { act } from 'react';
import { createRoot } from 'react-dom/client';
import { afterEach, beforeEach, describe, expect, test, vi } from 'vitest';

const wikiMocks = vi.hoisted(() => ({
  getKnowledgeWikiManifest: vi.fn(),
  getKnowledgeWikiWebSocketTicket: vi.fn(),
  connectWS: vi.fn(),
  disconnectWS: vi.fn(),
  requestSkillHubDeviceTool: vi.fn(),
}));

vi.mock('../api', async (importOriginal) => {
  const actual = await importOriginal();
  return {
    ...actual,
    api: {
      ...actual.api,
      getKnowledgeWikiManifest: wikiMocks.getKnowledgeWikiManifest,
      getKnowledgeWikiWebSocketTicket: wikiMocks.getKnowledgeWikiWebSocketTicket,
    },
    connectWS: wikiMocks.connectWS,
    disconnectWS: wikiMocks.disconnectWS,
    requestSkillHubDeviceTool: wikiMocks.requestSkillHubDeviceTool,
  };
});

import TinodeWeb from './tinode-web';
import { knowledgeWikiAgentID } from './knowledge-wiki-view';

let container;
let root;

beforeEach(() => {
  wikiMocks.getKnowledgeWikiManifest.mockReset();
  wikiMocks.getKnowledgeWikiWebSocketTicket.mockReset();
  wikiMocks.connectWS.mockReset();
  wikiMocks.disconnectWS.mockReset();
  wikiMocks.requestSkillHubDeviceTool.mockReset();
  container = document.createElement('div');
  document.body.appendChild(container);
  root = createRoot(container);
});

afterEach(async () => {
  await act(async () => root.unmount());
  container.remove();
});

function renderWikiRoute(pathname) {
  return act(async () => {
    root.render(<TinodeWeb location={{ pathname, search: '', hash: '' }} />);
  });
}

describe('knowledgeWikiAgentID', () => {
  test('decodes the uid and normalizes trailing slashes like the route gate', () => {
    expect(knowledgeWikiAgentID('/wiki/agents/982')).toBe('982');
    expect(knowledgeWikiAgentID('/wiki/agents/982/')).toBe('982');
    expect(knowledgeWikiAgentID('/wiki/agents/982//')).toBe('982');
    expect(knowledgeWikiAgentID('/wiki/agents/%E6%B5%8B%E8%AF%95')).toBe('测试');
  });

  test('returns an empty uid instead of throwing on malformed escapes', () => {
    expect(knowledgeWikiAgentID('/wiki/agents/%E0%A4')).toBe('');
    expect(knowledgeWikiAgentID('/wiki/agents')).toBe('');
    expect(knowledgeWikiAgentID('/wiki/agents/')).toBe('');
    expect(knowledgeWikiAgentID('/wiki')).toBe('');
  });
});

describe('knowledge wiki routing without a local session token', () => {
  test('renders the handoff view for a valid agent uid', async () => {
    wikiMocks.getKnowledgeWikiManifest.mockResolvedValue({
      online: true,
      owner_user_id: 406,
      target_device_id: 'device-1',
      agent_name: '测试知识库',
    });
    wikiMocks.getKnowledgeWikiWebSocketTicket.mockResolvedValue({ token: 'wiki-ticket' });
    wikiMocks.connectWS.mockImplementation((onMessage) => {
      onMessage?.({ _type: 'ws_open' });
      return true;
    });
    wikiMocks.requestSkillHubDeviceTool.mockResolvedValue({
      items: [],
      total: 3,
      review_pending: 1,
      next_offset: 0,
    });

    await renderWikiRoute('/wiki/agents/982');

    await vi.waitFor(() => {
      expect(container.textContent).toContain('测试知识库');
    });
    expect(container.querySelector('.cc-knowledge-wiki')).toBeTruthy();
    expect(wikiMocks.getKnowledgeWikiManifest).toHaveBeenCalledWith('982');
    expect(wikiMocks.requestSkillHubDeviceTool).toHaveBeenCalledWith(expect.objectContaining({
      toolName: 'knowledge.document.list',
    }));
    expect(container.textContent).toContain('3');
  });

  test('falls back to the invalid-entry state for malformed uids instead of throwing', async () => {
    await renderWikiRoute('/wiki/agents/%E0%A4');

    expect(container.querySelector('.cc-knowledge-wiki')).toBeTruthy();
    expect(container.textContent).toContain('知识库入口无效');
    expect(wikiMocks.getKnowledgeWikiManifest).not.toHaveBeenCalled();
  });

  test('maps an expired handoff session to the re-entry message', async () => {
    wikiMocks.getKnowledgeWikiManifest.mockRejectedValue(
      Object.assign(new Error('unauthorized'), { status: 401 }),
    );

    await renderWikiRoute('/wiki/agents/982');

    await vi.waitFor(() => {
      expect(container.textContent).toContain('知识库入口无效或已过期，请重新从 AI 助手管理进入。');
    });
    expect(container.textContent).not.toContain('unauthorized');
  });
});
