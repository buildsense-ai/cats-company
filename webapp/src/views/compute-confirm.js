// Pure helpers for the /compute/confirm page. The page itself is a thin
// rendering layer over these functions so the quoting, polling and
// account-scoped wording rules stay testable without mounting React.

export const COMPUTE_CONFIRM_PATH = '/compute/confirm';
export const COMPUTE_TASK_ROUTE = '/api/compute/tasks/';
// The server prices one credit as 1 fen, so a yuan is 100 credits. Never derive
// money from a hard-coded unit scale: units_per_credit comes from the response.
export const COMPUTE_CREDITS_PER_YUAN = 100;
// queued/active work is polled; a finished task must stop polling so a stale tab
// cannot keep hitting the API after settlement.
export const COMPUTE_POLL_INTERVAL_MS = 6000;
export const COMPUTE_TERMINAL_STATES = Object.freeze(['completed', 'failed']);
export const COMPUTE_ID_PATTERN = /^hpc_[0-9a-f]{32}$/;

export const COMPUTE_LOCKED_SPEC_NOTICE = '本页只用于固定短时 CPU 验收作业：1 至 2 个节点，每个作业时限 60 至 300 秒的整分钟。规格由报价时确定，确认后不能再改。';
export const COMPUTE_UNKNOWN_CAPACITY_NOTICE = '实时库存和排队 ETA 未知：平台只在开通时核实容量，这里不会给出预计完成时间，也没有可承诺的等待时长。';
export const COMPUTE_RESERVATION_NOTICE = '确认后按上限预占积分，作业结束后按实际资源时长结算。';
export const COMPUTE_SELF_CONFIRM_NOTICE = '只有你本人点击确认按钮才会提交这次任务；页面加载和轮询都不会自动确认。';

const STATE_LABELS = Object.freeze({
  quoted: '待你确认',
  queued: '已确认，排队等待开通',
  active: '任务处理中',
  completed: '已完成',
  failed: '未完成',
});

const ERROR_MESSAGES = Object.freeze({
  compute_task_not_found: '报价不存在，或不属于当前登录账号。请回到对话里重新获取报价链接。',
  compute_quote_expired: '报价已过期。请回到对话里重新获取报价。',
  compute_quote_mismatch: '报价状态已变化（可能已经确认过）。请刷新页面查看最新状态，不要重复确认。',
  compute_credit_insufficient: '可用积分不足，无法预占本次上限。请到「套餐与权益」充值后重新获取报价。',
  compute_task_unavailable: '算力服务暂时不可用，请稍后重试。',
  invalid_compute_confirmation: '报价上限读取异常，请回到对话里重新获取报价。',
});

function positiveNumber(value) {
  const amount = Number(value);
  return Number.isFinite(amount) && amount > 0 ? amount : 0;
}

export function isComputeQuoteID(value) {
  return COMPUTE_ID_PATTERN.test(String(value || '').trim());
}

// The quote id travels in the page query string, so a mangled or missing value
// must be rejected here instead of being sent to the API as a path segment.
export function readComputeQuoteID(search = '') {
  const raw = String(search || '').replace(/^\?/, '');
  if (!raw) return '';
  const quoteId = String(new URLSearchParams(raw).get('quote') || '').trim();
  return isComputeQuoteID(quoteId) ? quoteId : '';
}

export function computeTaskPath(quoteId) {
  return isComputeQuoteID(quoteId) ? `${COMPUTE_TASK_ROUTE}${quoteId}` : '';
}

// units_per_credit is the server's credit scale. Without it the page must not
// invent a money amount, so every formatter below returns '' for an unknown scale.
export function computeUnitsPerCredit(value) {
  return positiveNumber(value);
}

// Credits round up to the next fen so a cap is never shown lower than what the
// server reserved.
export function computeCreditAmount(units, unitsPerCredit) {
  const scale = computeUnitsPerCredit(unitsPerCredit);
  const amount = positiveNumber(units);
  if (!scale || !amount) return 0;
  return Math.ceil(amount / scale);
}

export function formatComputeCredits(units, unitsPerCredit) {
  const credits = computeCreditAmount(units, unitsPerCredit);
  if (!credits) return '';
  return credits.toLocaleString('zh-CN');
}

export function formatComputeYuan(units, unitsPerCredit) {
  const credits = computeCreditAmount(units, unitsPerCredit);
  if (!credits) return '';
  return (credits / COMPUTE_CREDITS_PER_YUAN).toFixed(2);
}

export function formatComputeSeconds(value) {
  const seconds = Number(value);
  if (!Number.isFinite(seconds) || seconds < 0) return '';
  const whole = Math.round(seconds);
  if (whole < 120) return `${whole} 秒`;
  const minutes = Math.floor(whole / 60);
  const remainder = whole % 60;
  return remainder ? `${whole} 秒（${minutes} 分 ${remainder} 秒）` : `${whole} 秒（${minutes} 分）`;
}

// Tariff periods are quoted in whole hours or minutes on the server; render the
// billing unit the way an operator writes it instead of raw seconds.
export function formatComputePeriod(value) {
  const seconds = Number(value);
  if (!Number.isFinite(seconds) || seconds <= 0) return '';
  const whole = Math.round(seconds);
  if (whole % 3600 === 0) return `${whole / 3600} 小时`;
  if (whole % 60 === 0) return `${whole / 60} 分钟`;
  return `${whole} 秒`;
}

export function formatComputeTime(value) {
  if (!value) return '';
  const time = new Date(value).getTime();
  if (Number.isNaN(time)) return '';
  const date = new Date(time);
  const pad = (number) => String(number).padStart(2, '0');
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())} ${pad(date.getHours())}:${pad(date.getMinutes())}`;
}

export function computeStateLabel(state, progress) {
  if (state === 'active') {
    if (progress?.phase === 'reconcile') return '需核对云端结果，任务暂缓';
    if (progress?.slurm_state === 'RUNNING') return '已开通，作业运行中';
    if (progress?.phase === 'slurm_running') return '已提交 Slurm 作业，等待结果';
  }
  return STATE_LABELS[String(state || '')] || '状态未知';
}

export function isComputeTerminal(state) {
  return COMPUTE_TERMINAL_STATES.includes(String(state || ''));
}

// Only a confirmed-but-unfinished task needs polling; quoted quotes are checked
// again by the server when the user confirms.
export function shouldPollComputeTask(run) {
  const state = String(run?.state || '');
  return state === 'queued' || state === 'active';
}

// A quote is confirmable only while it is still the quoted state and its expiry
// has not passed. The server re-checks both, so this is UX only.
export function computeQuoteExpiry(run, now = Date.now()) {
  const expiresAt = run?.expires_at;
  const time = expiresAt ? new Date(expiresAt).getTime() : NaN;
  if (Number.isNaN(time)) return { known: false, expired: false, remainingSeconds: 0, label: '' };
  const remainingSeconds = Math.max(0, Math.ceil((time - now) / 1000));
  const label = remainingSeconds > 0
    ? `报价将在 ${formatComputeTime(expiresAt)} 失效（剩余 ${Math.floor(remainingSeconds / 60)} 分 ${remainingSeconds % 60} 秒）`
    : `报价已于 ${formatComputeTime(expiresAt)} 失效，请回到对话重新获取报价`;
  return { known: true, expired: remainingSeconds === 0, remainingSeconds, label };
}

export function canConfirmComputeTask(run, now = Date.now()) {
  if (String(run?.state || '') !== 'quoted') return false;
  const expiry = computeQuoteExpiry(run, now);
  return !expiry.expired;
}

// The POST body must carry the exact quoted cap; a value the browser cannot
// represent exactly (or a missing one) has to block the confirmation instead of
// being rounded into a different reservation.
export function computeConfirmMaxUnits(run) {
  const value = Number(run?.max_units);
  return Number.isSafeInteger(value) && value > 0 ? value : 0;
}

export function describeComputeTaskError(error) {
  const status = Number(error?.status || 0);
  const detail = error?.data?.error;
  const code = typeof detail === 'string'
    ? detail
    : String(error?.code || detail?.code || '');
  if (ERROR_MESSAGES[code]) return ERROR_MESSAGES[code];
  if (status === 401) return '登录状态已失效，请重新登录后再确认这次任务。';
  if (status === 402) return ERROR_MESSAGES.compute_credit_insufficient;
  if (status === 403) return '当前账号没有开通算力任务的权限。';
  if (status === 404) return ERROR_MESSAGES.compute_task_not_found;
  if (status === 409) return ERROR_MESSAGES.compute_quote_mismatch;
  if (status === 429) return '操作过于频繁，请稍后再试。';
  if (status >= 500) return ERROR_MESSAGES.compute_task_unavailable;
  return '读取算力任务失败，请稍后重试。';
}

function joinValues(value) {
  if (Array.isArray(value)) return value.filter(Boolean).join('、');
  return value === undefined || value === null ? '' : String(value).trim();
}

// Progress rows mirror the worker-only store fields: cloud operations first,
// then the Slurm job, then the release step. Unknown or empty fields are
// skipped so an in-flight task never renders half-finished labels.
export function computeTaskProgressRows(run) {
  const progress = run?.progress || {};
  const rows = [
    ['phase', '阶段', progress.phase],
    ['queue', '队列', [
      joinValues(progress.queue_name),
      joinValues(progress.queue_id) && `ID ${joinValues(progress.queue_id)}`,
      joinValues(progress.queue_create_operation) && `创建 ${joinValues(progress.queue_create_operation)}`,
      joinValues(progress.queue_delete_operation) && `删除 ${joinValues(progress.queue_delete_operation)}`,
    ].filter(Boolean).join(' · ')],
    ['expand_operation', '开通操作（扩容）', joinValues(progress.expand_operation)],
    ['master_order_id', '云主机订单', joinValues(progress.master_order_id)],
    ['instance_ids', '云主机实例', joinValues(progress.instance_ids)],
    ['volume_ids', '云硬盘', joinValues(progress.volume_ids)],
    ['nodes', '节点', [
      joinValues(progress.node_names),
      joinValues(progress.node_ids) && `ID ${joinValues(progress.node_ids)}`,
    ].filter(Boolean).join(' · ')],
    ['provision_started_at', '开通开始', formatComputeTime(progress.provision_started_at)],
    ['slurm_job_id', 'Slurm 作业号', joinValues(progress.slurm_job_id)],
    ['slurm_state', 'Slurm 状态', joinValues(progress.slurm_state)],
    ['slurm_elapsed_seconds', 'Slurm 运行时长', progress.slurm_elapsed_seconds
      ? formatComputeSeconds(progress.slurm_elapsed_seconds) : ''],
    ['slurm_allocated_cpus', 'Slurm 分配 CPU', progress.slurm_allocated_cpus
      ? `${progress.slurm_allocated_cpus} 个逻辑 CPU` : ''],
    ['slurm_requested_memory', 'Slurm 申请内存', joinValues(progress.slurm_requested_memory)],
    ['slurm_node_list', 'Slurm 节点', joinValues(progress.slurm_node_list)],
    ['slurm_exit_code', 'Slurm 退出码', joinValues(progress.slurm_exit_code)],
    ['shrink_operation', '回收操作（缩容）', joinValues(progress.shrink_operation)],
    ['released_at', '资源回收时间', formatComputeTime(progress.released_at)],
    ['problem', '问题', joinValues(progress.problem)],
  ];
  return rows
    .map(([key, label, value]) => ({ key, label, value: String(value || '').trim() }))
    .filter((row) => row.value);
}

// Ledger wording only claims what the API returned. A finished task without
// charged_units is reported as pending settlement rather than as "free".
export function computeLedgerNotice(run) {
  const charged = run?.charged_units;
  if (charged !== undefined && charged !== null && Number.isFinite(Number(charged))) {
    const units = Math.max(0, Number(charged));
    const credits = formatComputeCredits(units, run?.units_per_credit) || '0';
    const yuan = formatComputeYuan(units, run?.units_per_credit) || '0.00';
    return {
      known: true,
      label: `账本实际扣费 ${credits} 积分（约 ¥${yuan}）`,
      detail: units > 0 ? '按实测资源时长结算，不超过本次上限。' : '本次没有产生扣费。',
    };
  }
  if (isComputeTerminal(run?.state)) {
    return { known: false, label: '账本结算待确认', detail: '任务已结束，实际扣费以账本记录为准，稍后刷新查看。' };
  }
  return { known: false, label: '账本结算待确认', detail: '任务结束后按实际资源时长结算，届时显示实际扣费。' };
}
