import React, { useState } from 'react';
import {
  AlertCircle,
  ArrowUpCircle,
  Bot,
  CheckCircle2,
  Cloud,
  PlusCircle,
  RefreshCw,
  RotateCcw,
  Server,
  ShieldAlert,
  Trash2,
  Zap,
} from 'lucide-react';

const CLOUD_STATUS_META = {
  provisioning: { label: '实例创建中', tone: 'info' },
  creating: { label: '实例创建中', tone: 'info' },
  running: { label: '运行中', tone: 'ok' },
  online: { label: '在线', tone: 'ok' },
  stopped: { label: '已停止', tone: 'warn' },
  missing: { label: '实例不存在', tone: 'danger' },
  error: { label: '异常', tone: 'danger' },
  failed: { label: '异常', tone: 'danger' },
  unknown: { label: '状态同步中', tone: 'muted' },
};

const statusMeta = (status) => (
  CLOUD_STATUS_META[String(status || '').toLowerCase()]
  || CLOUD_STATUS_META.unknown
);

/**
 * 云托管专属面板 —— 当创建助手的「部署方式」选中云托管时替换自托管表单。
 * 聚合云托管配额、创建云端虚拟员工、以及已有云托管员工的管理
 * （版本/更新/回滚/重置/删除），全部走云控制面。
 */
const randomCode = () => String(Math.floor(1000 + Math.random() * 9000));

export default function CloudWorkerPanel({
  quota,
  quotaError,
  workers = [],
  images = [],
  actioning = null,
  showHostingSwitch = true,
  onCreate,
  onUpdate,
  onRollback,
  onReset,
  onDelete,
  onSwitchMode,
}) {
  const [name, setName] = useState('');
  const [creating, setCreating] = useState(false);
  const [createError, setCreateError] = useState('');
  // tenant_name -> selected target version ('' = latest)
  const [versions, setVersions] = useState({});
  // Reset captcha flow: tenant being confirmed / its code / typed input / mismatch
  const [resetConfirming, setResetConfirming] = useState(null);
  const [resetCodes, setResetCodes] = useState({});
  const [resetInputs, setResetInputs] = useState({});
  const [resetErrors, setResetErrors] = useState({});

  const canCreate = Boolean(quota && quota.enabled && quota.remaining > 0);
  const usedPct = quota && quota.total > 0
    ? Math.min(100, Math.round((quota.used / quota.total) * 100))
    : 0;

  // Available image versions (deduplicated, order from the control plane).
  const imageVersions = [...new Set(
    (images || []).map((img) => img?.version).filter(Boolean),
  )];

  const handleSubmit = async () => {
    const displayName = name.trim();
    if (!displayName || creating || !canCreate) return;
    setCreating(true);
    setCreateError('');
    try {
      await onCreate(displayName);
      setName('');
    } catch (e) {
      // 显示按错误码分类后的提示（具体技术原因只在后端日志）
      setCreateError(e?.message || '云端资源创建失败，请稍后重试或联系管理员');
    } finally {
      setCreating(false);
    }
  };

  const beginReset = (tenantName) => {
    setResetErrors({});
    setResetCodes((prev) => (prev[tenantName] ? prev : { ...prev, [tenantName]: randomCode() }));
    setResetConfirming(tenantName);
  };

  const cancelReset = () => {
    setResetConfirming(null);
    setResetInputs({});
    setResetErrors({});
  };

  const confirmReset = (worker) => {
    const tenantName = worker.tenant_name;
    const code = resetCodes[tenantName];
    const input = (resetInputs[tenantName] || '').trim();
    if (!code || input !== code) {
      setResetErrors({ [tenantName]: true });
      return;
    }
    const version = versions[tenantName] || '';
    onReset(worker, version, { verified: true });
    cancelReset();
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
      {/* 部署方式：云托管面板自带切换入口，切回自托管恢复原表单（管理视图可隐藏） */}
      {showHostingSwitch && (
        <fieldset className="cc-agent-hosting">
          <legend><span><Zap size={16} /></span>部署方式 <small>高级设置</small></legend>
          <label>
            <input
              type="radio"
              name="hosting"
              checked={false}
              onChange={() => { if (onSwitchMode) onSwitchMode(); }}
            />
            <span><strong>自托管</strong><small>生成本地身份 Key，后续连接你的服务。</small></span>
          </label>
          <label className="active">
            <input
              type="radio"
              name="hosting"
              checked
              readOnly
              onChange={() => {}}
            />
            <span>
              <strong>云托管</strong>
              <small>
                {quotaError
                  ? '云端状态查询失败，请稍后重试'
                  : (quota && quota.enabled
                      ? `部署到云端虚拟员工（可创建 ${quota.remaining}/${quota.total}）`
                      : '云端部署当前未开放，请联系管理员开通')}
              </small>
            </span>
          </label>
        </fieldset>
      )}

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
              {creating ? <><RefreshCw size={14} className="cc-spin" /> 正在供给云端实例...</> : '创建云托管员工'}
            </button>
            {creating && (
              <p className="cc-cloud-create-hint">
                正在创建云端实例并部署，通常需要 1-3 分钟，请稍候…
              </p>
            )}
            {createError && (
              <p className="cc-cloud-create-error"><AlertCircle size={13} /> {createError}</p>
            )}
            {!creating && !createError && (
              <p className="cc-cloud-create-hint">
                创建后会供给一台云端虚拟员工并自动完成部署，无需配置身份 Key，可直接使用。
              </p>
            )}
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
                    {worker.app_version && (
                      <span>应用 <b>{worker.app_version}</b></span>
                    )}
                    {worker.cloud_version && (
                      <span>基础镜像 <b>{worker.cloud_version}</b></span>
                    )}
                    {worker.cloud_image_id && (
                      <span>镜像 <b>{worker.cloud_image_id.slice(0, 8)}</b></span>
                    )}
                    {!worker.app_version && !worker.cloud_version && !worker.cloud_image_id && (
                      <span>
                        {worker.cloud_status && worker.cloud_status !== 'unknown'
                          ? '版本信息收集中…'
                          : '状态信息同步中…'}
                      </span>
                    )}
                  </div>

                  <div className="cc-cloud-worker-actions">
                    <label className="cc-cloud-version-field">
                      <span>目标版本</span>
                      <select
                        className="cc-cloud-version-select"
                        value={versions[worker.tenant_name] || ''}
                        disabled={acting || imageVersions.length === 0}
                        onChange={(e) => setVersions((prev) => ({ ...prev, [worker.tenant_name]: e.target.value }))}
                        title={imageVersions.length === 0 ? '暂无可用版本' : '更新、回滚或重置使用的目标版本'}
                      >
                        {imageVersions.length === 0 ? (
                          <option value="">暂无可用版本</option>
                        ) : (
                          <>
                            <option value="">最新版本</option>
                            {imageVersions.map((v) => <option key={v} value={v}>{v}</option>)}
                          </>
                        )}
                      </select>
                    </label>

                    <button
                      type="button"
                      className="oc-btn oc-btn-primary"
                      onClick={() => onUpdate(worker, versions[worker.tenant_name] || '')}
                      disabled={acting || imageVersions.length === 0}
                      title={imageVersions.length === 0 ? '暂无可用版本，无法更新' : '更新应用到所选版本，保留当前数据'}
                    >
                      {acting ? '处理中...' : <><ArrowUpCircle size={13} /> 更新</>}
                    </button>

                    <button
                      type="button"
                      className="oc-btn oc-btn-default"
                      onClick={() => onRollback(worker, versions[worker.tenant_name] || '', { fromPanel: true })}
                      disabled={acting || imageVersions.length === 0}
                      title={imageVersions.length === 0 ? '暂无可用版本，无法回滚' : '回滚应用到所选版本，保留当前数据'}
                    >
                      {acting ? '处理中...' : <><RotateCcw size={13} /> 回滚</>}
                    </button>

                    {resetConfirming === worker.tenant_name ? (
                      <div className="cc-cloud-reset-confirm">
                        <p className="cc-cloud-reset-confirm-title">
                          <ShieldAlert size={13} /> 重置「{worker.display_name}」会清空数据，请输入验证码确认
                        </p>
                        <p className="cc-cloud-reset-confirm-code">
                          验证码 <b>{resetCodes[worker.tenant_name]}</b>
                        </p>
                        <div className="cc-cloud-reset-confirm-input">
                          <input
                            type="text"
                            value={resetInputs[worker.tenant_name] || ''}
                            onChange={(e) => {
                              setResetInputs((prev) => ({ ...prev, [worker.tenant_name]: e.target.value }));
                              setResetErrors({});
                            }}
                            placeholder="输入验证码"
                            disabled={acting}
                          />
                          <button
                            type="button"
                            className="oc-btn oc-btn-primary"
                            onClick={() => confirmReset(worker)}
                            disabled={acting}
                          >
                            确认重置
                          </button>
                          <button
                            type="button"
                            className="oc-btn oc-btn-default"
                            onClick={cancelReset}
                          >
                            取消
                          </button>
                        </div>
                        {resetErrors[worker.tenant_name] && (
                          <p className="cc-cloud-quota-err">验证码不正确，请重新输入</p>
                        )}
                      </div>
                    ) : (
                      <button
                        type="button"
                        className="oc-btn oc-btn-default"
                        onClick={() => beginReset(worker.tenant_name)}
                        disabled={acting}
                        title="重置：销毁实例并从所选镜像版本重建，所有数据丢失（需验证码）"
                      >
                        {acting ? '处理中...' : <><RefreshCw size={13} /> 重置</>}
                      </button>
                    )}

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
        <CheckCircle2 size={13} /> 更新与回滚只切换应用并保留数据；重置会按所选镜像重建并清空数据。
      </p>
    </div>
  );
}
