import React, { useCallback, useEffect, useMemo, useState } from 'react';
import { request } from '../auth-session';
import { loginPathForLocation } from '../utils/auth-routes';
import {
  COMPUTE_CONFIRM_PATH,
  COMPUTE_CREDITS_PER_YUAN,
  COMPUTE_LOCKED_SPEC_NOTICE,
  COMPUTE_POLL_INTERVAL_MS,
  COMPUTE_RESERVATION_NOTICE,
  COMPUTE_SELF_CONFIRM_NOTICE,
  COMPUTE_UNKNOWN_CAPACITY_NOTICE,
  canConfirmComputeTask,
  computeConfirmMaxUnits,
  computeLedgerNotice,
  computeQuoteExpiry,
  computeStateLabel,
  computeTaskPath,
  computeTaskProgressRows,
  describeComputeTaskError,
  formatComputeCredits,
  formatComputePeriod,
  formatComputeSeconds,
  formatComputeTime,
  formatComputeYuan,
  isComputeTerminal,
  readComputeQuoteID,
  shouldPollComputeTask,
} from './compute-confirm';
import '../css/compute-confirm.css';

const READ_TIMEOUT_MS = 20_000;
// Confirmation reserves credits, so give the reservation write a longer budget
// than a plain read but still surface a definite failure to the user.
const CONFIRM_TIMEOUT_MS = 40_000;

// Both calls stay on the account-scoped store endpoint; the quote id is the only
// identity the page ever sends, so a leaked link cannot act on another account.
export function readComputeTask(quoteId, options = {}) {
  const path = computeTaskPath(quoteId);
  if (!path) return Promise.reject(new Error('invalid compute quote id'));
  return request('GET', path, undefined, { timeoutMs: READ_TIMEOUT_MS, ...options });
}

export function confirmComputeTask(quoteId, maxUnits, options = {}) {
  const path = computeTaskPath(quoteId);
  if (!path) return Promise.reject(new Error('invalid compute quote id'));
  return request('POST', path, { max_units: maxUnits }, { timeoutMs: CONFIRM_TIMEOUT_MS, ...options });
}

function Fact({ label, value, code = false }) {
  if (!value) return null;
  return (
    <div className="compute-confirm-fact">
      <dt>{label}</dt>
      <dd className={code ? 'compute-confirm-code' : undefined}>{value}</dd>
    </div>
  );
}

function taskSpecText(run) {
  const task = run?.task || {};
  const parts = [];
  if (Number(task.nodes) > 0) parts.push(`${Number(task.nodes)} 个节点`);
  if (Number(task.logical_cpus) > 0) parts.push(`每节点 ${Number(task.logical_cpus)} 个逻辑 CPU`);
  if (Number(task.memory_mib) > 0) parts.push(`每节点 ${Number(task.memory_mib)} MiB 内存`);
  if (Number(task.wall_seconds) > 0) parts.push(`作业时限 ${formatComputeSeconds(task.wall_seconds)}`);
  return parts.join(' · ');
}

function skuSpecText(run) {
  const parts = [];
  if (run?.sku) parts.push(run.sku);
  if (Number(run?.sku_logical_cpus) > 0) parts.push(`每节点 ${Number(run.sku_logical_cpus)} 个逻辑 CPU`);
  if (Number(run?.sku_memory_mib) > 0) parts.push(`每节点 ${Number(run.sku_memory_mib)} MiB 内存`);
  return parts.join(' · ');
}

function rateFacts(run) {
  const rate = run?.rate || {};
  const credits = formatComputeCredits(rate.units_per_node_period, run?.units_per_credit);
  if (!credits || !(Number(rate.period_seconds) > 0)) return '';
  const increment = Number(rate.increment_seconds) > 0
    ? `，按 ${formatComputePeriod(rate.increment_seconds)}取整`
    : '';
  return `每节点每 ${formatComputePeriod(rate.period_seconds)} ${credits} 积分${increment}`;
}

function capFacts(run) {
  const credits = formatComputeCredits(run?.max_units, run?.units_per_credit);
  const yuan = formatComputeYuan(run?.max_units, run?.units_per_credit);
  if (!credits || !yuan) return '';
  return `${credits} 积分（约 ¥${yuan}，按 ${COMPUTE_CREDITS_PER_YUAN} 积分/元折算）`;
}

export default function ComputeConfirmView({ search = '' }) {
  const quoteId = useMemo(() => readComputeQuoteID(search), [search]);
  const [run, setRun] = useState(null);
  const [loading, setLoading] = useState(Boolean(quoteId));
  const [error, setError] = useState(null);
  const [submitting, setSubmitting] = useState(false);
  const [reloadKey, setReloadKey] = useState(0);
  const [clock, setClock] = useState(() => Date.now());

  const load = useCallback((options = {}) => {
    if (!quoteId) return () => {};
    const controller = new AbortController();
    let cancelled = false;
    setLoading(true);
    readComputeTask(quoteId, { ...options, signal: controller.signal })
      .then((next) => {
        if (cancelled) return;
        if (next?.quote_id) setRun(next);
        setError(null);
        setClock(Date.now());
      })
      .catch((reason) => {
        if (cancelled || reason?.name === 'AbortError') return;
        setError({ message: describeComputeTaskError(reason), status: Number(reason?.status || 0) });
      })
      .finally(() => {
        if (!cancelled) setLoading(false);
      });
    return () => {
      cancelled = true;
      controller.abort();
    };
  }, [quoteId]);

  useEffect(() => load(), [load, reloadKey]);

  // A queued or active task is polled until it reaches a terminal state. The
  // effect keys off the derived boolean so a data refresh does not restart the
  // interval, and a finished task stops polling on its own.
  const polling = shouldPollComputeTask(run);
  useEffect(() => {
    if (!quoteId || !polling) return undefined;
    const controller = new AbortController();
    let cancelled = false;
    const tick = () => {
      readComputeTask(quoteId, { signal: controller.signal })
        .then((next) => {
          if (cancelled) return;
          if (next?.quote_id) setRun(next);
          setError(null);
          setClock(Date.now());
        })
        .catch((reason) => {
          if (cancelled || reason?.name === 'AbortError') return;
          // Keep the last known state on screen; the next tick retries.
          setError({ message: describeComputeTaskError(reason), status: Number(reason?.status || 0) });
        });
    };
    const timer = setInterval(tick, COMPUTE_POLL_INTERVAL_MS);
    return () => {
      cancelled = true;
      clearInterval(timer);
      controller.abort();
    };
  }, [quoteId, polling]);

  // The expiry countdown only needs to tick while a confirmation is possible.
  const quoteOpen = String(run?.state || '') === 'quoted';
  useEffect(() => {
    if (!quoteOpen) return undefined;
    const timer = setInterval(() => setClock(Date.now()), 1000);
    return () => clearInterval(timer);
  }, [quoteOpen]);

  const handleConfirm = async () => {
    const maxUnits = computeConfirmMaxUnits(run);
    if (submitting || !maxUnits || !canConfirmComputeTask(run, clock)) return;
    setSubmitting(true);
    setError(null);
    try {
      const next = await confirmComputeTask(quoteId, maxUnits);
      if (next?.quote_id) {
        setRun(next);
        setClock(Date.now());
      } else {
        setError({ message: '确认已提交，暂未取到任务状态。请刷新本页查看，不要重复确认。', status: 0 });
        setReloadKey((value) => value + 1);
      }
    } catch (reason) {
      setError({ message: describeComputeTaskError(reason), status: Number(reason?.status || 0) });
      // A definite rejection means this page's copy of the quote is stale.
      setReloadKey((value) => value + 1);
    } finally {
      setSubmitting(false);
    }
  };

  const expiry = computeQuoteExpiry(run, clock);
  const confirmable = Boolean(quoteId) && canConfirmComputeTask(run, clock)
    && computeConfirmMaxUnits(run) > 0 && !submitting;
  const progressRows = run ? computeTaskProgressRows(run) : [];
  const ledger = run ? computeLedgerNotice(run) : null;
  const stateLabel = computeStateLabel(run?.state, run?.progress);
  const loginHref = loginPathForLocation({ pathname: COMPUTE_CONFIRM_PATH, search: String(search || '') });

  return (
    <main className="compute-confirm-page">
      <section className="compute-confirm-card" aria-label="算力任务确认">
        <header className="compute-confirm-heading">
          <h1>算力任务确认</h1>
          <p>
            这是当前登录账号的 Cats 算力任务报价。核对请求规格、供应商规格和积分上限后，
            由你本人点击确认才会预占积分并开通作业。
          </p>
        </header>

        {!quoteId && (
          <p role="alert" className="compute-confirm-alert">
            链接里缺少有效的报价编号（quote=hpc_…），无法确认任务。请回到对话里重新获取报价链接。
          </p>
        )}

        {quoteId && loading && !run && (
          <p role="status" className="compute-confirm-loading">正在读取报价…</p>
        )}

        {error && (
          <p role="alert" className="compute-confirm-alert">
            {error.message}
            {error.status === 401 && (
              <>
                {' '}
                <a className="compute-confirm-link" href={loginHref}>重新登录</a>
              </>
            )}
          </p>
        )}

        {run && (
          <>
            <dl className="compute-confirm-facts">
              <Fact label="任务状态" value={`${stateLabel}（${run.state || '未知'}）`} />
              <Fact label="报价编号" value={run.quote_id || quoteId} code />
              <Fact label="请求规格" value={taskSpecText(run)} />
              <Fact label="供应商规格" value={skuSpecText(run)} />
              <Fact label="积分上限" value={capFacts(run)} />
              <Fact label="计费费率" value={rateFacts(run)} />
              <Fact label="报价有效期" value={quoteOpen ? expiry.label : formatComputeTime(run.expires_at)} />
              <Fact
                label="库存与排队 ETA"
                value="未知（开通时由平台核实容量，无法提前给出排队时长）"
              />
              <Fact label="确认时间" value={formatComputeTime(run.confirmed_at)} />
            </dl>

            <ul className="compute-confirm-notices">
              <li>{COMPUTE_LOCKED_SPEC_NOTICE}</li>
              <li>{COMPUTE_UNKNOWN_CAPACITY_NOTICE}</li>
              <li>{COMPUTE_RESERVATION_NOTICE}</li>
              <li>{COMPUTE_SELF_CONFIRM_NOTICE}</li>
            </ul>

            {String(run.state || '') === 'quoted' && (
              <div className="compute-confirm-actions">
                <button
                  type="button"
                  className="oc-auth-btn compute-confirm-submit"
                  onClick={handleConfirm}
                  disabled={!confirmable}
                  aria-busy={submitting}
                >
                  {submitting ? '正在提交确认…' : '我确认，提交并预占积分上限'}
                </button>
                <p className="compute-confirm-hint">
                  {confirmable || submitting
                    ? '点击后才会发送这条确认；提交成功后积分上限被预占，作业随即进入排队。'
                    : (expiry.known && expiry.expired
                      ? '报价已过期，无法再确认。请回到对话重新获取报价。'
                      : '当前报价不可确认，请刷新状态或回到对话重新获取报价。')}
                </p>
              </div>
            )}

            {(progressRows.length > 0 || String(run.state || '') !== 'quoted') && (
              <section className="compute-confirm-progress" aria-label="开通与回收进度">
                <h2>开通、作业与回收</h2>
                <p className="compute-confirm-progress-state" role="status">
                  {stateLabel}
                  {polling ? `，每 ${Math.round(COMPUTE_POLL_INTERVAL_MS / 1000)} 秒自动刷新` : ''}
                </p>
                {progressRows.length > 0 ? (
                  <dl className="compute-confirm-facts compute-confirm-progress-facts">
                    {progressRows.map((row) => (
                      <Fact key={row.key} label={row.label} value={row.value} />
                    ))}
                  </dl>
                ) : (
                  <p className="compute-confirm-hint">平台还没有回写开通细节，稍后会显示在这里。</p>
                )}
                {ledger && (
                  <div className={`compute-confirm-ledger${ledger.known ? ' is-settled' : ''}`}>
                    <p><strong>{ledger.label}</strong></p>
                    <p className="compute-confirm-hint">{ledger.detail}</p>
                    {isComputeTerminal(run.state) && !ledger.known && (
                      <p className="compute-confirm-hint">任务已结束，账本终态以服务端记录为准。</p>
                    )}
                  </div>
                )}
              </section>
            )}
          </>
        )}

        {quoteId && (
          <div className="compute-confirm-footer">
            <button
              type="button"
              className="compute-confirm-refresh"
              onClick={() => setReloadKey((value) => value + 1)}
              disabled={loading || submitting}
            >
              {loading ? '刷新中…' : '刷新状态'}
            </button>
            <span className="compute-confirm-hint">本页只显示当前账号的这一个任务，不会列出其他账号的报价。</span>
          </div>
        )}
      </section>
    </main>
  );
}
