import React, { act } from 'react';
import { createRoot } from 'react-dom/client';
import { Simulate } from 'react-dom/test-utils';

import CloudWorkerPanel from './cloud-worker-panel';

describe('CloudWorkerPanel', () => {
  let container;
  let root;

  const quota = { enabled: true, total: 3, used: 1, remaining: 2 };

  const worker = (overrides = {}) => ({
    id: 91,
    uid: 91,
    tenant_name: 'tenant-a',
    display_name: '云端审查助手',
    username: 'bot-cloud-1',
    cloud_status: 'running',
    app_version: '1.4.9',
    cloud_version: '1.4.8',
    cloud_image_id: '79f5b7f4-c06e-4f97-90fa-d69566f23d63',
    ...overrides,
  });

  const renderPanel = async (props = {}) => {
    await act(async () => {
      root.render(React.createElement(CloudWorkerPanel, {
        quota,
        quotaError: false,
        workers: [],
        images: [],
        actioning: null,
        onCreate: vi.fn(),
        onUpdate: vi.fn(),
        onRollback: vi.fn(),
        onReset: vi.fn(),
        onDelete: vi.fn(),
        onSwitchMode: vi.fn(),
        ...props,
      }));
      await Promise.resolve();
    });
  };

  beforeEach(() => {
    global.IS_REACT_ACT_ENVIRONMENT = true;
    container = document.createElement('div');
    document.body.appendChild(container);
    root = createRoot(container);
  });

  afterEach(async () => {
    await act(async () => {
      root.unmount();
    });
    container.remove();
  });

  test('shows quota usage and remaining capacity', async () => {
    await renderPanel();
    expect(container.textContent).toContain('云托管配额');
    expect(container.textContent).toContain('1/3 已使用');
    expect(container.textContent).toContain('还可创建 2 个云端虚拟员工');
    const bar = container.querySelector('.cc-cloud-quota-bar i');
    expect(bar).toBeTruthy();
    expect(bar.style.width).toBe('33%');
  });

  test('shows quota fetch error state', async () => {
    await renderPanel({ quotaError: true });
    expect(container.textContent).toContain('云端状态查询失败，请稍后重试');
  });

  test('shows disabled state when cloud hosting is not enabled', async () => {
    await renderPanel({ quota: { enabled: false, total: 0, used: 0, remaining: 0 } });
    expect(container.textContent).toContain('云端部署当前未开放，请联系管理员开通');
  });

  test('blocks creation when quota is exhausted', async () => {
    await renderPanel({ quota: { enabled: true, total: 1, used: 1, remaining: 0 } });
    expect(container.querySelector('.cc-cloud-create-card input')).toBeNull();
    expect(container.textContent).toContain('配额已用完或未开放，暂时无法继续创建。');
  });

  test('creates a cloud worker with the entered name', async () => {
    const onCreate = vi.fn().mockResolvedValue(undefined);
    await renderPanel({ onCreate });

    const input = container.querySelector('.cc-cloud-create-card input');
    await act(async () => {
      Simulate.change(input, { target: { value: '云端审查助手' } });
    });
    const button = Array.from(container.querySelectorAll('button'))
      .find((el) => el.textContent.includes('创建云托管员工'));
    expect(button.disabled).toBe(false);

    await act(async () => {
      Simulate.click(button);
    });
    expect(onCreate).toHaveBeenCalledTimes(1);
    expect(onCreate).toHaveBeenCalledWith('云端审查助手');
    // input cleared after success
    expect(input.value).toBe('');
  });

  test('keeps button disabled while creating', async () => {
    let resolveCreate;
    const onCreate = vi.fn(() => new Promise((resolve) => { resolveCreate = resolve; }));
    await renderPanel({ onCreate });

    const input = container.querySelector('.cc-cloud-create-card input');
    await act(async () => {
      Simulate.change(input, { target: { value: '云端审查助手' } });
    });
    const button = Array.from(container.querySelectorAll('button'))
      .find((el) => el.textContent.includes('创建云托管员工'));
    await act(async () => {
      Simulate.click(button);
    });
    expect(button.disabled).toBe(true);
    expect(button.textContent).toContain('正在供给云端实例...');

    await act(async () => {
      resolveCreate();
      await Promise.resolve();
    });
    expect(button.disabled).toBe(true); // cleared name -> disabled again
    expect(input.value).toBe('');
  });

  test('shows empty state when no cloud workers exist', async () => {
    await renderPanel();
    expect(container.textContent).toContain('还没有云托管员工');
    expect(container.textContent).toContain('0 个');
  });

  test('renders cloud workers with version and status', async () => {
    await renderPanel({
      workers: [worker()],
    });
    expect(container.textContent).toContain('云端审查助手');
    expect(container.textContent).toContain('@bot-cloud-1');
    expect(container.textContent).toContain('运行中');
    expect(container.textContent).toContain('应用 1.4.9');
    expect(container.textContent).toContain('基础镜像 1.4.8');
    expect(container.textContent).toContain('镜像 79f5b7f4');
    expect(container.textContent).toContain('1 个');
  });

  test('maps unknown status to fallback label', async () => {
    await renderPanel({
      workers: [worker({ cloud_status: 'weird_state' })],
    });
    expect(container.textContent).toContain('状态未知');
  });

  test('renders an unavailable probe as a settled state instead of loading forever', async () => {
    await renderPanel({
      workers: [worker({
        cloud_status: 'unavailable',
        app_version: '',
        cloud_version: '',
        cloud_image_id: '',
      })],
    });
    expect(container.textContent).toContain('状态暂不可用');
    expect(container.textContent).toContain('暂未读取到版本信息');
    expect(container.textContent).not.toContain('同步中');
  });

  test('labels creating / stopped / missing cloud states distinctly', async () => {
    await renderPanel({
      workers: [
        worker({ cloud_status: 'creating', tenant_name: 'bot-t1' }),
        worker({ cloud_status: 'stopped', tenant_name: 'bot-t2' }),
        worker({ cloud_status: 'missing', tenant_name: 'bot-t3' }),
      ],
    });
    const text = container.textContent;
    expect(text).toContain('实例创建中');
    expect(text).toContain('已停止');
    expect(text).toContain('实例不存在');
  });

  test('calls update/rollback/reset/delete callbacks from worker actions', async () => {
    const onUpdate = vi.fn();
    const onRollback = vi.fn();
    const onReset = vi.fn();
    const onDelete = vi.fn();
    const images = [{ version: '1.4.8' }, { version: '1.4.7' }];
    await renderPanel({
      workers: [worker()],
      images,
      onUpdate,
      onRollback,
      onReset,
      onDelete,
    });

    const buttons = Array.from(container.querySelectorAll('.cc-cloud-worker-actions button'));
    const updateBtn = buttons.find((el) => el.textContent.includes('更新'));
    const rollbackBtn = buttons.find((el) => el.textContent.includes('回滚'));
    const resetBtn = buttons.find((el) => el.textContent.includes('重置'));
    const deleteBtn = buttons[buttons.length - 1];

    await act(async () => {
      Simulate.click(updateBtn);
    });
    expect(onUpdate).toHaveBeenCalledWith(
      expect.objectContaining({ tenant_name: 'tenant-a' }),
      '',
    );

    // rollback with no explicit version passes '' (latest) + fromPanel flag
    await act(async () => {
      Simulate.click(rollbackBtn);
    });
    expect(onRollback).toHaveBeenCalledTimes(1);
    expect(onRollback).toHaveBeenCalledWith(
      expect.objectContaining({ tenant_name: 'tenant-a' }),
      '',
      { fromPanel: true },
    );

    // reset opens the captcha confirmation; verify it before onReset fires
    await act(async () => {
      Simulate.click(resetBtn);
    });
    const code = container.querySelector('.cc-cloud-reset-confirm-code b').textContent;
    const captchaInput = container.querySelector('.cc-cloud-reset-confirm-input input');
    await act(async () => {
      Simulate.change(captchaInput, { target: { value: code } });
    });
    const confirmBtn = Array.from(container.querySelectorAll('.cc-cloud-reset-confirm-input button'))
      .find((el) => el.textContent.includes('确认重置'));
    await act(async () => {
      Simulate.click(confirmBtn);
    });
    expect(onReset).toHaveBeenCalledTimes(1);
    expect(onReset).toHaveBeenCalledWith(
      expect.objectContaining({ tenant_name: 'tenant-a' }),
      '',
      { verified: true },
    );

    // delete fires directly
    await act(async () => {
      Simulate.click(deleteBtn);
    });
    expect(onDelete).toHaveBeenCalledTimes(1);
    expect(onDelete).toHaveBeenCalledWith(expect.objectContaining({ tenant_name: 'tenant-a' }));
  });

  test('reset requires the displayed captcha before calling onReset', async () => {
    const onReset = vi.fn();
    const images = [{ version: '1.4.8' }];
    await renderPanel({
      workers: [worker()],
      images,
      onReset,
    });

    const resetBtn = Array.from(container.querySelectorAll('.cc-cloud-worker-actions button'))
      .find((el) => el.textContent.includes('重置'));
    await act(async () => {
      Simulate.click(resetBtn);
    });

    // 验证码确认区明确标注被重置的机器人
    expect(container.querySelector('.cc-cloud-reset-confirm-title').textContent)
      .toContain('重置「云端审查助手」');

    const code = container.querySelector('.cc-cloud-reset-confirm-code b').textContent;
    const captchaInput = container.querySelector('.cc-cloud-reset-confirm-input input');
    const confirmBtn = Array.from(container.querySelectorAll('.cc-cloud-reset-confirm-input button'))
      .find((el) => el.textContent.includes('确认重置'));

    // wrong code -> no call + inline error
    await act(async () => {
      Simulate.change(captchaInput, { target: { value: '0000' } });
    });
    await act(async () => {
      Simulate.click(confirmBtn);
    });
    expect(onReset).not.toHaveBeenCalled();
    expect(container.textContent).toContain('验证码不正确，请重新输入');

    // correct code -> onReset fires with verified flag
    await act(async () => {
      Simulate.change(captchaInput, { target: { value: code } });
    });
    await act(async () => {
      Simulate.click(confirmBtn);
    });
    expect(onReset).toHaveBeenCalledTimes(1);
    expect(onReset).toHaveBeenCalledWith(
      expect.objectContaining({ tenant_name: 'tenant-a' }),
      '',
      { verified: true },
    );
    // confirmation panel closes after a successful reset
    expect(container.querySelector('.cc-cloud-reset-confirm')).toBeNull();
  });

  test('rollback passes a user-selected version from the dropdown', async () => {
    const onRollback = vi.fn();
    const images = [{ version: '1.4.8' }, { version: '1.4.7' }];
    await renderPanel({
      workers: [worker()],
      images,
      onRollback,
    });

    const select = container.querySelector('.cc-cloud-version-select');
    expect(select.value).toBe(''); // defaults to '' (latest), not the first image
    expect(Array.from(select.options).map((o) => o.value)).toEqual(['', '1.4.8', '1.4.7']);

    await act(async () => {
      Simulate.change(select, { target: { value: '1.4.7' } });
    });
    const rollbackBtn = Array.from(container.querySelectorAll('.cc-cloud-worker-actions button'))
      .find((el) => el.textContent.includes('回滚'));
    await act(async () => {
      Simulate.click(rollbackBtn);
    });
    expect(onRollback).toHaveBeenCalledWith(
      expect.objectContaining({ tenant_name: 'tenant-a' }),
      '1.4.7',
      { fromPanel: true },
    );
  });

  test('latest version passes an empty version even when images are unordered', async () => {
    // 无序镜像列表：选择「最新版本」必须传 ''，而不是镜像列表第一项
    const onRollback = vi.fn();
    const images = [{ version: '1.4.7' }, { version: '1.4.8' }, { version: '1.4.5' }];
    await renderPanel({
      workers: [worker()],
      images,
      onRollback,
    });

    const select = container.querySelector('.cc-cloud-version-select');
    expect(select.value).toBe('');
    await act(async () => {
      Simulate.change(select, { target: { value: '' } }); // 明确选「最新版本」
    });
    const rollbackBtn = Array.from(container.querySelectorAll('.cc-cloud-worker-actions button'))
      .find((el) => el.textContent.includes('回滚'));
    await act(async () => {
      Simulate.click(rollbackBtn);
    });
    expect(onRollback).toHaveBeenCalledWith(
      expect.objectContaining({ tenant_name: 'tenant-a' }),
      '',
      { fromPanel: true },
    );
  });

  test('rollback is disabled when no image versions are available', async () => {
    await renderPanel({
      workers: [worker()],
      images: [],
    });
    const rollbackBtn = Array.from(container.querySelectorAll('.cc-cloud-worker-actions button'))
      .find((el) => el.textContent.includes('回滚'));
    expect(rollbackBtn.disabled).toBe(true);
    const select = container.querySelector('.cc-cloud-version-select');
    expect(select.disabled).toBe(true);
  });

  test('disables only cloud actions explicitly reported as unavailable', async () => {
    await renderPanel({
      workers: [worker()],
      images: [{ version: '1.4.9' }],
      actions: {
        create: false,
        update: false,
        rollback: true,
        reset: false,
        delete: false,
      },
    });

    expect(container.textContent).toContain('云端创建服务尚未配置');
    expect(container.textContent).toContain('部分云端管理功能暂不可用');
    const buttons = Array.from(container.querySelectorAll('.cc-cloud-worker-actions button'));
    const updateBtn = buttons.find((el) => el.textContent.includes('更新'));
    const rollbackBtn = buttons.find((el) => el.textContent.includes('回滚'));
    const resetBtn = buttons.find((el) => el.textContent.includes('重置'));
    const deleteBtn = buttons[buttons.length - 1];
    expect(updateBtn.disabled).toBe(true);
    expect(updateBtn.title).toContain('尚未配置');
    expect(rollbackBtn.disabled).toBe(false);
    expect(resetBtn.disabled).toBe(true);
    expect(deleteBtn.disabled).toBe(true);
    expect(container.querySelector('.cc-cloud-version-select').disabled).toBe(false);
  });

  test('disables worker actions while the worker is being acted on', async () => {
    await renderPanel({
      workers: [worker()],
      images: [{ version: '1.4.8' }],
      actioning: 'tenant-a',
    });
    const actionButtons = Array.from(container.querySelectorAll('.cc-cloud-worker-actions button'));
    expect(actionButtons.length).toBeGreaterThan(0);
    actionButtons.forEach((btn) => expect(btn.disabled).toBe(true));
  });

  test('shows the exact wait state and blocks actions on every worker', async () => {
    await renderPanel({
      workers: [worker(), worker({ tenant_name: 'tenant-b', id: 92, uid: 92 })],
      images: [{ version: '1.4.8' }],
      actioning: { name: 'tenant-a', action: 'update' },
    });

    const status = container.querySelector('.cc-cloud-operation-status');
    expect(status).toBeTruthy();
    expect(status.textContent).toContain('正在更新应用');
    expect(container.textContent).toContain('更新中...');
    const actionButtons = Array.from(container.querySelectorAll('.cc-cloud-worker-actions button'));
    actionButtons.forEach((button) => expect(button.disabled).toBe(true));
    const selectors = Array.from(container.querySelectorAll('.cc-cloud-version-select'));
    selectors.forEach((select) => expect(select.disabled).toBe(true));
  });

  test('shows the categorized failure message inline in the create card', async () => {
    // 面板只显示按错误码分类后的提示（不显示后端具体技术原因）
    const onCreate = vi.fn().mockRejectedValue(new Error('云端资源供给失败，请稍后重试或联系管理员'));
    await renderPanel({ onCreate });

    const input = container.querySelector('.cc-cloud-create-card input');
    await act(async () => {
      Simulate.change(input, { target: { value: '云端审查助手' } });
    });
    const button = Array.from(container.querySelectorAll('button'))
      .find((el) => el.textContent.includes('创建云托管员工'));
    await act(async () => {
      Simulate.click(button);
    });
    expect(container.querySelector('.cc-cloud-create-error')).toBeTruthy();
    expect(container.querySelector('.cc-cloud-create-error').textContent).toContain('云端资源供给失败');
    expect(container.querySelector('.cc-cloud-create-error').textContent).not.toContain('Ecs.Order.ProcFailed');
    // 输入保留，便于用户修改后重试
    expect(input.value).toBe('云端审查助手');
  });

  test('hides the hosting switch when showHostingSwitch is false', async () => {
    await renderPanel({ showHostingSwitch: false });
    expect(container.querySelector('.cc-agent-hosting')).toBeNull();
    // 面板其余部分仍在
    expect(container.textContent).toContain('云托管配额');
    expect(container.textContent).toContain('创建云托管员工');
  });

  test('switches back to self-hosted from the panel hosting radio', async () => {
    const onSwitchMode = vi.fn();
    await renderPanel({ onSwitchMode });

    const radios = container.querySelectorAll('.cc-agent-hosting input[name="hosting"]');
    expect(radios.length).toBe(2);
    // managed radio is active
    expect(radios[1].checked).toBe(true);
    // clicking the self-hosted radio asks the modal to switch mode
    await act(async () => {
      Simulate.change(radios[0], { target: { checked: true } });
    });
    expect(onSwitchMode).toHaveBeenCalledTimes(1);
  });
});
