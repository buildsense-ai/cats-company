import React, { useState } from 'react';
import {
  AlertCircle,
  Bot,
  CheckCircle2,
  Cloud,
  PlusCircle,
  RefreshCw,
  RotateCcw,
  Server,
  Trash2,
} from 'lucide-react';

const CLOUD_STATUS_META = {
  provisioning: { label: '供给中', tone: 'info' },
  running: { label: '运行中', tone: 'ok' },
  online: { label: '在线', tone: 'ok' },
  stopped: { label: '已停止', tone: 'warn' },
  error: { label: '异常', tone: 'danger' },
  failed: { label: '异常', tone: 'danger' },
};

const statusMeta = (status) => (
  CLOUD_STATUS_META[String(status || '').toLowerCase()]
  || { label: '未知', tone: 'warn' }
);

/**
 * 云托管专属面板 —— 当创建助手的「部署方式」选中云托管时替换自托管表单。
 * 聚合云托管配额、创建云端虚拟员工、以及已有云托管员工的管理
 * （版本/回滚/重置/删除），全部走云控制面。
 */
export default function CloudWorkerPanel({
  quota,
  quotaError,
  workers = [],
  actioning = null,
  onCreate,
  onRollback,
  onReset,
  onDelete,
}) {
  const [name, setName] = useState('');
  const [creating, setCreating] = useState(false);

  const canCreate = Boolean(quota && quota.enabled && quota.remaining > 0);
  const usedPct = quota && quota.total > 0
    ? Math.min(100, Math.round((quota.used / quota.total) * 100))
    : 0;

  const handleSubmit = async () => {
    const displayName = name.trim();
    if (!displayName || creating || !canCreate) return;
    setCreating(true);
    try {
      await onCreate(displayName);
      setName('');
    } catch {
      // 错误已由 modal 层反馈
    } finally {
      setCreating(false);
    }
  };

  const quotaNote = quotaError ? (
    <p className="cc-cloud-quota-err"><AlertCircle size={13} /> 云端状态查询失败，请稍后重试</p>
  ) : (!quota || !quota.enabled) ? (
    <p className="cc-cloud-quota-err"><AlertCircle size={13} /> 云端部署当前未开放，请联系管理员开通</p>
  ) : (
    <>
      <div className="cc-cloud-quota-bar"><i style={{ width: `${usedPct}%` }} /></div>
      <p>还可创建 <b>{quota.remaining}</b> 个云端虚拟员工</p>
    </>
  );

  return (
    <div className="cc-cloud-panel">
      {/* 配额与说明 */}
      <section className="cc-cloud-quota" aria-label="云托管配额">
        <div className="cc-cloud-quota-head">
          <div><Cloud size={16} /> <strong>云托管配额</strong></div>
          <span>{quota ? `${quota.used}/${quota.total} 已使用` : '—'}</span>
        </div>
        {quotaNote}
      </section>

      {/* 创建云托管员工 */}
      <section className="cc-agent-create-card cc-cloud-create-card">
        <h3><PlusCircle size={17} /> 创建云托管员工</h3>
        {canCreate ? (
          <>
            <label>
              <span>员工名称 <b>*</b></span>
              <input
                type="text"
                value={name}
                onChange={(e) => setName(e.target.value)}
                placeholder="例如：云端审查助手"
                className="oc-auth-input"
                disabled={creating}
                maxLength={40}
              />
            </label>
            <button
              type="button"
              className="oc-btn oc-btn-primary cc-cloud-create-submit"
              onClick={handleSubmit}
              disabled={creating || !name.trim()}
            >
              {creating ? '创建中...' : '创建云托管员工'}
            </button>
            <p className="cc-cloud-create-hint">
              创建后会供给一台云端虚拟员工并自动完成部署，无需配置身份 Key，可直接使用。
            </p>
          </>
        ) : (
          <p className="cc-cloud-quota-err">
            {quotaError ? '云端状态查询失败，暂时无法创建。' : '配额已用完或未开放，暂时无法继续创建。'}
          </p>
        )}
      </section>

      {/* 已有云托管员工 */}
      <section aria-label="已有云托管员工">
        <div className="cc-cloud-workers-head">
          <h3><Server size={16} /> 已有云托管员工</h3>
          <span>{workers.length} 个</span>
        </div>

        {workers.length === 0 ? (
          <div className="cc-cloud-empty">
            <Bot size={36} strokeWidth={1.5} />
            <strong>还没有云托管员工</strong>
            <p>在上方输入名称并创建，云端实例供给完成后会出现在这里。</p>
          </div>
        ) : (
          <div className="cc-cloud-worker-list">
            {workers.map((worker) => {
              const id = worker.id || worker.uid;
              const meta = statusMeta(worker.cloud_status);
              const acting = actioning === worker.tenant_name;
              return (
                <div key={worker.tenant_name || id} className="cc-cloud-worker">
                  <div className="cc-cloud-worker-head">
                    <div className="cc-cloud-worker-avatar">
                      {(worker.display_name || worker.username || '?').charAt(0).toUpperCase()}
                    </div>
                    <div className="cc-cloud-worker-name">
                      <strong>{worker.display_name}</strong>
                      <small>@{worker.username} · uid {id}</small>
                    </div>
                    <span className={`cc-cloud-worker-status ${meta.tone}`}>
                      {meta.label}
                    </span>
                  </div>

                  <div className="cc-cloud-worker-meta">
                    {worker.cloud_version && (
                      <span>版本 <b>{worker.cloud_version}</b></span>
                    )}
                    {worker.cloud_image_id && (
                      <span>镜像 <b>{worker.cloud_image_id.slice(0, 8)}</b></span>
                    )}
                    {!worker.cloud_version && !worker.cloud_image_id && (
                      <span>状态信息同步中…</span>
                    )}
                  </div>

                  <div className="cc-cloud-worker-actions">
                    <button
                      type="button"
                      className="oc-btn oc-btn-default"
                      onClick={() => onRollback(worker)}
                      disabled={acting}
                      title="回滚：切换到所选镜像版本，保留当前数据"
                    >
                      {acting ? '处理中...' : <><RotateCcw size={13} /> 回滚</>}
                    </button>
                    <button
                      type="button"
                      className="oc-btn oc-btn-default"
                      onClick={() => onReset(worker)}
                      disabled={acting}
                      title="重置：销毁实例并从镜像重建，所有数据丢失"
                    >
                      {acting ? '处理中...' : <><RefreshCw size={13} /> 重置</>}
                    </button>
                    <button
                      type="button"
                      className="oc-btn oc-btn-default cc-agent-card-delete"
                      onClick={() => onDelete(worker)}
                      disabled={acting}
                      title="删除：销毁云端实例并删除该助手"
                    >
                      <Trash2 size={13} />
                    </button>
                  </div>
                </div>
              );
            })}
          </div>
        )}
      </section>

      <p className="cc-cloud-footnote">
        <CheckCircle2 size={13} /> 云托管员工由云端控制面统一管理，回滚保留数据，重置会清空数据。
      </p>
    </div>
  );
}
