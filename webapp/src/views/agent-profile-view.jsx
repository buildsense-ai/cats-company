import React, { useEffect, useRef, useState } from 'react';
import { AppWindow, ArrowLeft, Puzzle, QrCode } from 'lucide-react';
import { AssistantBadge, X } from '../widgets/desktop-icons';
import { api } from '../api';
import Avatar from '../widgets/avatar';
import AgentDetailTabs from '../widgets/agent-detail-tabs';
import MobileChannelBindModal from '../widgets/mobile-channel-bind-modal';
import useDialogBehavior from '../utils/use-dialog-behavior';
import '../css/agent-profile-view.css';

const dateFormatter = new Intl.DateTimeFormat('zh-CN', { dateStyle: 'medium' });
const formatDate = value => value && Number.isFinite(Date.parse(value)) ? dateFormatter.format(new Date(value)) : '';
const fetchSkills = async (uid, signal) => (await api.getAgentSkills(uid, { signal, timeoutMs: 10_000 }))?.skills;
const fetchApps = async (uid, signal) => (await api.listArtifactApps(uid, { signal }))?.apps;
const profileTabs = [
  ['basic', '基本信息'],
  ['apps', '使用与协作'],
  ['skills', '行为与能力'],
];

function useProfileList(agentId, load, enabled = true) {
  const [attempt, setAttempt] = useState(0);
  const [result, setResult] = useState({ status: 'loading', items: [] });

  useEffect(() => {
    if (!enabled) return undefined;
    const controller = new AbortController();
    const { signal } = controller;
    const refresh = async () => {
      setResult({ status: 'loading', items: [] });
      try {
        const items = await load(agentId, signal);
        if (signal.aborted) return;
        if (!Array.isArray(items)) throw new Error('Invalid profile list');
        setResult({ status: 'ready', items: items.filter(Boolean) });
      } catch (error) {
        if (signal.aborted) return;
        setResult({
          status: [401, 403].includes(Number(error?.status)) ? 'restricted' : 'error',
          items: [],
        });
      }
    };
    void refresh();
    return () => controller.abort();
  }, [agentId, attempt, enabled, load]);

  return { ...result, retry: () => setAttempt(value => value + 1) };
}

function ProfileListState({ status, label, empty, retry, children }) {
  if (status === 'loading') return <p role="status">正在读取{label}…</p>;
  if (status === 'restricted') return <p>你暂时无法查看此助手的{label}。</p>;
  if (status === 'error') {
    return (
      <div className="cc-agent-profile-error">
        <p role="status">{label}列表暂时无法读取。</p>
        <button type="button" className="cc-recovery-button" onClick={retry}>重试</button>
      </div>
    );
  }
  return empty ? <p>暂无可查看的{label}</p> : children;
}

export default function AgentProfileView({ agent, onBack, onClose, returnFocusRef }) {
  const agentId = agent.uid || agent.id;
  const name = agent.display_name || agent.username || '未命名助手';
  const online = agent.is_online ?? agent.online;
  const addedOn = formatDate(agent.added_at);
  const dialogRef = useRef(null);
  const [section, setSection] = useState('basic');
  const [appsVisited, setAppsVisited] = useState(false);
  const [mobileScanOpen, setMobileScanOpen] = useState(false);
  const skills = useProfileList(agentId, fetchSkills);
  const apps = useProfileList(agentId, fetchApps, appsVisited);
  const skillEntries = skills.items.filter(skill => skill.skillId);
  const appEntries = apps.items.filter(app => app.id);

  useDialogBehavior(dialogRef, { onClose, initialFocus: 'heading', returnFocusRef });

  return (
    <div className="oc-modal-overlay" style={{ zIndex: 1000 }}
      onClick={(event) => { if (event.target === event.currentTarget) onClose(); }}>
      <div ref={dialogRef} className="oc-modal cc-agent-manager cc-agent-manager-manage cc-secondary-interface cc-agent-profile-shell"
        role="dialog" aria-modal="true" aria-labelledby="cc-agent-profile-heading">
        <header className="oc-modal-header cc-agent-manager-header">
          <div className="cc-agent-manager-nav">
            <button className="cc-agent-profile-back cc-agent-manager-header-action" type="button" onClick={onBack} aria-label="返回">
              <ArrowLeft size={16} aria-hidden="true" />
            </button>
            <h1 id="cc-agent-profile-heading" className="cc-agent-manager-title cc-dialog-reading-focus" tabIndex={-1}>
              <AssistantBadge size={22} aria-hidden="true" />
              <span>
                <span className="cc-agent-title-context">助手资料</span>
                <span className="cc-agent-title-separator" aria-hidden="true"> · </span>
                <span className="cc-agent-title-name" title={name}>{name}</span>
              </span>
            </h1>
          </div>
          <div className="cc-agent-manager-header-actions">
            <button type="button" className="cc-agent-manager-header-action" aria-label="手机扫码" onClick={() => setMobileScanOpen(true)}>
              <QrCode size={15} aria-hidden="true" /><span>手机扫码</span>
            </button>
            <button type="button" className="cc-dialog-close" onClick={onClose} aria-label="关闭">
              <X size={18} aria-hidden="true" />
            </button>
          </div>
        </header>

        <div className="oc-modal-body cc-agent-manager-body">
          <div className="cc-agent-manage-form">
            <div className="cc-agent-detail-sections">
              <AgentDetailTabs ariaLabel="助手资料栏目" idPrefix="cc-agent-profile" tabs={profileTabs} value={section}
                onChange={(id) => {
                  setSection(id);
                  if (id === 'apps') setAppsVisited(true);
                }} />
              <div className="cc-agent-manage-section-body cc-agent-detail-panel" id="cc-agent-profile-content"
                role="tabpanel" aria-labelledby={`cc-agent-profile-tab-${section}`} tabIndex={0}>
                {section === 'basic' && (
                  <div className="cc-agent-manage-basic-layout">
                    <div className="cc-agent-manage-basic-fields">
                      <div className="oc-form-group">
                        <h2 className="cc-field-title">头像</h2>
                        <div className="cc-agent-manage-avatar-wrap">
                          <Avatar name={name} src={agent.avatar_url} size={64} identityFallback />
                        </div>
                      </div>
                      <div className="oc-form-group">
                        <h2 className="cc-field-title">名称</h2>
                        <p className="oc-auth-input cc-agent-profile-value">{name}</p>
                      </div>
                      <div className="oc-form-group cc-agent-manage-description-field">
                        <h2 className="cc-field-title">用途说明</h2>
                        <p className="oc-auth-input cc-agent-manage-description cc-agent-profile-value">
                          {agent.description?.trim() || '暂无用途说明'}
                        </p>
                      </div>
                    </div>

                    <section className="cc-agent-collaboration-card cc-agent-profile-overview" aria-labelledby="cc-agent-profile-overview">
                      <h2 className="cc-field-title" id="cc-agent-profile-overview">助手概览</h2>
                      <dl>
                        <div><dt>UID</dt><dd>{agentId}</dd></div>
                        {agent.username && <div><dt>用户名</dt><dd>@{agent.username}</dd></div>}
                        {agent.owner_id && <div><dt>创建者 UID</dt><dd>{agent.owner_id}</dd></div>}
                        {typeof online === 'boolean' && <div><dt>状态</dt><dd><span className={`cc-agent-profile-presence${online ? ' is-online' : ''}`}>{online ? '在线' : '离线'}</span></dd></div>}
                        {typeof agent.is_cloud === 'boolean' && <div><dt>部署方式</dt><dd>{agent.is_cloud ? '云托管' : '自托管'}</dd></div>}
                        {addedOn && <div><dt>添加时间</dt><dd>{addedOn}</dd></div>}
                      </dl>
                    </section>
                  </div>
                )}
                {section === 'apps' && (
                  <section className="cc-agent-collaboration-card" aria-busy={apps.status === 'loading'}>
                    <div className="cc-agent-collaboration-heading">
                      <h3><AppWindow size={16} strokeWidth={1.8} aria-hidden="true" />共享应用</h3>
                    </div>
                    <ProfileListState status={apps.status} label="共享应用" empty={!appEntries.length} retry={apps.retry}>
                      <ul className="cc-agent-profile-list">
                        {appEntries.map(app => {
                          const updatedOn = formatDate(app.updated_at);
                          return (
                            <li key={app.id}>
                              <div>
                                <strong>{app.title || app.name || app.id}</strong>
                                {app.description && <p>{app.description}</p>}
                              </div>
                              {updatedOn && <small>更新于 {updatedOn}</small>}
                            </li>
                          );
                        })}
                      </ul>
                    </ProfileListState>
                  </section>
                )}
                {section === 'skills' && (
                  <section className="cc-agent-capability-summary cc-agent-profile-capabilities" aria-busy={skills.status === 'loading'}>
                    <div className="cc-agent-capability-summary-icon" aria-hidden="true"><Puzzle size={18} /></div>
                    <div className="cc-agent-capability-summary-copy">
                      <h3>可查看的能力</h3>
                      <ProfileListState status={skills.status} label="能力" empty={!skillEntries.length} retry={skills.retry}>
                        <ul className="cc-agent-profile-list">
                          {skillEntries.map(skill => {
                            const addedAt = formatDate(skill.addedAt);
                            return (
                              <li key={JSON.stringify([skill.skillId, skill.version])}>
                                <div>
                                  <strong>{skill.displayName || skill.skillId}</strong>
                                  {addedAt && <p>添加于 {addedAt}</p>}
                                </div>
                                {skill.version && <small>{skill.version}</small>}
                              </li>
                            );
                          })}
                        </ul>
                      </ProfileListState>
                    </div>
                  </section>
                )}
              </div>
            </div>
            <div className="cc-agent-manage-actions">
              <button type="button" className="oc-btn oc-btn-default" onClick={onBack}>返回</button>
            </div>
          </div>
        </div>
      </div>
      {mobileScanOpen && (
        <MobileChannelBindModal agentUid={agentId} agentName={name} onClose={() => setMobileScanOpen(false)} stacked />
      )}
    </div>
  );
}
