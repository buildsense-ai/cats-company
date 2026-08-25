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
  offline: { label: '离线', tone: 'muted' },
  stopped: { label: '已停止', tone: 'warn' },
  missing: { label: '实例不存在', tone: 'danger' },
  error: { label: '异常', tone: 'danger' },
  failed: { label: '异常', tone: 'danger' },
  unavailable: { label: '状态暂不可用', tone: 'muted' },
  unknown: { label: '状态未知', tone: 'muted' },
};

const statusMeta = (status) => (
  CLOUD_STATUS_META[String(status || '').toLowerCase()]
  || CLOUD_STATUS_META.unknown
);

const parseVersion = (value) => {
  const match = String(value || '').trim().replace(/^v/i, '').match(/^(\d+)(?:\.(\d+))?(?:\.(\d+))?/);
  return match ? [Number(match[1]), Number(match[2] || 0), Number(match[3] || 0)] : null;
};

const compareVersions = (left, right) => {
  const a = parseVersion(left);
  const b = parseVersion(right);
  if (!a || !b) return null;
  for (let i = 0; i < a.length; i += 1) {
    if (a[i] !== b[i]) return a[i] > b[i] ? 1 : -1;
  }
  return 0;
};

const CLOUD_ACTION_LABELS = {
  update: '正在更新应用版本，期间员工会短暂离线，请保持页面打开',
  rollback: '正在回滚应用版本，期间员工会短暂离线，请保持页面打开',
  reset: '正在原实例内重装基础镜像，所有数据会被清空，请勿关闭或重复提交',
  delete: '正在销毁实例并删除员工',
};

// Do not render an exhausted quota as `0/1`: users can read that as a
// remaining slot. The managed-hosting switch should describe the actionable
// state in plain language.
const cloudHostingSummary = (quota, quotaError) => {
  if (quotaError) return '云端状态查询失败，请稍后重试';
  if (!quota || !quota.enabled) return '云端部署当前未开放，请联系管理员开通';
  const remaining = Number(quota.remaining);
  return remaining > 0
    ? `部署到云端虚拟员工（还可创建 ${remaining} 次）`
    : '云端虚拟员工创建权益已用完';
};

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
  releases = [],
  actions = null,
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
  const [updateSelections, setUpdateSelections] = useState({});
  const [rollbackSelections, setRollbackSelections] = useState({});
  const [imageSelections, setImageSelections] = useState({});
  // Reset captcha flow: tenant being confirmed / its code / typed input / mismatch
  const [resetConfirming, setResetConfirming] = useState(null);
  const [resetCodes, setResetCodes] = useState({});
  const [resetInputs, setResetInputs] = useState({});
  const [resetErrors, setResetErrors] = useState({});

  // Missing action metadata means an older backend; preserve compatibility
  // during a rolling web/server deployment. Explicit false always disables.
  const actionAvailable = (action) => actions?.[action] !== false;
  const canCreate = Boolean(
    quota && quota.enabled && quota.remaining > 0 && actionAvailable('create'),
  );
  const usedPct = quota && quota.total > 0
    ? Math.min(100, Math.round((quota.used / quota.total) * 100))
    : 0;

  // Available image versions (deduplicated, order from the control plane).
  const imageVersions = [...new Set(
    (images || []).map((img) => img?.version).filter(Boolean),
  )];
  const releaseVersions = [...new Set(
    (releases || []).map((release) => release?.version).filter(Boolean),
  )];
  const activeAction = typeof actioning === 'string'
    ? { name: actioning, action: 'update' }
    : (actioning || {});
  const hasActiveAction = Boolean(activeAction.name);

  const handleSubmit = async () => {
    const displayName = name.trim();
    if (!displayName || creating || hasActiveAction || !canCreate) return;
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
    const version = imageSelections[tenantName] || worker.cloud_version || imageVersions[0] || '';
    Promise.resolve(onReset(worker, version, { verified: true }))
      .then(cancelReset, () => {});
  };

  const quotaNote = quotaError ? (
    <p className="cc-cloud-quota-err"><AlertCircle size={13} /> 云端状态查询失败，请稍后重试</p>
  ) : !actionAvailable('create') ? (
    <p className="cc-cloud-quota-err"><AlertCircle size={13} /> 云端创建服务尚未配置，请联系管理员</p>
  ) : (!quota || !quota.enabled) ? (
    <p className="cc-cloud-quota-err"><AlertCircle size={13} /> 云端部署当前未开放，请联系管理员开通</p>
  ) : quota.remaining <= 0 ? (
    <p className="cc-cloud-quota-err"><AlertCircle size={13} /> 云端虚拟员工创建权益已用完，暂时无法继续创建</p>
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
              <small>{cloudHostingSummary(quota, quotaError)}</small>
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
                disabled={creating || hasActiveAction}
                maxLength={40}
              />
            </label>
            <button
              type="button"
              className="oc-btn oc-btn-primary cc-cloud-create-submit"
              onClick={handleSubmit}
              disabled={creating || hasActiveAction || !name.trim()}
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
            {quotaError
              ? '云端状态查询失败，暂时无法创建。'
              : (!actionAvailable('create')
                  ? '云端创建服务尚未配置，请联系管理员。'
                  : '配额已用完或未开放，暂时无法继续创建。')}
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
              const acting = activeAction.name === worker.tenant_name;
              const actionName = acting ? activeAction.action : '';
              const currentVersion = parseVersion(worker.app_version);
              const upgradeVersions = currentVersion
                ? releaseVersions.filter((version) => compareVersions(version, worker.app_version) > 0)
                : [];
              const rollbackVersions = currentVersion
                ? releaseVersions.filter((version) => compareVersions(version, worker.app_version) < 0)
                : [];
              const updateTarget = updateSelections[worker.tenant_name] || upgradeVersions[0] || '';
              const rollbackTarget = rollbackSelections[worker.tenant_name] || rollbackVersions[0] || '';
              const imageTarget = imageSelections[worker.tenant_name]
                || (imageVersions.includes(worker.cloud_version) ? worker.cloud_version : '')
                || imageVersions[0] || '';
              return (
                <div
                  key={worker.tenant_name || id}
                  className="cc-cloud-worker"
                  aria-busy={acting ? 'true' : undefined}
                >
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
                    <span>应用版本 <b>{worker.app_version || '暂未读取'}</b></span>
                    <span>基础镜像 <b>{worker.cloud_version || '暂未读取'}</b></span>
                    {!worker.app_version && !worker.cloud_version && !worker.cloud_image_id && (
                      <span>
                        {['provisioning', 'creating'].includes(String(worker.cloud_status || '').toLowerCase())
                          ? '实例创建完成后显示版本信息'
                          : '暂未读取到版本信息'}
                      </span>
                    )}
                  </div>

                  <div className="cc-cloud-worker-controls">
                    <div className="cc-cloud-version-controls">
                      <label className="cc-cloud-version-field">
                        <span>更新版本</span>
                        <select
                          className="cc-cloud-version-select cc-cloud-update-version-select"
                          value={updateTarget}
                          disabled={hasActiveAction || upgradeVersions.length === 0 || !actionAvailable('update')}
                          onChange={(e) => setUpdateSelections((prev) => ({ ...prev, [worker.tenant_name]: e.target.value }))}
                          title={!currentVersion ? '当前应用版本未知，无法判断可更新版本' : (upgradeVersions.length === 0 ? '暂无高于当前版本的应用发布' : '仅显示高于当前应用版本的发布')}
                        >
                          {upgradeVersions.length === 0 ? (
                            <option value="">{currentVersion ? '暂无更高版本' : '当前版本未知'}</option>
                          ) : (
                            upgradeVersions.map((v) => <option key={v} value={v}>{v}</option>)
                          )}
                        </select>
                      </label>

                      <label className="cc-cloud-version-field">
                        <span>回滚版本</span>
                        <select
                          className="cc-cloud-version-select cc-cloud-rollback-version-select"
                          value={rollbackTarget}
                          disabled={hasActiveAction || rollbackVersions.length === 0 || !actionAvailable('rollback')}
                          onChange={(e) => setRollbackSelections((prev) => ({ ...prev, [worker.tenant_name]: e.target.value }))}
                          title={!currentVersion ? '当前应用版本未知，无法判断可回滚版本' : (rollbackVersions.length === 0 ? '暂无低于当前版本的应用发布' : '仅显示低于当前应用版本的发布')}
                        >
                          {rollbackVersions.length === 0 ? (
                            <option value="">{currentVersion ? '暂无更低版本' : '当前版本未知'}</option>
                          ) : (
                            rollbackVersions.map((v) => <option key={v} value={v}>{v}</option>)
                          )}
                        </select>
                      </label>

                      <label className="cc-cloud-version-field">
                        <span>基础镜像</span>
                        <select
                          className="cc-cloud-image-select"
                          value={imageTarget}
                          disabled={hasActiveAction || imageVersions.length === 0 || !actionAvailable('reset')}
                          onChange={(e) => setImageSelections((prev) => ({ ...prev, [worker.tenant_name]: e.target.value }))}
                          title={imageVersions.length === 0 ? '暂无可用基础镜像' : '仅重置实例时使用，重置会清空数据'}
                        >
                          {imageVersions.length === 0 ? (
                            <option value="">暂无基础镜像</option>
                          ) : (
                            imageVersions.map((v) => <option key={v} value={v}>{v}</option>)
                          )}
                        </select>
                      </label>
                    </div>
                    <p className="cc-cloud-version-hint">更新只显示更高版本；回滚只显示更低版本；基础镜像用于重置并会清空数据。</p>

                    <div className="cc-cloud-worker-actions">
                    <button
                      type="button"
                      className="oc-btn oc-btn-primary"
                      onClick={() => onUpdate(worker, updateTarget)}
                      disabled={hasActiveAction || !updateTarget || !actionAvailable('update')}
                      title={!actionAvailable('update') ? '云端更新服务尚未配置' : (!updateTarget ? '暂无高于当前版本的应用发布，无法更新' : '更新到所选应用版本，保留当前数据')}
                    >
                      {actionName === 'update' ? <><RefreshCw size={13} className="cc-spin" /> 更新中...</> : <><ArrowUpCircle size={13} /> 更新</>}
                    </button>

                    <button
                      type="button"
                      className="oc-btn oc-btn-default"
                      onClick={() => onRollback(worker, rollbackTarget, { fromPanel: true })}
                      disabled={hasActiveAction || !rollbackTarget || !actionAvailable('rollback')}
                      title={!actionAvailable('rollback') ? '云端回滚服务尚未配置' : (!rollbackTarget ? '暂无低于当前版本的应用发布，无法回滚' : '回滚到所选应用版本，保留当前数据')}
                    >
                      {actionName === 'rollback' ? <><RefreshCw size={13} className="cc-spin" /> 回滚中...</> : <><RotateCcw size={13} /> 回滚</>}
                    </button>

                    {resetConfirming === worker.tenant_name ? (
                      <div className="cc-cloud-reset-confirm">
                        <p className="cc-cloud-reset-confirm-title">
                          <ShieldAlert size={13} /> 重置「{worker.display_name}」会在原实例内使用基础镜像 {imageTarget} 重装系统盘，并清空全部数据
                        </p>
                        <p className="cc-cloud-reset-confirm-warning">此操作不可撤销，员工会暂时离线。请核对镜像版本后输入验证码完成二次确认。</p>
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
                            disabled={hasActiveAction}
                          />
                          <button
                            type="button"
                            className="oc-btn oc-btn-primary"
                            onClick={() => confirmReset(worker)}
                            disabled={hasActiveAction}
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
                        disabled={hasActiveAction || imageVersions.length === 0 || !actionAvailable('reset')}
                          title={!actionAvailable('reset') ? '云端重置服务尚未配置' : (imageVersions.length === 0 ? '暂无可用基础镜像，无法重置' : '重置：在原实例内重装所选镜像，保留包月到期时间但清空所有数据（需验证码）')}
                      >
                          {actionName === 'reset' ? <><RefreshCw size={13} className="cc-spin" /> 重置中...</> : <><RefreshCw size={13} /> 重置</>}
                      </button>
                    )}

                    <button
                      type="button"
                      className="oc-btn oc-btn-default cc-agent-card-delete"
                      onClick={() => onDelete(worker)}
                      disabled={hasActiveAction || !actionAvailable('delete')}
                      aria-label={actionName === 'delete' ? '删除中' : `删除 ${worker.display_name || worker.username || '员工'}`}
                      title={!actionAvailable('delete') ? '云端删除服务尚未配置' : '删除：销毁云端实例并删除该助手'}
                    >
                      {actionName === 'delete' ? <RefreshCw size={13} className="cc-spin" /> : <Trash2 size={13} />}
                    </button>
                    </div>
                  </div>
                  {acting && (
                    <div className="cc-cloud-operation-status" role="status" aria-live="polite">
                      <RefreshCw size={14} className="cc-spin" />
                      <div className="cc-cloud-operation-status-copy">
                        <strong>{CLOUD_ACTION_LABELS[actionName] || '云端操作正在执行，请勿重复提交'}</strong>
                        <span>云端正在处理，请勿重复点击；完成后页面会自动刷新状态。</span>
                        <i className="cc-cloud-operation-progress" aria-hidden="true"><b /></i>
                      </div>
                    </div>
                  )}
                </div>
              );
            })}
          </div>
        )}
      </section>

      {actions && Object.entries(actions).some(([, enabled]) => enabled === false) && (
        <p className="cc-cloud-footnote">
          <AlertCircle size={13} /> 部分云端管理功能暂不可用，已禁用对应按钮。
        </p>
      )}

      <p className="cc-cloud-footnote">
        <CheckCircle2 size={13} /> 更新与回滚只切换应用并保留数据；重置会在原实例内按所选镜像重装系统盘并清空数据。
      </p>
    </div>
  );
}
