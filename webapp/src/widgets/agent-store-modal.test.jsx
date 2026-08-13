import React, { act } from 'react';
import { createRoot } from 'react-dom/client';
import { Simulate } from 'react-dom/test-utils';

vi.mock('../api', () => ({
  api: {
    createBot: vi.fn(),
    createCloudWorker: vi.fn(),
    deleteCloudWorker: vi.fn(),
    getAgents: vi.fn(),
    getCloudWorkerMeta: vi.fn(),
    getCloudWorkers: vi.fn(),
    getBotDefinitionSkills: vi.fn(),
    getFriends: vi.fn(),
    getLocalSkills: vi.fn(),
    getMyBots: vi.fn(),
    resetCloudWorker: vi.fn(),
    rollbackCloudWorker: vi.fn(),
    getSkillHubSkill: vi.fn(),
    searchSkillHubSkills: vi.fn(),
    shareLocalSkill: vi.fn(),
    setBotSkillsVisibility: vi.fn(),
    updateBotDefinitionSkills: vi.fn(),
    uploadFile: vi.fn(),
  },
  getWebSocketURL: vi.fn(() => 'wss://app.catsco.cc/v0/channels'),
  resolveMediaURL: vi.fn((url) => url),
}));

import { api } from '../api';
import AgentStoreModal from './agent-store-modal';

describe('AgentStoreModal', () => {
  let container;
  let root;

  beforeEach(() => {
    global.IS_REACT_ACT_ENVIRONMENT = true;
    api.createBot.mockReset().mockResolvedValue({ uid: 91 });
    api.createCloudWorker.mockReset().mockResolvedValue({ uid: 92, tenant_name: 'tenant-new' });
    api.deleteCloudWorker.mockReset().mockResolvedValue({});
    api.getAgents.mockReset().mockResolvedValue({ agents: [] });
    api.getCloudWorkerMeta.mockReset().mockResolvedValue({ images: [] });
    api.getCloudWorkers.mockReset().mockResolvedValue({
      quota: { enabled: true, total: 3, used: 1, remaining: 2 },
      workers: [],
    });
    api.getBotDefinitionSkills.mockReset().mockResolvedValue({ revision: 0, skills: [] });
    api.getFriends.mockReset().mockResolvedValue({ friends: [] });
    api.getLocalSkills.mockReset().mockResolvedValue({ skills: [] });
    api.getMyBots.mockReset().mockResolvedValue({ bots: [] });
    api.resetCloudWorker.mockReset().mockResolvedValue({});
    api.rollbackCloudWorker.mockReset().mockResolvedValue({});
    api.getSkillHubSkill.mockReset().mockResolvedValue({});
    api.searchSkillHubSkills.mockReset().mockResolvedValue({ skills: [] });
    api.shareLocalSkill.mockReset();
    api.setBotSkillsVisibility.mockReset().mockResolvedValue({ skills_visibility: 'owner' });
    api.updateBotDefinitionSkills.mockReset().mockResolvedValue({ revision: 1, skills: [] });
    api.uploadFile.mockReset().mockResolvedValue({ url: '/uploads/avatar.png' });
    container = document.createElement('div');
    document.body.appendChild(container);
    root = createRoot(container);
  });

  afterEach(async () => {
    await act(async () => {
      root.unmount();
    });
    document.body.querySelectorAll('.cc-agent-skill-picker-overlay').forEach((node) => node.remove());
    document.body.querySelectorAll('.cc-agent-skill-detail-overlay').forEach((node) => node.remove());
    container.remove();
  });

  test('allows creating an assistant without a usage description', async () => {
    await act(async () => {
      root.render(React.createElement(AgentStoreModal, {
        onClose: vi.fn(),
        user: { uid: 7 },
      }));
      await Promise.resolve();
    });

    const createTab = Array.from(container.querySelectorAll('button'))
      .find((button) => button.textContent.includes('创建新助手'));

    expect(container.querySelector('.cc-agent-manager-title .lucide-bot')).not.toBeNull();
    expect(container.querySelector('.cc-agent-manager-title .lucide-zap')).toBeNull();

    await act(async () => {
      Simulate.click(createTab);
    });

    const form = container.querySelector('.cc-agent-create-form');
    const nameInput = form.querySelector('input[type="text"]');
    const description = form.querySelector('textarea');
    const submit = form.querySelector('button[type="submit"]');
    const roleSelect = form.querySelector('.v3-custom-model-select-trigger[aria-label="助手定位"]');

    expect(description.required).toBe(false);
    expect(submit.disabled).toBe(false);
    expect(roleSelect).not.toBeNull();
    expect(roleSelect.closest('.cc-agent-role-select')).not.toBeNull();
    expect(roleSelect.querySelector('.v3-custom-model-select-chevron')).not.toBeNull();

    await act(async () => {
      Simulate.click(roleSelect);
    });

    const roleOptions = Array.from(document.body.querySelectorAll('.v3-custom-model-select-option'));
    expect(roleOptions.map((option) => option.textContent)).toEqual([
      '代码审查助手',
      '问题排查助手',
      '写作助手',
      '研究助手',
      '通用助手',
    ]);

    await act(async () => {
      Simulate.click(roleOptions[2]);
    });
    expect(roleSelect.dataset.value).toBe('writing');
    expect(roleSelect.textContent).toContain('写作助手');

    await act(async () => {
      Simulate.change(nameInput, { target: { value: '测试助手' } });
    });

    await act(async () => {
      Simulate.submit(form);
      await Promise.resolve();
      await Promise.resolve();
    });

    expect(api.createBot).toHaveBeenCalledWith(
      expect.objectContaining({ display_name: '测试助手' }),
    );
  });

  test('recommends a SkillHub ability when the assistant role changes', async () => {
    api.getSkillHubSkill.mockResolvedValue({
      skill: {
        id: 'pdf-author-editor',
        name: 'PDF Author Editor',
        description: 'Detailed PDF authoring parameters.',
        author: 'CatsCo',
        latest_version: '1.0.0',
        content_hash: 'b'.repeat(64),
      },
    });
    api.searchSkillHubSkills.mockImplementation(async (query) => (
      query === 'writing'
        ? {
          skills: [{
            id: 'pdf-author-editor',
            name: 'PDF Author Editor',
            description: 'Create and edit structured PDF documents.',
            author: 'CatsCo',
            latest_version: '1.0.0',
            content_hash: 'b'.repeat(64),
          }, {
            id: 'structured-document-editor',
            name: 'Structured Document Editor',
            description: 'Edit long documents and writing projects.',
            author: 'CatsCo',
            latest_version: '1.1.0',
            content_hash: 'c'.repeat(64),
          }, {
            id: 'writing-workflow',
            name: 'Writing Workflow',
            description: 'A workflow for professional writing.',
            author: 'CatsCo',
            latest_version: '2.0.0',
            content_hash: 'd'.repeat(64),
          }],
        }
        : { skills: [] }
    ));

    await act(async () => {
      root.render(React.createElement(AgentStoreModal, {
        onClose: vi.fn(),
        user: { uid: 7 },
      }));
      await Promise.resolve();
    });

    await act(async () => {
      Simulate.click(Array.from(container.querySelectorAll('button'))
        .find((button) => button.textContent.includes('创建新助手')));
      await Promise.resolve();
    });

    const roleSelect = container.querySelector('.cc-agent-role-select .v3-custom-model-select-trigger');
    await act(async () => {
      Simulate.click(roleSelect);
    });
    const roleOptions = Array.from(document.body.querySelectorAll('.v3-custom-model-select-option'));
    await act(async () => {
      Simulate.click(roleOptions[2]);
      await Promise.resolve();
      await Promise.resolve();
    });

    const skillTabs = Array.from(container.querySelectorAll('.cc-agent-skill-tabs [role="tab"]'));
    expect(skillTabs.map((button) => button.textContent)).toEqual(['已选0', '可用3']);
    expect(skillTabs[0].getAttribute('aria-selected')).toBe('false');
    expect(skillTabs[1].getAttribute('aria-selected')).toBe('true');
    await act(async () => {
      Simulate.keyDown(skillTabs[1], { key: 'ArrowLeft' });
    });
    expect(skillTabs[0].getAttribute('aria-selected')).toBe('true');
    expect(skillTabs[1].getAttribute('aria-selected')).toBe('false');
    await act(async () => {
      Simulate.click(skillTabs[1]);
    });

    const recommendation = container.querySelector('.cc-agent-available-group .cc-agent-selected-skills');
    expect(recommendation.querySelectorAll('.cc-agent-selected-skill')).toHaveLength(3);
    expect(recommendation?.textContent).toContain('PDF Author Editor');
    expect(recommendation.querySelectorAll('.cc-agent-skill-recommended-badge')).toHaveLength(3);
    expect(recommendation.querySelector('.cc-agent-selected-skill-icon')).not.toBeNull();
    expect(recommendation.querySelector('.cc-agent-selected-skill-copy strong')?.textContent)
      .toBe('PDF Author Editor');
    expect(recommendation.querySelector('.cc-agent-selected-skill-copy small')?.textContent)
      .toBe('CatsCo · v1.0.0');
    expect(container.querySelector('.cc-agent-add-skill')?.textContent).toContain('浏览全部 Skills');
    expect(api.searchSkillHubSkills).toHaveBeenCalledWith('writing');

    const addButton = recommendation.querySelector('.cc-agent-skill-row-action');
    expect(addButton.textContent).toBe('');
    expect(addButton.querySelector('.lucide-plus')).not.toBeNull();

    await act(async () => {
      Simulate.click(recommendation.querySelector('.cc-agent-skill-detail-trigger'));
      await Promise.resolve();
      await Promise.resolve();
    });
    const detailDialog = document.body.querySelector('.cc-agent-skill-detail-dialog');
    expect(detailDialog?.textContent).toContain('Detailed PDF authoring parameters.');
    expect(detailDialog?.textContent).toContain('pdf-author-editor');
    expect(detailDialog?.textContent).toContain('v1.0.0');
    expect(detailDialog?.textContent).not.toContain('内容哈希');
    expect(detailDialog?.textContent).not.toContain('b'.repeat(64));

    await act(async () => {
      Simulate.click(detailDialog.querySelector('.cc-dialog-close'));
    });
    expect(document.body.querySelector('.cc-agent-skill-detail-dialog')).toBeNull();

    await act(async () => {
      Simulate.click(addButton);
    });
    expect(container.querySelector('.cc-agent-available-group')?.textContent)
      .toContain('PDF Author Editor');
    expect(container.querySelector('.cc-agent-available-group')?.textContent)
      .toContain('Structured Document Editor');
    expect(addButton.querySelector('.lucide-check')).not.toBeNull();
    expect(skillTabs[0].textContent).toBe('已选1');
    await act(async () => {
      Simulate.click(skillTabs[0]);
    });
    expect(container.querySelector('.cc-agent-selected-group .cc-agent-selected-skill')?.textContent)
      .toContain('PDF Author Editor');
    expect(container.querySelector('.cc-agent-selected-group .cc-agent-skill-group-empty')).toBeNull();
  });

  test('shows personal and computer Skills in the available list', async () => {
    api.getLocalSkills.mockResolvedValue({
      skills: [{
        local_skill_id: 'mine-1',
        name: 'My Private Review',
        description: 'A review Skill created by the user.',
        source: 'user',
        skill_hub: {
          reference: {
            skillId: 'priv_mine1',
            version: 'sha256-private',
            contentHash: 'e'.repeat(64),
          },
        },
      }, {
        local_skill_id: 'computer-1',
        name: 'Computer Formatter',
        description: 'Installed on this computer.',
        source: 'system',
        skill_hub: {
          reference: {
            skillId: 'tools/computer-formatter',
            version: '1.0.0',
            contentHash: 'f'.repeat(64),
          },
        },
      }, {
        local_skill_id: 'draft-1',
        name: 'Local Draft Skill',
        description: 'Not synchronized yet.',
        source: 'user',
      }],
    });

    await act(async () => {
      root.render(React.createElement(AgentStoreModal, {
        onClose: vi.fn(),
        user: { uid: 7 },
      }));
      await Promise.resolve();
    });
    await act(async () => {
      Simulate.click(Array.from(container.querySelectorAll('button'))
        .find((button) => button.textContent.includes('创建新助手')));
      await Promise.resolve();
      await Promise.resolve();
    });

    const installedGroup = container.querySelector('.cc-agent-available-group');
    expect(installedGroup.querySelectorAll('.cc-agent-selected-skill')).toHaveLength(3);
    expect(installedGroup.textContent).toContain('My Private Review');
    expect(installedGroup.textContent).toContain('我的 Skill');
    expect(installedGroup.textContent).toContain('Computer Formatter');
    expect(installedGroup.textContent).toContain('本地 Skill');

    const localDraftRow = Array.from(installedGroup.querySelectorAll('.cc-agent-selected-skill'))
      .find((row) => row.textContent.includes('Local Draft Skill'));
    expect(localDraftRow.textContent).toContain('仅本地');
    expect(localDraftRow.querySelector('.cc-agent-skill-row-action').disabled).toBe(false);
    expect(localDraftRow.querySelector('.cc-agent-skill-row-action').title).toBe('同步并添加');

    await act(async () => {
      Simulate.click(localDraftRow.querySelector('.cc-agent-skill-row-action'));
    });
    const localDetail = document.body.querySelector('.cc-agent-skill-detail-dialog');
    expect(localDetail?.textContent).toContain('仅保存在本机');
    expect(localDetail?.textContent).toContain('不包含密钥或私密数据');
    expect(Array.from(localDetail.querySelectorAll('button'))
      .some((button) => button.textContent.includes('同步并添加'))).toBe(true);
    await act(async () => {
      Simulate.click(localDetail.querySelector('.cc-dialog-close'));
    });

    const privateRow = Array.from(installedGroup.querySelectorAll('.cc-agent-selected-skill'))
      .find((row) => row.textContent.includes('My Private Review'));
    await act(async () => {
      Simulate.click(privateRow.querySelector('.cc-agent-skill-row-action'));
    });
    expect(container.querySelector('[data-skill-panel-tab="selected"]')?.textContent).toBe('已选1');
    expect(privateRow.querySelector('.lucide-check')).not.toBeNull();
  });

  test('syncs a local Skill with consent, selects it, and binds it after creation', async () => {
    const contentHash = '9'.repeat(64);
    api.getLocalSkills.mockResolvedValue({
      skills: [{
        local_skill_id: 'draft-1',
        name: 'Local Draft Skill',
        description: 'Not synchronized yet.',
        source: 'user',
      }],
    });
    api.shareLocalSkill.mockResolvedValue({
      skill: { id: 'alice/local-draft' },
      latestVersion: '1.0.0',
      contentHash,
    });
    api.getBotDefinitionSkills.mockResolvedValue({ revision: 2, skills: [] });

    await act(async () => {
      root.render(React.createElement(AgentStoreModal, {
        onClose: vi.fn(),
        user: { uid: 7 },
      }));
      await Promise.resolve();
    });
    await act(async () => {
      Simulate.click(Array.from(container.querySelectorAll('button'))
        .find((button) => button.textContent.includes('创建新助手')));
      await Promise.resolve();
      await Promise.resolve();
    });

    const localRow = Array.from(container.querySelectorAll('.cc-agent-available-group .cc-agent-selected-skill'))
      .find((row) => row.textContent.includes('Local Draft Skill'));
    await act(async () => {
      Simulate.click(localRow.querySelector('.cc-agent-skill-row-action'));
    });
    const detail = document.body.querySelector('.cc-agent-skill-detail-dialog');
    await act(async () => {
      Simulate.click(Array.from(detail.querySelectorAll('button'))
        .find((button) => button.textContent.includes('同步并添加')));
      await Promise.resolve();
      await Promise.resolve();
    });

    expect(api.shareLocalSkill).toHaveBeenCalledWith('draft-1', '', 7);
    expect(document.body.querySelector('.cc-agent-skill-detail-dialog')).toBeNull();
    expect(container.querySelector('[data-skill-panel-tab="selected"]')?.textContent).toBe('已选1');
    expect(container.querySelector('.cc-agent-skill-sync-feedback')?.textContent)
      .toContain('已同步并添加');

    const form = container.querySelector('.cc-agent-create-form');
    await act(async () => {
      Simulate.change(form.querySelector('input[type="text"]'), { target: { value: '本地能力助手' } });
    });
    await act(async () => {
      Simulate.submit(form);
      await Promise.resolve();
      await Promise.resolve();
      await Promise.resolve();
    });

    expect(api.updateBotDefinitionSkills).toHaveBeenCalledWith(91, 2, [{
      source: 'skillhub',
      skillId: 'alice/local-draft',
      version: '1.0.0',
      contentHash,
    }]);
    expect(container.textContent).toContain('创建成功');
  });

  test('keeps a local Skill unselected and offers retry when synchronization fails', async () => {
    api.getLocalSkills.mockResolvedValue({
      skills: [{
        local_skill_id: 'private-notes',
        name: 'Private Notes',
        source: 'user',
      }],
    });
    api.shareLocalSkill.mockRejectedValue(new Error('SkillHub fetch failed'));

    await act(async () => {
      root.render(React.createElement(AgentStoreModal, {
        onClose: vi.fn(),
        user: { uid: 7 },
      }));
      await Promise.resolve();
    });
    await act(async () => {
      Simulate.click(Array.from(container.querySelectorAll('button'))
        .find((button) => button.textContent.includes('创建新助手')));
      await Promise.resolve();
      await Promise.resolve();
    });

    const localRow = container.querySelector('.cc-agent-available-group .cc-agent-selected-skill');
    await act(async () => {
      Simulate.click(localRow.querySelector('.cc-agent-skill-row-action'));
    });
    const detail = document.body.querySelector('.cc-agent-skill-detail-dialog');
    const syncButton = Array.from(detail.querySelectorAll('button'))
      .find((button) => button.textContent.includes('同步并添加'));
    await act(async () => {
      Simulate.click(syncButton);
      await Promise.resolve();
      await Promise.resolve();
    });

    expect(detail.querySelector('[role="alert"]')?.textContent).toContain('SkillHub fetch failed');
    expect(detail.querySelector('[role="alert"]')?.textContent).toContain('可以稍后重试');
    expect(syncButton.disabled).toBe(false);
    expect(container.querySelector('[data-skill-panel-tab="selected"]')?.textContent).toBe('已选0');
  });

  test('does not recommend a weakly related SkillHub result', async () => {
    api.searchSkillHubSkills.mockResolvedValue({
      skills: [{
        id: 'catsco-prompt-editor',
        name: 'catsco-prompt-editor',
        description: 'Safely inspect and apply prompt changes.',
      }],
    });

    await act(async () => {
      root.render(React.createElement(AgentStoreModal, {
        onClose: vi.fn(),
        user: { uid: 7 },
      }));
      await Promise.resolve();
    });
    await act(async () => {
      Simulate.click(Array.from(container.querySelectorAll('button'))
        .find((button) => button.textContent.includes('创建新助手')));
      await Promise.resolve();
      await Promise.resolve();
    });

    expect(container.querySelector('.cc-agent-skill-recommended-badge')).toBeNull();
    expect(container.querySelector('.cc-agent-skill-group-empty')?.textContent)
      .toContain('暂无可用 Skill');
  });

  test('distinguishes an unavailable recommendation service from no matching Skill', async () => {
    api.searchSkillHubSkills.mockRejectedValue(new Error('fetch failed'));

    await act(async () => {
      root.render(React.createElement(AgentStoreModal, {
        onClose: vi.fn(),
        user: { uid: 7 },
      }));
      await Promise.resolve();
    });
    await act(async () => {
      Simulate.click(Array.from(container.querySelectorAll('button'))
        .find((button) => button.textContent.includes('创建新助手')));
      await Promise.resolve();
      await Promise.resolve();
    });

    const recommendationState = container.querySelector('.cc-agent-available-group .cc-agent-skill-recommendation-state');
    expect(recommendationState?.textContent).toContain('推荐暂时不可用');
    expect(recommendationState?.textContent).toContain('已安装的 Skill 仍可正常添加');
    expect(recommendationState?.textContent).not.toContain('暂无可用 Skill');
  });

  test('selects SkillHub abilities in a portal and binds them after creating the assistant', async () => {
    const contentHash = 'a'.repeat(64);
    api.searchSkillHubSkills.mockResolvedValue({
      skills: [{
        id: 'code-review',
        name: '代码审查',
        description: '检查代码质量与潜在问题',
        author: 'CatsCo',
        latest_version: '1.2.0',
        content_hash: contentHash,
      }],
    });
    api.getBotDefinitionSkills.mockResolvedValue({ revision: 4, skills: [] });
    api.updateBotDefinitionSkills.mockResolvedValue({ revision: 5, skills: [] });

    await act(async () => {
      root.render(React.createElement(AgentStoreModal, {
        onClose: vi.fn(),
        user: { uid: 7 },
      }));
      await Promise.resolve();
    });

    await act(async () => {
      Simulate.click(Array.from(container.querySelectorAll('button'))
        .find((button) => button.textContent.includes('创建新助手')));
    });

    const form = container.querySelector('.cc-agent-create-form');
    expect(form.querySelector('.cc-agent-skill-recommended-badge')).not.toBeNull();

    await act(async () => {
      Simulate.click(form.querySelector('.cc-agent-add-skill'));
      await Promise.resolve();
      await Promise.resolve();
    });

    const picker = document.body.querySelector('.cc-agent-skill-picker');
    expect(picker).not.toBeNull();
    expect(picker.getAttribute('aria-modal')).toBe('true');
    const skillOption = picker.querySelector('.cc-agent-skill-option');
    expect(skillOption.textContent).toContain('代码审查');

    await act(async () => Simulate.click(skillOption));
    expect(skillOption.getAttribute('aria-pressed')).toBe('true');

    await act(async () => {
      Simulate.click(Array.from(picker.querySelectorAll('button'))
        .find((button) => button.textContent.includes('完成')));
    });
    expect(document.body.querySelector('.cc-agent-skill-picker')).toBeNull();
    await act(async () => {
      Simulate.click(form.querySelector('[data-skill-panel-tab="selected"]'));
    });
    expect(form.querySelector('.cc-agent-selected-skill')?.textContent).toContain('代码审查');

    await act(async () => {
      Simulate.change(form.querySelector('input[type="text"]'), { target: { value: '审查助手' } });
    });
    await act(async () => {
      Simulate.submit(form);
      await Promise.resolve();
      await Promise.resolve();
      await Promise.resolve();
    });

    expect(api.getBotDefinitionSkills).toHaveBeenCalledWith(91);
    expect(api.updateBotDefinitionSkills).toHaveBeenCalledWith(91, 4, [{
      source: 'skillhub',
      skillId: 'code-review',
      version: '1.2.0',
      contentHash,
    }]);
    expect(container.textContent).toContain('创建成功');
  });

  test('keeps the assistant when post-create Skill binding fails', async () => {
    const contentHash = 'b'.repeat(64);
    api.searchSkillHubSkills.mockResolvedValue({
      skills: [{
        id: 'research',
        name: '研究整理',
        latest_version: '2.0.0',
        content_hash: contentHash,
      }],
    });
    api.updateBotDefinitionSkills.mockRejectedValue(new Error('版本冲突'));

    await act(async () => {
      root.render(React.createElement(AgentStoreModal, {
        onClose: vi.fn(),
        user: { uid: 7 },
      }));
      await Promise.resolve();
    });
    await act(async () => {
      Simulate.click(Array.from(container.querySelectorAll('button'))
        .find((button) => button.textContent.includes('创建新助手')));
    });
    await act(async () => {
      Simulate.click(container.querySelector('.cc-agent-add-skill'));
      await Promise.resolve();
      await Promise.resolve();
    });
    await act(async () => {
      Simulate.click(document.body.querySelector('.cc-agent-skill-option'));
    });
    await act(async () => {
      Simulate.click(Array.from(document.body.querySelectorAll('.cc-agent-skill-picker button'))
        .find((button) => button.textContent.includes('完成')));
    });
    await act(async () => {
      Simulate.change(container.querySelector('.cc-agent-create-form input[type="text"]'), {
        target: { value: '研究助手' },
      });
    });
    await act(async () => {
      Simulate.submit(container.querySelector('.cc-agent-create-form'));
      await Promise.resolve();
      await Promise.resolve();
      await Promise.resolve();
    });

    expect(api.createBot).toHaveBeenCalledTimes(1);
    expect(container.querySelector('.cc-agent-skill-binding-warning')?.textContent)
      .toContain('助手已创建，但 Skill 未全部添加：版本冲突');
  });

  test('opens a requested owned assistant directly in the management view', async () => {
    api.getMyBots.mockResolvedValue({
      bots: [{
        id: 42,
        uid: 42,
        username: 'dev-agent',
        display_name: 'Dev Agent',
        avatar_url: '/uploads/dev.png',
        relation: 'owner',
        is_owner: true,
        visibility: 'public',
      }],
    });

    await act(async () => {
      root.render(React.createElement(AgentStoreModal, {
        initialAgentId: 42,
        onClose: vi.fn(),
        user: { uid: 7 },
      }));
      await Promise.resolve();
      await Promise.resolve();
    });

    expect(container.querySelector('.cc-agent-manager-body h2')?.textContent).toBe('管理助手');
    expect(container.querySelector('.cc-agent-manager-body input[type="text"]')?.value).toBe('Dev Agent');
    expect(
      Array.from(container.querySelectorAll('.cc-agent-manager-body .oc-form-group > label'))
        .map((label) => label.textContent),
    ).toEqual(['头像', '名称']);
    expect(container.textContent).not.toContain('还没有你创建的 AI 助手');
  });

  test('uses the stable hub height for live overview data and practical usage guidance', async () => {
    api.getMyBots.mockResolvedValue({
      bots: [
        {
          id: 42,
          username: 'review-agent',
          display_name: 'Review Agent',
          relation: 'owner',
          is_owner: true,
          visibility: 'public',
        },
        {
          id: 43,
          username: 'private-agent',
          display_name: 'Private Agent',
          relation: 'owner',
          is_owner: true,
          visibility: 'private',
          tenant_name: 'catsco-cloud',
        },
      ],
    });
    api.getAgents.mockResolvedValue({
      agents: [{
        uid: 42,
        username: 'review-agent',
        display_name: 'Review Agent',
        relation: 'owner',
        is_bot: true,
        is_online: true,
      }],
    });

    await act(async () => {
      root.render(React.createElement(AgentStoreModal, {
        onClose: vi.fn(),
        user: { uid: 7 },
      }));
      await Promise.resolve();
      await Promise.resolve();
    });

    const stats = Array.from(container.querySelectorAll('.cc-agent-overview-stats > div'))
      .map((item) => item.textContent);
    expect(stats).toEqual(['2全部助手', '1当前在线', '1公开可搜索', '1自托管']);
    expect(container.querySelector('.cc-agent-hub-grid')).not.toBeNull();
    expect(container.querySelectorAll('.cc-agent-hub-grid .v3-agent-card')).toHaveLength(2);
    expect(container.querySelector('.cc-agent-card-manage')?.textContent).toBe('管理');
    expect(container.querySelector('[aria-label="删除助手 Review Agent"]')?.textContent).toBe('');
    expect(container.querySelector('.cc-agent-usage-guide')?.textContent).toContain('管理');
    expect(container.querySelector('.cc-agent-usage-guide')?.textContent).toContain('入口码');
    expect(container.querySelector('.cc-agent-usage-guide')?.textContent).toContain('移动端使用');
    expect(container.querySelector('.cc-agent-usage-guide')?.textContent)
      .not.toContain('三个入口分别负责配置、分享与移动端连接');
  });

  test('does not apply a completed avatar upload to a different managed assistant', async () => {
    let resolveUpload;
    api.uploadFile.mockReturnValue(new Promise((resolve) => {
      resolveUpload = resolve;
    }));
    api.getMyBots.mockResolvedValue({
      bots: [
        {
          id: 42,
          username: 'alpha-agent',
          display_name: 'Alpha Agent',
          relation: 'owner',
          is_owner: true,
          avatar_url: '/uploads/alpha.png',
          visibility: 'public',
        },
        {
          id: 43,
          username: 'beta-agent',
          display_name: 'Beta Agent',
          relation: 'owner',
          is_owner: true,
          avatar_url: '/uploads/beta.png',
          visibility: 'public',
        },
      ],
    });

    await act(async () => {
      root.render(React.createElement(AgentStoreModal, {
        onClose: vi.fn(),
        user: { uid: 7 },
      }));
      await Promise.resolve();
      await Promise.resolve();
    });

    const alphaCard = Array.from(container.querySelectorAll('.v3-agent-card'))
      .find((card) => card.textContent.includes('Alpha Agent'));
    await act(async () => Simulate.click(alphaCard.querySelector('.cc-agent-card-manage')));

    const fileInput = container.querySelector('input[type="file"]');
    Object.defineProperty(fileInput, 'files', {
      configurable: true,
      value: [new File(['avatar'], 'alpha.png', { type: 'image/png' })],
    });
    await act(async () => {
      Simulate.change(fileInput);
      await Promise.resolve();
    });
    expect(api.uploadFile).toHaveBeenCalledTimes(1);

    await act(async () => Simulate.click(container.querySelector('.cc-agent-manager-tabs button')));
    const betaCard = Array.from(container.querySelectorAll('.v3-agent-card'))
      .find((card) => card.textContent.includes('Beta Agent'));
    await act(async () => Simulate.click(betaCard.querySelector('.cc-agent-card-manage')));

    await act(async () => {
      resolveUpload({ url: '/uploads/old-alpha-upload.png' });
      await Promise.resolve();
      await Promise.resolve();
    });

    expect(container.querySelector('.cc-agent-manager-body input[type="text"]').value).toBe('Beta Agent');
    expect(container.querySelector('.cc-agent-manager-body .oc-avatar-img')?.getAttribute('src'))
      .toBe('/uploads/beta.png');
  });

  test('summarizes the current capability configuration and opens SkillHub', async () => {
    const onOpenSkillHub = vi.fn();
    api.getBotDefinitionSkills.mockResolvedValue({
      revision: 3,
      skills: [
        { source: 'skillhub', skillId: 'tools/review', version: '1.0.0' },
        { source: 'skillhub', skillId: 'tools/pdf', version: '2.0.0' },
      ],
    });
    api.getMyBots.mockResolvedValue({
      bots: [{
        id: 42,
        uid: 42,
        username: 'dev-agent',
        display_name: 'Dev Agent',
        relation: 'owner',
        is_owner: true,
        visibility: 'public',
      }],
    });

    await act(async () => {
      root.render(React.createElement(AgentStoreModal, {
        initialAgentId: 42,
        onOpenSkillHub,
        onClose: vi.fn(),
        user: { uid: 7 },
      }));
      await Promise.resolve();
      await Promise.resolve();
    });

    const summary = container.querySelector('.cc-agent-capability-summary');
    expect(summary?.textContent).toContain('能力配置');
    expect(summary?.textContent).toContain('已启用 2 个 Skill');
    expect(container.textContent).not.toContain('技能可见范围');

    await act(async () => {
      Simulate.click(summary.querySelector('.cc-agent-open-skillhub'));
    });
    expect(onOpenSkillHub).toHaveBeenCalledWith(42, expect.objectContaining({ display_name: 'Dev Agent' }));
  });

  test('switches to the dedicated cloud panel when managed hosting is selected', async () => {
    api.getMyBots.mockResolvedValue({
      bots: [{
        id: 92,
        uid: 92,
        tenant_name: 'tenant-a',
        username: 'bot-cloud-1',
        display_name: '云端审查助手',
        relation: 'owner',
        is_owner: true,
        visibility: 'public',
      }],
    });
    api.getCloudWorkers.mockResolvedValue({
      quota: { enabled: true, total: 3, used: 1, remaining: 2 },
      workers: [{
        tenant_name: 'tenant-a',
        status: 'running',
        version: '1.4.8',
        image_id: '79f5b7f4-c06e-4f97-90fa-d69566f23d63',
      }],
    });

    await act(async () => {
      root.render(React.createElement(AgentStoreModal, {
        onClose: vi.fn(),
        user: { uid: 7 },
      }));
      await Promise.resolve();
      await Promise.resolve();
    });

    const createTab = Array.from(container.querySelectorAll('button'))
      .find((button) => button.textContent.includes('创建新助手'));
    await act(async () => {
      Simulate.click(createTab);
      await Promise.resolve();
    });

    // Self-hosted form is shown by default.
    expect(Array.from(container.querySelectorAll('button'))
      .some((button) => button.textContent.includes('创建我的专属助手'))).toBe(true);
    expect(container.textContent).not.toContain('云托管配额');

    // Select managed hosting -> the cloud panel replaces the self-hosted form.
    const managedRadio = container.querySelectorAll('.cc-agent-hosting input[name="hosting"]')[1];
    await act(async () => {
      Simulate.change(managedRadio, { target: { checked: true } });
      await Promise.resolve();
    });

    expect(container.textContent).toContain('云托管配额');
    expect(container.textContent).toContain('1/3 已使用');
    expect(Array.from(container.querySelectorAll('button'))
      .some((button) => button.textContent.includes('创建云托管员工'))).toBe(true);
    expect(container.textContent).toContain('云端审查助手');
    expect(container.textContent).toContain('运行中');
    // Self-hosted form is gone while managed is active.
    expect(container.textContent).not.toContain('创建我的专属助手');

    // Switching back to self-hosted restores the original form.
    const selfHostedRadio = container.querySelectorAll('.cc-agent-hosting input[name="hosting"]')[0];
    await act(async () => {
      Simulate.change(selfHostedRadio, { target: { checked: true } });
      await Promise.resolve();
    });
    expect(Array.from(container.querySelectorAll('button'))
      .some((button) => button.textContent.includes('创建我的专属助手'))).toBe(true);
    expect(container.textContent).not.toContain('创建云托管员工');
  });

  test('creates a cloud worker from the managed panel', async () => {
    api.getMyBots.mockResolvedValue({ bots: [] });
    api.getCloudWorkers.mockResolvedValue({
      quota: { enabled: true, total: 3, used: 1, remaining: 2 },
      workers: [],
    });
    api.createCloudWorker.mockResolvedValue({ uid: 93, tenant_name: 'tenant-new' });

    await act(async () => {
      root.render(React.createElement(AgentStoreModal, {
        onClose: vi.fn(),
        user: { uid: 7 },
      }));
      await Promise.resolve();
      await Promise.resolve();
    });

    const createTab = Array.from(container.querySelectorAll('button'))
      .find((button) => button.textContent.includes('创建新助手'));
    await act(async () => {
      Simulate.click(createTab);
    });

    const managedRadio = container.querySelectorAll('.cc-agent-hosting input[name="hosting"]')[1];
    await act(async () => {
      Simulate.change(managedRadio, { target: { checked: true } });
      await Promise.resolve();
    });

    const input = container.querySelector('.cc-cloud-create-card input');
    await act(async () => {
      Simulate.change(input, { target: { value: '云端审查助手' } });
    });
    const createBtn = Array.from(container.querySelectorAll('button'))
      .find((button) => button.textContent.includes('创建云托管员工'));
    await act(async () => {
      Simulate.click(createBtn);
      await Promise.resolve();
      await Promise.resolve();
    });

    expect(api.createCloudWorker).toHaveBeenCalledTimes(1);
    expect(api.createCloudWorker).toHaveBeenCalledWith(expect.objectContaining({ display_name: '云端审查助手' }));
  });

  test('opens the cloud manage view from the hub and returns to the roster', async () => {
    api.getMyBots.mockResolvedValue({
      bots: [{
        id: 92,
        uid: 92,
        tenant_name: 'tenant-a',
        username: 'bot-cloud-1',
        display_name: '云端审查助手',
        relation: 'owner',
        is_owner: true,
        visibility: 'public',
      }],
    });
    api.getCloudWorkers.mockResolvedValue({
      quota: { enabled: true, total: 3, used: 1, remaining: 2 },
      workers: [{
        tenant_name: 'tenant-a',
        status: 'running',
        version: '1.4.8',
        image_id: '79f5b7f4-c06e-4f97-90fa-d69566f23d63',
      }],
    });

    await act(async () => {
      root.render(React.createElement(AgentStoreModal, {
        onClose: vi.fn(),
        user: { uid: 7 },
      }));
      await Promise.resolve();
      await Promise.resolve();
    });

    // hub 列表里有云托管管理入口（云员工独有）
    const entry = Array.from(container.querySelectorAll('button'))
      .find((b) => b.textContent.includes('云托管管理'));
    expect(entry).toBeTruthy();

    await act(async () => {
      Simulate.click(entry);
      await Promise.resolve();
    });

    // 云托管管理视图：配额 + 员工管理，无部署方式 radio
    expect(container.textContent).toContain('云托管配额');
    expect(container.textContent).toContain('云端审查助手');
    expect(container.textContent).toContain('运行中');
    expect(container.querySelector('.cc-agent-hosting')).toBeNull();
    const back = Array.from(container.querySelectorAll('button'))
      .find((b) => b.textContent.includes('返回助手列表'));
    expect(back).toBeTruthy();

    await act(async () => {
      Simulate.click(back);
      await Promise.resolve();
    });

    // 回到助手列表
    expect(Array.from(container.querySelectorAll('button'))
      .some((b) => b.textContent.includes('云托管管理'))).toBe(true);
  });

  test('maps cloud worker create errors to categorized messages', async () => {
    api.getMyBots.mockResolvedValue({ bots: [] });
    api.getCloudWorkers.mockResolvedValue({
      quota: { enabled: true, total: 3, used: 1, remaining: 2 },
      workers: [],
    });
    const err = new Error('cloud worker creation quota exhausted');
    err.data = { code: 'cloud_worker_quota_exhausted' };
    api.createCloudWorker.mockRejectedValue(err);

    await act(async () => {
      root.render(React.createElement(AgentStoreModal, {
        onClose: vi.fn(),
        user: { uid: 7 },
      }));
      await Promise.resolve();
      await Promise.resolve();
    });

    const createTab = Array.from(container.querySelectorAll('button'))
      .find((button) => button.textContent.includes('创建新助手'));
    await act(async () => {
      Simulate.click(createTab);
    });
    const managedRadio = container.querySelectorAll('.cc-agent-hosting input[name="hosting"]')[1];
    await act(async () => {
      Simulate.change(managedRadio, { target: { checked: true } });
      await Promise.resolve();
    });

    const input = container.querySelector('.cc-cloud-create-card input');
    await act(async () => {
      Simulate.change(input, { target: { value: '云端审查助手' } });
    });
    const createBtn = Array.from(container.querySelectorAll('button'))
      .find((button) => button.textContent.includes('创建云托管员工'));
    await act(async () => {
      Simulate.click(createBtn);
      await Promise.resolve();
      await Promise.resolve();
    });

    // 配额不足 → 分类提示；不暴露后端原始错误
    expect(container.querySelector('.cc-cloud-create-error')).toBeTruthy();
    expect(container.querySelector('.cc-cloud-create-error').textContent).toContain('云端虚拟员工配额已用完');
    expect(container.querySelector('.cc-cloud-create-error').textContent).not.toContain('quota exhausted');
  });

  test('keeps the SkillHub entry available when the capability count cannot load', async () => {
    api.getBotDefinitionSkills.mockRejectedValue(new Error('读取失败'));
    api.getMyBots.mockResolvedValue({
      bots: [{
        id: 42,
        uid: 42,
        username: 'dev-agent',
        display_name: 'Dev Agent',
        relation: 'owner',
        is_owner: true,
        visibility: 'public',
      }],
    });

    await act(async () => {
      root.render(React.createElement(AgentStoreModal, {
        initialAgentId: 42,
        onClose: vi.fn(),
        user: { uid: 7 },
      }));
      await Promise.resolve();
      await Promise.resolve();
    });
    const summary = container.querySelector('.cc-agent-capability-summary');
    expect(summary?.textContent).toContain('暂时无法读取能力数量');
    expect(summary?.querySelector('.cc-agent-open-skillhub')).not.toBeNull();
  });
});
