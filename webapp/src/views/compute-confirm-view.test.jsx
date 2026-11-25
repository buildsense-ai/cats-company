import React, { act } from 'react';
import { createRoot } from 'react-dom/client';

vi.mock('../auth-session', () => ({
  request: vi.fn(),
}));

import { request } from '../auth-session';
import { authenticationRedirectPath } from '../utils/auth-routes';
import ComputeConfirmView from './compute-confirm-view';
import {
  COMPUTE_POLL_INTERVAL_MS,
  describeComputeTaskError,
  formatComputeCredits,
  formatComputeYuan,
  readComputeQuoteID,
  shouldPollComputeTask,
} from './compute-confirm';

const QUOTE_ID = 'hpc_0123456789abcdef0123456789abcdef';

function quoteRun(overrides = {}) {
  return {
    quote_id: QUOTE_ID,
    state: 'quoted',
    task: { nodes: 2, logical_cpus: 4, memory_mib: 8192, wall_seconds: 300 },
    sku: 'cats.c7.large',
    sku_logical_cpus: 8,
    sku_memory_mib: 16384,
    rate: { units_per_node_period: 2_000_000, period_seconds: 3600, increment_seconds: 60 },
    max_units: 45_000_000,
    max_node_seconds: 6600,
    expires_at: new Date(Date.now() + 5 * 60_000).toISOString(),
    units_per_credit: 1_000_000,
    progress: {},
    ...overrides,
  };
}

function findButton(container, label) {
  return Array.from(container.querySelectorAll('button')).find((button) => button.textContent === label);
}

describe('compute confirm helpers', () => {
  it('accepts only a server-shaped hpc quote id from the query string', () => {
    expect(readComputeQuoteID(`?quote=${QUOTE_ID}`)).toBe(QUOTE_ID);
    expect(readComputeQuoteID(`?quote=${QUOTE_ID}&theme_preview=dark`)).toBe(QUOTE_ID);
    expect(readComputeQuoteID('?quote=hpc_short')).toBe('');
    expect(readComputeQuoteID('?quote=../etc/passwd')).toBe('');
    expect(readComputeQuoteID('')).toBe('');
  });

  it('converts reserved units into credits and yuan at 100 credits per yuan', () => {
    expect(formatComputeCredits(45_000_000, 1_000_000)).toBe('45');
    expect(formatComputeYuan(45_000_000, 1_000_000)).toBe('0.45');
    // A cap rounds up to the next credit so the shown limit is never lower than
    // the amount the server reserved.
    expect(formatComputeCredits(1_500_000, 1_000_000)).toBe('2');
    // Without the server scale no amount may be invented.
    expect(formatComputeCredits(45_000_000, 0)).toBe('');
    expect(formatComputeYuan(45_000_000, undefined)).toBe('');
  });

  it('polls only queued and active tasks', () => {
    expect(shouldPollComputeTask({ state: 'queued' })).toBe(true);
    expect(shouldPollComputeTask({ state: 'active' })).toBe(true);
    expect(shouldPollComputeTask({ state: 'quoted' })).toBe(false);
    expect(shouldPollComputeTask({ state: 'completed' })).toBe(false);
    expect(shouldPollComputeTask({ state: 'failed' })).toBe(false);
    expect(shouldPollComputeTask(null)).toBe(false);
  });

  it('maps server error codes onto account-scoped guidance', () => {
    expect(describeComputeTaskError({ status: 409, data: { error: 'compute_quote_expired' } }))
      .toContain('报价已过期');
    expect(describeComputeTaskError({ status: 409, data: { error: 'compute_quote_mismatch' } }))
      .toContain('不要重复确认');
    expect(describeComputeTaskError({ status: 402 })).toContain('可用积分不足');
    expect(describeComputeTaskError({ status: 404 })).toContain('不属于当前登录账号');
    expect(describeComputeTaskError({ status: 401 })).toContain('重新登录');
  });
});

describe('compute confirm route protection', () => {
  it('keeps the whole quote url as the login return path', () => {
    const next = `/compute/confirm?quote=${QUOTE_ID}`;
    expect(authenticationRedirectPath({
      authenticated: false,
      location: { pathname: '/compute/confirm', search: `?quote=${QUOTE_ID}` },
    })).toBe(`/login?next=${encodeURIComponent(next)}`);
    expect(authenticationRedirectPath({
      authenticated: true,
      location: { pathname: '/compute/confirm', search: `?quote=${QUOTE_ID}` },
    })).toBe('');
  });
});

describe('ComputeConfirmView', () => {
  let container;
  let root;

  beforeEach(() => {
    global.IS_REACT_ACT_ENVIRONMENT = true;
    request.mockReset();
    request.mockResolvedValue(quoteRun());
    container = document.createElement('div');
    document.body.appendChild(container);
    root = createRoot(container);
  });

  afterEach(async () => {
    await act(async () => root.unmount());
    container.remove();
    vi.useRealTimers();
  });

  const render = async (search = `?quote=${QUOTE_ID}`) => {
    await act(async () => {
      root.render(<ComputeConfirmView search={search} />);
      await Promise.resolve();
      await Promise.resolve();
    });
  };

  it('reads only this account quote and never posts on its own', async () => {
    await render();

    expect(request).toHaveBeenCalledTimes(1);
    const [method, path] = request.mock.calls[0];
    expect(method).toBe('GET');
    expect(path).toBe(`/api/compute/tasks/${QUOTE_ID}`);

    const text = container.textContent;
    expect(text).toContain('请求规格');
    expect(text).toContain('2 个节点');
    expect(text).toContain('供应商规格');
    expect(text).toContain('cats.c7.large');
    expect(text).toContain('45 积分');
    expect(text).toContain('¥0.45');
    expect(text).toContain('库存与排队 ETA');
    expect(text).toContain('未知');
    expect(text).toContain('固定短时 CPU 验收');
    expect(text).toContain('每节点每 1 小时 2 积分');
    expect(findButton(container, '我确认，提交并预占积分上限')?.disabled).toBe(false);
    expect(request.mock.calls.some(([method]) => method === 'POST')).toBe(false);
  });

  it('sends the confirmation only after the click and then shows the queued task', async () => {
    request.mockImplementation((method) => Promise.resolve(
      method === 'POST'
        ? quoteRun({ state: 'queued', confirmed_at: new Date().toISOString() })
        : quoteRun(),
    ));
    await render();

    await act(async () => {
      findButton(container, '我确认，提交并预占积分上限').click();
      await Promise.resolve();
      await Promise.resolve();
    });

    const posts = request.mock.calls.filter(([method]) => method === 'POST');
    expect(posts).toHaveLength(1);
    expect(posts[0][1]).toBe(`/api/compute/tasks/${QUOTE_ID}`);
    expect(posts[0][2]).toEqual({ max_units: 45_000_000 });
    expect(container.textContent).toContain('已确认，排队等待开通');
    expect(container.textContent).toContain('账本结算待确认');
    expect(findButton(container, '我确认，提交并预占积分上限')).toBeFalsy();
  });

  it('blocks confirmation when the quote already expired', async () => {
    request.mockResolvedValue(quoteRun({ expires_at: new Date(Date.now() - 60_000).toISOString() }));
    await render();

    const submit = findButton(container, '我确认，提交并预占积分上限');
    expect(submit?.disabled).toBe(true);
    expect(container.textContent).toContain('报价已于');
    expect(container.textContent).toContain('报价已过期，无法再确认');
  });

  it('shows reconciliation after confirmation without asking for another quote', async () => {
    request.mockResolvedValue(quoteRun({
      state: 'active',
      confirmed_at: new Date(Date.now() - 120_000).toISOString(),
      expires_at: new Date(Date.now() - 60_000).toISOString(),
      progress: { phase: 'reconcile', problem: 'expand_intent: jobUUID unconfirmed' },
    }));
    await render();

    expect(container.textContent).toContain('需核对云端结果，任务暂缓');
    expect(container.querySelector('[role="status"]')?.textContent).toContain('自动刷新');
    expect(container.textContent).toContain('账本结算待确认');
    expect(container.textContent).not.toContain('作业运行中');
    expect(container.textContent).not.toContain('重新获取报价');
    expect(findButton(container, '我确认，提交并预占积分上限')).toBeFalsy();
  });

  it('shows the account-scoped expiry error returned by the API', async () => {
    request.mockRejectedValue(Object.assign(new Error('compute quote expired'), {
      status: 409,
      data: { error: 'compute_quote_expired' },
    }));
    await render();

    expect(container.querySelector('[role="alert"]')?.textContent).toContain('报价已过期');
    expect(findButton(container, '我确认，提交并预占积分上限')).toBeFalsy();
  });

  it('rejects a link without a valid quote id before calling the API', async () => {
    await render('?quote=hpc_bogus');

    expect(request).not.toHaveBeenCalled();
    expect(container.querySelector('[role="alert"]')?.textContent).toContain('缺少有效的报价编号');
  });

  it('polls a queued task until it settles and shows the ledger terminal state', async () => {
    vi.useFakeTimers();
    request.mockResolvedValueOnce(quoteRun({ state: 'active', progress: { slurm_job_id: '42', slurm_state: 'RUNNING' } }));
    request.mockResolvedValue(quoteRun({
      state: 'completed',
      charged_units: 3_000_000,
      progress: {
        slurm_job_id: '42',
        slurm_state: 'COMPLETED',
        shrink_operation: 'op-shrink-1',
        released_at: new Date().toISOString(),
      },
    }));

    await act(async () => {
      root.render(<ComputeConfirmView search={`?quote=${QUOTE_ID}`} />);
      await Promise.resolve();
      await Promise.resolve();
    });
    expect(container.textContent).toContain('已开通，作业运行中');
    expect(container.textContent).toContain('42');

    await act(async () => {
      vi.advanceTimersByTime(COMPUTE_POLL_INTERVAL_MS);
      await Promise.resolve();
      await Promise.resolve();
    });

    expect(request.mock.calls.filter(([method]) => method === 'GET')).toHaveLength(2);
    const text = container.textContent;
    expect(text).toContain('已完成');
    expect(text).toContain('回收操作（缩容）');
    expect(text).toContain('op-shrink-1');
    expect(text).toContain('账本实际扣费 3 积分（约 ¥0.03）');
  });
});
