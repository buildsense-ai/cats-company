import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { api, getWebSocketURL } from '../api';
import t from '../i18n';
import {
  Bot,
  Bug,
  CheckCircle,
  ChevronDown,
  Code2,
  Copy,
  FileCheck2,
  Lightbulb,
  QrCode,
  RefreshCw,
  Sparkles,
  Upload,
  X,
  XCircle,
  Zap,
} from 'lucide-react';
import Avatar from './avatar';
import QRCode from './qr-code';
import { InlineFeedback, useFeedback } from '../components/feedback-system';
import { IMAGE_UPLOAD_ACCEPT, validateImageUpload } from '../utils/upload-rules';

const CREATE_MODES = {
  SELF_HOSTED: 'self_hosted',
  MANAGED: 'managed',
};

const CHANNEL_AGENT_ACCESS_MODES = {
  APPROVAL_REQUIRED: 'approval_required',
  PUBLIC: 'public',
};

const BOT_VISIBILITY = {
  PUBLIC: 'public',
  PRIVATE: 'private',
};

const CHANNEL_OPTIONS = [
  { value: 'weixin', label: '微信公众号', shortLabel: '公众号' },
  { value: 'feishu', label: '飞书', shortLabel: '飞书' },
  { value: 'weixin_clawbot', label: '微信 ClawBot', shortLabel: 'ClawBot' },
];

const normalizeChannelAgentAccessMode = (value) => (
  value === CHANNEL_AGENT_ACCESS_MODES.PUBLIC
    ? CHANNEL_AGENT_ACCESS_MODES.PUBLIC
    : CHANNEL_AGENT_ACCESS_MODES.APPROVAL_REQUIRED
);

const normalizeChannel = (value) => {
  const normalized = String(value || '').trim().toLowerCase();
  const safe = normalized.replace(/[-\s]+/g, '_');
  if (['wechat', 'weixin_mp', 'wechat_mp', 'weixin_official', 'wechat_official', 'weixin_official_account', 'wechat_official_account'].includes(safe)) return 'weixin';
  if (['clawbot', 'weixinclawbot', 'wechatclawbot', 'weixin_clawbot', 'wechat_clawbot'].includes(safe)) return 'weixin_clawbot';
  if (safe === 'lark') return 'feishu';
  return safe;
};

const channelLabel = (value) => (
  CHANNEL_OPTIONS.find((item) => item.value === normalizeChannel(value))?.label
  || value
  || '渠道'
);

const isManagedChannel = (value) => ['feishu', 'weixin', 'weixin_clawbot'].includes(normalizeChannel(value));

const isWeixinOfficialChannel = (value) => normalizeChannel(value) === 'weixin';

const isWeixinClawBotChannel = (value) => normalizeChannel(value) === 'weixin_clawbot';

const isFeishuChannel = (value) => normalizeChannel(value) === 'feishu';

const initialForm = {
  display_name: '',
  role: 'code_review',
  description: '',
};

const ASSISTANT_ROLES = [
  { value: 'code_review', label: '代码审查助手' },
  { value: 'debugging', label: '问题排查助手' },
  { value: 'writing', label: '写作助手' },
  { value: 'research', label: '研究助手' },
  { value: 'general', label: '通用助手' },
];

const ASSISTANT_CAPABILITIES = [
  { icon: Code2, title: '阅读代码', detail: '理解代码结构与逻辑，快速定位功能' },
  { icon: Bug, title: '分析 Bug', detail: '识别潜在问题，分析原因与影响' },
  { icon: Lightbulb, title: '优化建议', detail: '提供可行的优化建议与最佳实践' },
  { icon: FileCheck2, title: '生成方案', detail: '整理可执行的修改方案与代码片段' },
];

const isOwnedBot = (bot) => bot?.is_owner === true || bot?.relation === 'owner';

const normalizeBotVisibility = (visibility) => (
  visibility === BOT_VISIBILITY.PRIVATE ? BOT_VISIBILITY.PRIVATE : BOT_VISIBILITY.PUBLIC
);

const botVisibilityLabel = (visibility) => (
  normalizeBotVisibility(visibility) === BOT_VISIBILITY.PRIVATE ? '私有不可搜索' : '公开可搜索'
);

const botVisibilityDescription = (visibility) => (
  normalizeBotVisibility(visibility) === BOT_VISIBILITY.PRIVATE
    ? '不会出现在添加好友搜索里，也不能通过 UID 直接申请；已授权用户会保留，可在访问管理里移除。'
    : '别人可以通过名字或 UID 搜索并申请添加。'
);

export default function AgentStoreModal({ onClose, user, onBotsChanged }) {
  const feedback = useFeedback();
  const [bots, setBots] = useState([]);
  const [loading, setLoading] = useState(true);
  const [tab, setTab] = useState('hub'); // 'hub', 'create', 'manage'
  const [createForm, setCreateForm] = useState(initialForm);
  const [createMode, setCreateMode] = useState(CREATE_MODES.SELF_HOSTED);
  const [isSubmitting, setIsSubmitting] = useState(false);
  const [error, setError] = useState('');
  const [createdBot, setCreatedBot] = useState(null);
  const [createdMode, setCreatedMode] = useState(CREATE_MODES.SELF_HOSTED);
  const [copiedField, setCopiedField] = useState('');
  const [copyingBotKey, setCopyingBotKey] = useState(null);
  const [editingBot, setEditingBot] = useState(null);
  const [entryBot, setEntryBot] = useState(null);
  const avatarFileRef = useRef(null);
  const [avatarUploading, setAvatarUploading] = useState(false);

  useEffect(() => { loadBots(); }, []);

  const loadBots = async ({ silent = false } = {}) => {
    try {
      if (!silent) setLoading(true);
      const [botsRes, agentsRes, friendsRes] = await Promise.all([
        api.getMyBots().catch((err) => {
          throw err;
        }),
        api.getAgents ? api.getAgents().catch(() => ({})) : Promise.resolve({}),
        api.getFriends ? api.getFriends().catch(() => ({})) : Promise.resolve({}),
      ]);
      setBots(mergeManageableBots(botsRes.bots || [], agentsRes.agents || [], friendsRes.friends || []).filter(isOwnedBot));
    } catch (e) {
      console.error('Load bots error:', e);
    } finally {
      if (!silent) setLoading(false);
    }
  };

  const handleCreate = async (e) => {
    e.preventDefault();
    const displayName = createForm.display_name.trim();
    if (!displayName) {
      setError(t('bot_create_name_required'));
      return;
    }

    const slug = displayName.trim().toLowerCase().replace(/\s+/g, '-').replace(/[^a-z0-9-]/g, '').slice(0, 16);
    const suffix = Math.floor(Math.random() * 9000) + 1000;
    const username = `bot-${slug || 'bot'}-${suffix}`;
    const isManaged = createMode === CREATE_MODES.MANAGED;

    try {
      setError('');
      setCreatedBot(null);
      setIsSubmitting(true);

      const result = await api.createBot({ username, display_name: displayName }, isManaged);
      const fullResult = { ...result, id: result.uid, display_name: displayName, visibility: 'public' };

      // [CRITICAL HANDSHAKE]: Automatically force a bidirectional subscription so the bot 
      // instantly appears in both sides' Contact lists, avoiding ghost P2P topics.
      if (!isManaged && fullResult.api_key && user?.uid) {
        try {
          await api.sendFriendRequest(fullResult.uid);
          await api.acceptFriendAsBot(fullResult.api_key, user.uid);
          console.log('[Agent Handshake] Instantly bound P2P topic for developer testing.');
        } catch (handshakeErr) {
          console.warn('[Agent Handshake Failed]:', handshakeErr);
        }
      }

      setCreatedBot(fullResult);
      setCreatedMode(createMode);
      setTab('success');

      await loadBots({ silent: true });
      if (onBotsChanged) onBotsChanged();
    } catch (e) {
      setError(e.message || t('error_server'));
    } finally {
      setIsSubmitting(false);
    }
  };

  const handleCopy = async (field, value) => {
    try {
      await navigator.clipboard.writeText(value);
      setCopiedField(field);
      setTimeout(() => setCopiedField(''), 2000);
    } catch (e) {
      console.error('Copy failed:', e);
    }
  };

  const handleCopyBotAPIKey = async (bot, field = 'api_edit') => {
    const botId = bot?.id || bot?.uid;
    if (!botId) return;

    try {
      setError('');
      setCopyingBotKey(botId);

      let apiKey = bot.api_key;
      if (!apiKey) {
        const result = await api.getBotAPIKey(botId);
        apiKey = result.api_key;
      }
      if (!apiKey) throw new Error('API Key not found');

      setBots(prev => prev.map(item => item.id === botId ? { ...item, api_key: apiKey } : item));
      setEditingBot(prev => prev && (prev.id === botId || prev.uid === botId) ? { ...prev, api_key: apiKey } : prev);
      await handleCopy(field, apiKey);
    } catch (e) {
      setError(e.message || 'Failed to copy API Key');
    } finally {
      setCopyingBotKey(null);
    }
  };

  const handleDelete = async (bot) => {
    const botId = bot?.id || bot?.uid;
    if (!botId) return;
    const owned = isOwnedBot(bot);
    const confirmed = await feedback.confirm({
      title: owned ? `永久删除“${bot.display_name}”？` : `移除“${bot.display_name}”？`,
      message: owned
        ? '该虚拟员工及其相关配置会被永久删除，且无法恢复。'
        : '这只会解除好友关系，不会删除对方创建的虚拟员工。',
      confirmLabel: owned ? '永久删除' : '移除',
      tone: 'danger',
    });
    if (!confirmed) return;
    try {
      if (owned) {
        await api.deleteBot(botId);
      } else {
        await api.removeFriend(botId);
      }
      await loadBots({ silent: true });
      if (onBotsChanged) onBotsChanged();
      setTab('hub');
      feedback.notify({ tone: 'success', message: owned ? '虚拟员工已删除' : '助手已移除' });
    } catch (e) {
      setError(e.message || t('error_server'));
    }
  };

  const handleSaveEdit = async (e) => {
    e.preventDefault();
    if (!editingBot) return;
    if (!isOwnedBot(editingBot)) {
      setError('只能管理自己创建的助手');
      return;
    }
    try {
      await api.updateBot(editingBot.id, {
        display_name: editingBot.newDisplayName,
        avatar_url: editingBot.newAvatarUrl,
      });
      await loadBots({ silent: true });
      if (onBotsChanged) onBotsChanged();
      setEditingBot(null);
      setTab('hub');
    } catch (e) {
      setError(e.message || t('error_server'));
    }
  };

  const handleSetVisibility = async (bot, visibility) => {
    const botId = bot?.id || bot?.uid;
    if (!botId || !isOwnedBot(bot)) return;
    const nextVisibility = normalizeBotVisibility(visibility);
    try {
      setError('');
      await api.setBotVisibility(botId, nextVisibility);
      setBots(prev => prev.map(item => (
        (item.id === botId || item.uid === botId)
          ? { ...item, visibility: nextVisibility }
          : item
      )));
      setEditingBot(prev => (
        prev && (prev.id === botId || prev.uid === botId)
          ? { ...prev, visibility: nextVisibility }
          : prev
      ));
      if (onBotsChanged) onBotsChanged();
    } catch (e) {
      setError(e.message || '更新助手可见性失败');
    }
  };

  const wsUrl = getWebSocketURL();

  return (
    <div className="oc-modal-overlay" onClick={onClose} style={{ zIndex: 1000 }}>
      {/* Removed arbitrary background hardcoding to allow inheritance from the global .oc-modal V3 matrix */}
      <div className="oc-modal cc-agent-manager" onClick={e => e.stopPropagation()}>

        <div className="oc-modal-header cc-agent-manager-header">
          <div className="cc-agent-manager-nav">
            <h3 className="cc-agent-manager-title">
              <Zap size={17} /> AI 助手管理
            </h3>
            <div className="cc-agent-manager-tabs">
              <button
                className={tab === 'hub' ? 'active' : ''}
                onClick={() => setTab('hub')}
              >
                我创建的助手
              </button>
              <button
                className={tab === 'create' ? 'active' : ''}
                onClick={() => setTab('create')}
              >
                创建新助手
              </button>
            </div>
          </div>
          <button className="cc-dialog-close" onClick={onClose} aria-label="关闭"><X size={18} /></button>
        </div>

        <div className="oc-modal-body cc-agent-manager-body">

          {/* HUB TAB */}
          {tab === 'hub' && (
            <>
              {loading ? (
                <div style={{ padding: 40, textAlign: 'center', color: 'var(--v3-text-muted)' }}>加载中...</div>
              ) : bots.length === 0 ? (
                <div style={{ padding: 60, textAlign: 'center', display: 'flex', flexDirection: 'column', alignItems: 'center', gap: 16 }}>
                  <div style={{ color: 'var(--v3-text-muted)' }}><Bot size={48} strokeWidth={1.5} /></div>
                  <div style={{ color: 'var(--v3-text-main)' }}>还没有你创建的 AI 助手</div>
                  <div style={{ color: 'var(--v3-text-muted)', fontSize: 13, maxWidth: 280 }}>
                    已添加的助手会保留在左侧 AI 助手列表，可直接移动端使用或移除。
                  </div>
                  <button className="oc-btn cc-agent-empty-action" onClick={() => setTab('create')}>创建第一个助手</button>
                </div>
              ) : (
                <div className="v3-agent-grid" style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fill, minmax(280px, 1fr))', gap: 16 }}>
                  {bots.map(bot => {
                    const botId = bot.id || bot.uid;
                    const owned = isOwnedBot(bot);
                    return (
                    <div key={botId} className="v3-agent-card" style={{ background: 'var(--v3-bg-app)', border: '1px solid var(--v3-border)', padding: 16, borderRadius: 12 }}>
                      <div className="v3-agent-header" style={{ display: 'flex', alignItems: 'center', gap: 12 }}>
                        <div className="v3-agent-avatar" style={{ width: 48, height: 48, borderRadius: 8, background: 'var(--v3-bg-sidebar)', display: 'flex', alignItems: 'center', justifyContent: 'center', fontSize: 20, color: 'var(--v3-primary)' }}>
                          {(bot.display_name || bot.username || '?').charAt(0).toUpperCase()}
                        </div>
                        <div className="v3-agent-info" style={{ flex: 1, minWidth: 0 }}>
                          <h4 style={{ margin: '0 0 4px 0', fontSize: 16, color: 'var(--v3-text-name)' }}>{bot.display_name}</h4>
                          <span style={{ fontSize: 13, color: 'var(--v3-text-muted)' }}>@{bot.username} · uid {botId}</span>
                        </div>
                      </div>
                      <div style={{ fontSize: 12, color: 'var(--v3-text-muted)', marginBottom: 16, marginTop: 12 }}>
                        {owned ? (bot.tenant_name ? '我创建的 · 云托管' : '我创建的 · 自托管') : '已添加的助手'}
                      </div>
                      {owned && (
                        <div className={`v3-agent-visibility-badge ${normalizeBotVisibility(bot.visibility) === BOT_VISIBILITY.PRIVATE ? 'private' : 'public'}`}>
                          {botVisibilityLabel(bot.visibility)}
                        </div>
                      )}
                      <div className="v3-agent-actions" style={{ display: 'flex', gap: 8 }}>
                        {owned && (
                        <button className="oc-btn oc-btn-default" style={{ flex: 1, padding: '8px 0', borderRadius: 8 }} onClick={() => {
                          setEditingBot({ ...bot, newDisplayName: bot.display_name, newAvatarUrl: bot.avatar_url || '' });
                          setTab('manage');
                        }}>
                          管理
                        </button>
                        )}
                        {owned && (
                        <button
                          className="oc-btn oc-btn-default"
                          style={{ padding: '8px 12px', borderRadius: 8, display: 'flex', alignItems: 'center', gap: 6 }}
                          onClick={() => setEntryBot(bot)}
                          title="入口码"
                        >
                          <QrCode size={14} />
                          入口码
                        </button>
                        )}
                        {owned && !bot.tenant_name && (
                          <button
                            className="oc-btn oc-btn-default"
                            style={{ padding: '8px 12px', borderRadius: 8 }}
                            onClick={() => handleCopyBotAPIKey(bot, `api_${botId}`)}
                            disabled={copyingBotKey === botId}
                          >
                            {copiedField === `api_${botId}` ? '已复制' : copyingBotKey === botId ? '加载中...' : '复制 Key'}
                          </button>
                        )}
                        <button className="oc-btn oc-btn-default" style={{ padding: '8px 16px', borderRadius: 8, borderColor: 'rgba(250,81,81,0.3)' }} onClick={() => handleDelete(bot)}>
                          <span style={{ color: '#FA5151' }}>删除</span>
                        </button>
                      </div>
                    </div>
                    );
                  })}
                </div>
              )}
            </>
          )}

          {/* CREATE TAB */}
          {tab === 'create' && (
            <form onSubmit={handleCreate} className="cc-agent-create-form">
              <div className="cc-agent-create-intro">
                <h2>创建你的专属助手</h2>
                <p>先定义助手身份，再配置运行方式。</p>
              </div>

              <div className="cc-agent-create-grid">
                <section className="cc-agent-create-card cc-agent-basic-card">
                  <h3><FileCheck2 size={17} />基本信息</h3>
                  <label>
                    <span>助手名称 <b>*</b></span>
                    <input
                      type="text"
                      value={createForm.display_name}
                      onChange={(e) => setCreateForm({ ...createForm, display_name: e.target.value })}
                      placeholder="例如：代码审查助手"
                      className="oc-auth-input"
                      required
                      disabled={isSubmitting}
                    />
                  </label>
                  <label>
                    <span>助手定位 <b>*</b></span>
                    <div className="cc-agent-role-field">
                      <select
                        value={createForm.role}
                        onChange={(e) => setCreateForm({ ...createForm, role: e.target.value })}
                        className="oc-auth-input cc-agent-role-select"
                      >
                        {ASSISTANT_ROLES.map((role) => <option key={role.value} value={role.value}>{role.label}</option>)}
                      </select>
                      <ChevronDown className="cc-agent-role-chevron" size={15} aria-hidden="true" />
                    </div>
                  </label>
                  <label>
                    <span>用途说明 <small>选填</small></span>
                    <textarea
                      value={createForm.description}
                      onChange={(e) => setCreateForm({ ...createForm, description: e.target.value.slice(0, 500) })}
                      placeholder="说明这个助手解决什么问题，以及你希望它如何工作"
                    />
                    <em>{createForm.description.length}/500</em>
                  </label>
                </section>

                <section className="cc-agent-create-card cc-agent-capability-card">
                  <h3><Sparkles size={17} />能力预览</h3>
                  <div className="cc-agent-capability-list">
                    {ASSISTANT_CAPABILITIES.map(({ icon: Icon, title, detail }) => (
                      <div className="cc-agent-capability-item" key={title}>
                        <span><Icon size={18} /></span>
                        <div><strong>{title}</strong><small>{detail}</small></div>
                      </div>
                    ))}
                  </div>
                </section>
              </div>

              <fieldset className="cc-agent-hosting">
                <legend><span><Zap size={16} /></span>部署方式 <small>高级设置</small></legend>
                <label className={createMode === CREATE_MODES.SELF_HOSTED ? 'active' : ''}>
                  <input type="radio" name="hosting" checked={createMode === CREATE_MODES.SELF_HOSTED} onChange={() => setCreateMode(CREATE_MODES.SELF_HOSTED)} />
                  <span><strong>自托管</strong><small>生成本地身份 Key，后续连接你的服务。</small></span>
                </label>
                <label className="disabled">
                  <input type="radio" name="hosting" disabled />
                  <span><strong>云托管</strong><small>无需部署，创建后直接使用，即将推出。</small></span>
                </label>
              </fieldset>

              <button type="submit" className="oc-btn oc-btn-primary cc-agent-create-submit" disabled={isSubmitting}>
                {isSubmitting ? '创建中...' : '创建我的专属助手'}
              </button>
            </form>
          )}

          {/* SUCCESS (API KEY) TAB */}
          {tab === 'success' && createdBot && (
            <div style={{ maxWidth: 460, margin: '0 auto', textAlign: 'center' }}>
              <div style={{ width: 64, height: 64, background: 'rgba(16, 185, 129, 0.1)', color: 'var(--v3-primary)', borderRadius: '50%', display: 'flex', alignItems: 'center', justifyContent: 'center', fontSize: 32, margin: '0 auto 20px' }}>✓</div>
              <h2 style={{ margin: '0 0 8px 0', color: 'var(--v3-text-name)' }}>创建成功</h2>
              <p style={{ margin: '0 0 24px 0', color: 'var(--v3-text-muted)', fontSize: 14 }}>AI 助手 <b style={{color: 'var(--v3-text-name)'}}>{createdBot.display_name}</b> 已准备好连接。</p>

              <div style={{ textAlign: 'left', background: 'var(--v3-bg-app)', border: '1px solid var(--v3-border)', borderRadius: 8, padding: 16, marginBottom: 16 }}>
                <div style={{ fontSize: 11, color: 'var(--v3-text-muted)', marginBottom: 8, letterSpacing: 0.5 }}>API KEY</div>
                <div style={{ display: 'flex', gap: 8 }}>
                  <code style={{ flex: 1, background: '#111', padding: '10px 12px', borderRadius: 6, color: 'var(--v3-primary)', fontFamily: 'monospace', fontSize: 13, userSelect: 'all' }}>
                    {createdBot.api_key}
                  </code>
                  <button className="oc-btn oc-btn-default" onClick={() => handleCopy('api', createdBot.api_key)}>
                    {copiedField === 'api' ? '已复制' : '复制'}
                  </button>
                </div>
              </div>

              <div style={{ textAlign: 'left', background: 'var(--v3-bg-app)', border: '1px solid var(--v3-border)', borderRadius: 8, padding: 16, marginBottom: 24 }}>
                <div style={{ fontSize: 11, color: 'var(--v3-text-muted)', marginBottom: 8, letterSpacing: 0.5 }}>WebSocket 连接地址</div>
                <div style={{ display: 'flex', gap: 8 }}>
                  <code style={{ flex: 1, background: '#111', padding: '10px 12px', borderRadius: 6, color: 'var(--v3-text-main)', fontFamily: 'monospace', fontSize: 13, userSelect: 'all' }}>
                    {wsUrl}
                  </code>
                  <button className="oc-btn oc-btn-default" onClick={() => handleCopy('ws', wsUrl)}>
                    {copiedField === 'ws' ? '已复制' : '复制'}
                  </button>
                </div>
              </div>

              <button className="oc-btn oc-btn-default" style={{ width: '100%', padding: '12px 0', borderRadius: 8 }} onClick={() => setTab('hub')}>
                返回列表
              </button>
            </div>
          )}

          {/* MANAGE / EDIT TAB */}
          {tab === 'manage' && editingBot && (
            <form onSubmit={handleSaveEdit} style={{ maxWidth: 460, margin: '0 auto' }}>
              <h2 style={{ margin: '0 0 24px 0', fontSize: 20, color: 'var(--v3-text-name)' }}>管理助手</h2>

              <div className="oc-form-group" style={{ marginBottom: 16 }}>
                <label style={{ display: 'block', marginBottom: 8, fontSize: 13, color: 'var(--v3-text-muted)' }}>名称</label>
                <input
                  type="text"
                  value={editingBot.newDisplayName}
                  onChange={(e) => setEditingBot({ ...editingBot, newDisplayName: e.target.value })}
                  className="oc-auth-input"
                  style={{ width: '100%', padding: '12px 16px', fontSize: 15 }}
                  required
                />
              </div>

              <div className="oc-form-group" style={{ marginBottom: 24 }}>
                <label style={{ display: 'block', marginBottom: 8, fontSize: 13, color: 'var(--v3-text-muted)' }}>头像</label>
                <div style={{ display: 'flex', alignItems: 'center', gap: 16 }}>
                  <Avatar
                    name={editingBot.newDisplayName || editingBot.display_name}
                    src={editingBot.newAvatarUrl}
                    size={64}
                    isBot
                  />
                  <div style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
                    <button
                      type="button"
                      className="oc-btn oc-btn-default"
                      style={{ padding: '8px 16px', borderRadius: 8, display: 'flex', alignItems: 'center', gap: 8 }}
                      onClick={() => avatarFileRef.current?.click()}
                      disabled={avatarUploading}
                    >
                      <Upload size={14} />
                      {avatarUploading ? '上传中...' : '选择头像'}
                    </button>
                    {editingBot.newAvatarUrl && (
                      <button
                        type="button"
                        style={{ background: 'none', border: 'none', color: 'var(--v3-text-muted)', fontSize: 12, cursor: 'pointer', textAlign: 'left', padding: 0 }}
                        onClick={() => setEditingBot({ ...editingBot, newAvatarUrl: '' })}
                      >
                        移除头像
                      </button>
                    )}
                  </div>
                  <input
                    ref={avatarFileRef}
                    type="file"
                    accept={IMAGE_UPLOAD_ACCEPT}
                    style={{ display: 'none' }}
                    onChange={async (event) => {
                      const file = event.target.files?.[0];
                      if (!file) return;
                      const validationError = validateImageUpload(file);
                      if (validationError) {
                        setError(validationError);
                        event.target.value = '';
                        return;
                      }
                      setAvatarUploading(true);
                      setError('');
                      try {
                        const uploaded = await api.uploadFile(file, 'image');
                        setEditingBot(prev => ({ ...prev, newAvatarUrl: uploaded.url || '' }));
                      } catch (err) {
                        setError(err.message || 'Avatar upload failed');
                      } finally {
                        setAvatarUploading(false);
                        event.target.value = '';
                      }
                    }}
                  />
                </div>
              </div>

              {!editingBot.tenant_name && (
                <div style={{ background: 'var(--v3-bg-app)', border: '1px solid var(--v3-border)', borderRadius: 8, padding: 16, marginBottom: 24 }}>
                  <div style={{ fontSize: 11, color: 'var(--v3-text-muted)', marginBottom: 8, letterSpacing: 0.5 }}>API Key</div>
                  <div style={{ display: 'flex', gap: 8, marginBottom: 16 }}>
                    <code style={{ flex: 1, background: '#111', padding: '10px 12px', borderRadius: 6, color: editingBot.api_key ? 'var(--v3-primary)' : 'var(--v3-text-muted)', fontFamily: 'monospace', fontSize: 13, userSelect: 'all' }}>
                      {editingBot.api_key || '点击复制加载 API Key'}
                    </code>
                    <button
                      type="button"
                      className="oc-btn oc-btn-default"
                      onClick={() => handleCopyBotAPIKey(editingBot, 'api_edit')}
                      disabled={copyingBotKey === editingBot.id}
                    >
                      {copiedField === 'api_edit' ? '已复制' : copyingBotKey === editingBot.id ? '加载中...' : '复制'}
                    </button>
                  </div>

                  <div style={{ fontSize: 11, color: 'var(--v3-text-muted)', marginBottom: 8, letterSpacing: 0.5 }}>WebSocket 连接地址</div>
                  <div style={{ display: 'flex', gap: 8 }}>
                    <code style={{ flex: 1, background: '#111', padding: '10px 12px', borderRadius: 6, color: 'var(--v3-text-main)', fontFamily: 'monospace', fontSize: 13, userSelect: 'all' }}>
                      {wsUrl}
                    </code>
                    <button type="button" className="oc-btn oc-btn-default" onClick={() => handleCopy('ws_edit', wsUrl)}>
                      {copiedField === 'ws_edit' ? '已复制' : '复制'}
                    </button>
                  </div>
                </div>
              )}

              <div style={{ background: 'var(--v3-bg-app)', border: '1px solid var(--v3-border)', borderRadius: 8, padding: 16, marginBottom: 24 }}>
                <div style={{ fontSize: 13, color: 'var(--v3-text-name)', fontWeight: 700, marginBottom: 8 }}>好友添加方式</div>
                <div style={{ color: 'var(--v3-text-muted)', fontSize: 12, lineHeight: 1.6, marginBottom: 12 }}>
                  当前：{botVisibilityLabel(editingBot.visibility)}。{botVisibilityDescription(editingBot.visibility)}
                </div>
                <div style={{ display: 'grid', gridTemplateColumns: 'repeat(2, minmax(0, 1fr))', gap: 8 }}>
                  <button
                    type="button"
                    className={`oc-btn ${normalizeBotVisibility(editingBot.visibility) === BOT_VISIBILITY.PUBLIC ? 'oc-btn-primary' : 'oc-btn-default'}`}
                    style={{ padding: '10px 0', borderRadius: 8 }}
                    onClick={() => handleSetVisibility(editingBot, BOT_VISIBILITY.PUBLIC)}
                  >
                    公开可搜索
                  </button>
                  <button
                    type="button"
                    className={`oc-btn ${normalizeBotVisibility(editingBot.visibility) === BOT_VISIBILITY.PRIVATE ? 'oc-btn-primary' : 'oc-btn-default'}`}
                    style={{ padding: '10px 0', borderRadius: 8 }}
                    onClick={() => handleSetVisibility(editingBot, BOT_VISIBILITY.PRIVATE)}
                  >
                    私有不可搜索
                  </button>
                </div>
              </div>

              <div style={{ display: 'flex', gap: 12 }}>
                <button type="button" className="oc-btn oc-btn-default" style={{ flex: 1, padding: '14px 0', borderRadius: 8 }} onClick={() => setTab('hub')}>
                  取消
                </button>
                <button type="button" className="oc-btn oc-btn-default" style={{ flex: 1, padding: '14px 0', borderRadius: 8 }} onClick={() => setEntryBot(editingBot)}>
                  入口码
                </button>
                <button type="submit" className="oc-btn oc-btn-primary" style={{ flex: 1, padding: '14px 0', borderRadius: 8 }}>
                  保存
                </button>
              </div>
            </form>
          )}
        </div>

        {error && <InlineFeedback tone="error" className="cc-agent-inline-feedback">{error}</InlineFeedback>}
      </div>
      {entryBot && isOwnedBot(entryBot) && (
        <AgentEntryModal
          bot={entryBot}
          onClose={() => setEntryBot(null)}
          onCopy={handleCopy}
          copiedField={copiedField}
          onAccessChanged={() => loadBots({ silent: true })}
        />
      )}
    </div>
  );
}

function mergeManageableBots(rawBots, rawAgents, rawFriends = []) {
  const byID = new Map();
  const add = (item, fallback = {}) => {
    const id = item?.id || item?.uid;
    if (!id) return;
    byID.set(String(id), {
      ...fallback,
      ...item,
      id,
      uid: item.uid || id,
      display_name: item.display_name || item.username || fallback.display_name || fallback.username || '未命名助手',
      username: item.username || fallback.username || `agent-${id}`,
      is_bot: true,
    });
  };

  rawBots.forEach((bot) => add(bot));
  rawAgents
    .filter((agent) => agent && (agent.is_bot === true || agent.relation === 'friend' || agent.relation === 'owner'))
    .forEach((agent) => {
      const id = agent.uid || agent.id;
      if (!id || byID.has(String(id))) return;
      add(agent, {
        relation: agent.relation || 'friend',
        is_owner: agent.relation === 'owner',
        visibility: agent.visibility || 'friend',
      });
    });
  rawFriends
    .filter((friend) => friend && (friend.bot === true || friend.is_bot === true || friend.account_type === 'bot' || friend.accountType === 'bot'))
    .forEach((friend) => {
      const id = friend.uid || friend.id;
      if (!id || byID.has(String(id))) return;
      add({
        id,
        uid: id,
        username: friend.username,
        display_name: friend.display_name,
        avatar_url: friend.avatar_url,
        relation: 'friend',
        is_owner: false,
        visibility: 'friend',
        is_bot: true,
        is_online: friend.is_online || friend.online || false,
      });
    });

  return Array.from(byID.values()).sort((a, b) => {
    const leftOwned = isOwnedBot(a);
    const rightOwned = isOwnedBot(b);
    if (leftOwned !== rightOwned) return leftOwned ? -1 : 1;
    return String(a.display_name || '').localeCompare(String(b.display_name || ''));
  });
}

function AgentEntryModal({ bot, onClose, onCopy, copiedField, onAccessChanged }) {
  const feedback = useFeedback();
  const [channel, setChannel] = useState('weixin');
  const [channelAppIds, setChannelAppIds] = useState({ weixin: '', feishu: '', weixin_clawbot: '' });
  const [entries, setEntries] = useState([]);
  const [accessMode, setAccessMode] = useState(CHANNEL_AGENT_ACCESS_MODES.APPROVAL_REQUIRED);
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState('');
  const [qrImageError, setQrImageError] = useState(false);
  const [pendingRequests, setPendingRequests] = useState([]);
  const [botFriends, setBotFriends] = useState([]);
  const [managedBindings, setManagedBindings] = useState([]);
  const [accessTab, setAccessTab] = useState('pending');
  const [pendingLoading, setPendingLoading] = useState(false);
  const [reviewingUID, setReviewingUID] = useState(null);
  const [removingFriendUID, setRemovingFriendUID] = useState(null);
  const [clawBotMobileLink, setClawBotMobileLink] = useState(null);
  const [clawBotMobileLoading, setClawBotMobileLoading] = useState(false);
  const [clawBotMobileError, setClawBotMobileError] = useState('');
  const [clawBotAuthStatus, setClawBotAuthStatus] = useState(null);
  const clawBotMobileRequestRef = useRef(0);
  const clawBotMobileMountedRef = useRef(false);
  const botId = bot?.id || bot?.uid;

  useEffect(() => {
    let cancelled = false;
    setLoading(true);
    setError('');
    api.getAgentEntries(botId)
      .then((res) => {
        if (!cancelled) setEntries(res.entries || []);
      })
      .catch((err) => {
        if (!cancelled) setError(err.message || 'Failed to load entry codes');
      })
      .finally(() => {
        if (!cancelled) setLoading(false);
      });
    return () => {
      cancelled = true;
    };
  }, [botId]);

  const loadPendingRequests = useCallback(async () => {
    if (!botId) return;
    try {
      setPendingLoading(true);
      const [pendingRes, bindingsRes, friendsRes] = await Promise.all([
        api.getPendingRequests(botId),
        api.getAgentChannelBindings(botId),
        api.getBotFriends(botId),
      ]);
      setPendingRequests(pendingRes.requests || []);
      setManagedBindings(bindingsRes.bindings || []);
      setBotFriends(friendsRes.friends || []);
    } catch (err) {
      console.warn('load agent pending requests:', err);
      setPendingRequests([]);
      setManagedBindings([]);
      setBotFriends([]);
    } finally {
      setPendingLoading(false);
    }
  }, [botId]);

  useEffect(() => {
    loadPendingRequests();
  }, [loadPendingRequests]);

  const channelAppId = channelAppIds[channel] || '';
  const managedChannelAppID = isManagedChannel(channel);
  const normalizedChannelAppId = managedChannelAppID ? '' : channelAppId.trim();
  const entryScopeMatches = (entry, targetChannel = channel, targetAppId = normalizedChannelAppId) => (
    normalizeChannel(entry.channel) === normalizeChannel(targetChannel)
    && (isManagedChannel(targetChannel) || (entry.channel_app_id || '') === targetAppId)
  );
  const selected = entries.find((entry) => (
    entryScopeMatches(entry)
    && normalizeChannelAgentAccessMode(entry.access_mode) === accessMode
  ));
  const selectedEntryID = selected?.id || '';
  const entryUrl = selected?.entry_url || '';
  const channelQrUrl = selected?.channel_qr_url || '';
  const qrValue = selected?.qr_value || '';
  const qrKind = selected?.qr_kind || '';
  const feishuOAuthUrl = selected?.feishu_oauth_url || '';
  const feishuEntryStatus = selected?.feishu_entry_status || null;
  const clawBotEntryStatus = selected?.clawbot_entry_status || null;
  const feishuEntryReasons = Array.isArray(feishuEntryStatus?.reasons) ? feishuEntryStatus.reasons : [];
  const clawBotEntryReasons = Array.isArray(clawBotEntryStatus?.reasons) ? clawBotEntryStatus.reasons : [];
  const isFeishuOAuthEntry = isFeishuChannel(channel) && qrKind === 'feishu_oauth_entry' && qrValue;
  const isFeishuNativeEntry = isFeishuChannel(channel) && qrKind === 'feishu_native_entry' && qrValue;
  const hasFeishuEntryQRCode = isFeishuOAuthEntry || isFeishuNativeEntry;
  const isClawBotEntry = isWeixinClawBotChannel(channel) && qrKind === 'weixin_clawbot_ilink_qr' && qrValue;
  const displayQrUrl = isWeixinOfficialChannel(channel) && channelQrUrl ? channelQrUrl : '';
  const displayUrl = displayQrUrl || (hasFeishuEntryQRCode ? qrValue : isClawBotEntry ? qrValue : (isFeishuChannel(channel) || isWeixinClawBotChannel(channel) ? '' : qrValue || entryUrl));
  const usesLocalEntryUrl = isPotentiallyPrivateEntryUrl(displayUrl);
  const needsWeixinConfig = isWeixinOfficialChannel(channel) && selected && !displayQrUrl;
  const needsFeishuNativeConfig = isFeishuChannel(channel) && selected && !hasFeishuEntryQRCode;
  const needsClawBotConfig = isWeixinClawBotChannel(channel) && selected && !isClawBotEntry;
  const clawBotMobileStatus = clawBotMobileLink?.entry?.clawbot_entry_status || null;
  const clawBotMobileReasons = Array.isArray(clawBotMobileStatus?.reasons)
    ? clawBotMobileStatus.reasons
    : clawBotEntryReasons;
  const clawBotMobileQrKind = clawBotMobileLink?.qr_kind || clawBotMobileLink?.entry?.qr_kind || '';
  const clawBotMobileQRValue = clawBotMobileQrKind === 'weixin_clawbot_ilink_qr'
    ? (clawBotMobileLink?.qr_value || clawBotMobileLink?.channel_qr_url || '')
    : '';
  const clawBotMobileQRCode = clawBotMobileStatus?.qrcode || '';
  const clawBotMobileSceneKey = clawBotMobileLink?.scene_key || '';

  useEffect(() => {
    setQrImageError(false);
  }, [displayQrUrl]);

  useEffect(() => {
    clawBotMobileMountedRef.current = true;
    return () => {
      clawBotMobileMountedRef.current = false;
      clawBotMobileRequestRef.current += 1;
    };
  }, []);

  useEffect(() => {
    clawBotMobileRequestRef.current += 1;
    setClawBotMobileLink(null);
    setClawBotMobileLoading(false);
    setClawBotMobileError('');
    setClawBotAuthStatus(null);
  }, [botId, channel, accessMode, selectedEntryID]);

  const loadClawBotMobileLink = useCallback(async () => {
    if (!botId || !clawBotMobileMountedRef.current) return;
    const requestSeq = clawBotMobileRequestRef.current + 1;
    clawBotMobileRequestRef.current = requestSeq;
    try {
      setClawBotMobileLoading(true);
      setClawBotMobileError('');
      setClawBotAuthStatus(null);
      setClawBotMobileLink(null);
      const res = await api.createChannelIdentityMobileLink(botId, 'weixin_clawbot', selectedEntryID);
      if (clawBotMobileRequestRef.current !== requestSeq || !clawBotMobileMountedRef.current) return;
      setClawBotMobileLink(res);
    } catch (err) {
      if (clawBotMobileRequestRef.current !== requestSeq || !clawBotMobileMountedRef.current) return;
      setClawBotMobileLink(null);
      setClawBotMobileError(err.message || '暂时无法生成微信 ClawBot 授权码');
    } finally {
      if (clawBotMobileRequestRef.current === requestSeq && clawBotMobileMountedRef.current) {
        setClawBotMobileLoading(false);
      }
    }
  }, [botId, selectedEntryID]);

  useEffect(() => {
    if (!needsClawBotConfig || !selectedEntryID || clawBotMobileLink || clawBotMobileLoading || clawBotMobileError) {
      return;
    }
    loadClawBotMobileLink();
  }, [needsClawBotConfig, selectedEntryID, clawBotMobileLink, clawBotMobileLoading, clawBotMobileError, loadClawBotMobileLink]);

  useEffect(() => {
    if (!needsClawBotConfig || !clawBotMobileSceneKey || !clawBotMobileQRCode || !clawBotMobileQRValue) return undefined;
    let cancelled = false;
    let timer = null;
    const poll = async () => {
      try {
        const res = await api.getWeixinClawBotQRCodeStatus(clawBotMobileSceneKey, clawBotMobileQRCode);
        if (cancelled) return;
        if (res?.token_saved) {
          setClawBotAuthStatus({ status: 'saved', target: res.target || 'agent' });
          return;
        }
        setClawBotAuthStatus({ status: res?.status || 'waiting' });
        timer = window.setTimeout(poll, 2000);
      } catch (err) {
        if (cancelled) return;
        setClawBotAuthStatus({ status: 'error', message: err.message || '授权状态检查失败' });
        timer = window.setTimeout(poll, 3000);
      }
    };
    poll();
    return () => {
      cancelled = true;
      if (timer) window.clearTimeout(timer);
    };
  }, [needsClawBotConfig, clawBotMobileSceneKey, clawBotMobileQRCode, clawBotMobileQRValue]);

  const handleGenerate = async () => {
    try {
      setSaving(true);
      setError('');
      const res = await api.createAgentEntry(botId, channel, normalizedChannelAppId, accessMode);
      const next = res.entry;
      setEntries((prev) => [next, ...prev.filter((entry) => (
        !(
          entryScopeMatches(entry, next.channel, isManagedChannel(next.channel) ? '' : (next.channel_app_id || ''))
          && normalizeChannelAgentAccessMode(entry.access_mode) === normalizeChannelAgentAccessMode(next.access_mode)
        )
      ))]);
      await loadPendingRequests();
    } catch (err) {
      setError(err.message || 'Failed to generate entry code');
    } finally {
      setSaving(false);
    }
  };

  const handleRegenerate = async () => {
    if (!selected) return;
    const confirmed = await feedback.confirm({
      title: '重新生成入口码？',
      message: '重新生成后，旧入口码会立即失效。',
      confirmLabel: '重新生成',
      tone: 'danger',
    });
    if (!confirmed) return;
    try {
      setSaving(true);
      setError('');
      const res = await api.regenerateAgentEntry(selected.id);
      const next = res.entry;
      setEntries((prev) => [next, ...prev.filter((entry) => (
        entry.id !== selected.id
        && !(
          entryScopeMatches(entry, next.channel, isManagedChannel(next.channel) ? '' : (next.channel_app_id || ''))
          && normalizeChannelAgentAccessMode(entry.access_mode) === normalizeChannelAgentAccessMode(next.access_mode)
        )
      ))]);
      feedback.notify({ tone: 'success', message: '入口码已重新生成' });
    } catch (err) {
      setError(err.message || 'Failed to regenerate entry code');
    } finally {
      setSaving(false);
    }
  };

  const handleReviewRequest = async (request, action) => {
    const fromUID = request?.from_user_id;
    if (!fromUID || !botId) return;
    try {
      setReviewingUID(fromUID);
      setError('');
      if (action === 'accept') {
        await api.acceptAgentFriend(botId, fromUID);
      } else {
        await api.rejectAgentFriend(botId, fromUID);
      }
      await loadPendingRequests();
    } catch (err) {
      setError(err.message || 'Failed to review friend request');
    } finally {
      setReviewingUID(null);
    }
  };

  const handleRemoveBotFriend = async (friend) => {
    const friendUID = friend?.id || friend?.uid;
    if (!friendUID || !botId) return;
    const name = friend.display_name || friend.username || `用户 ${friendUID}`;
    const confirmed = await feedback.confirm({
      title: `移除“${name}”的使用权限？`,
      message: '移除后，该用户将无法继续使用这个虚拟员工。',
      confirmLabel: '移除权限',
      tone: 'danger',
    });
    if (!confirmed) return;
    try {
      setRemovingFriendUID(friendUID);
      setError('');
      await api.removeBotFriend(botId, friendUID);
      await loadPendingRequests();
      if (onAccessChanged) onAccessChanged();
      feedback.notify({ tone: 'success', message: '使用权限已移除' });
    } catch (err) {
      setError(err.message || '移除使用者失败');
    } finally {
      setRemovingFriendUID(null);
    }
  };

  const bindingsByStatus = useMemo(() => {
    const buckets = { approved: [], rejected: [], needs_login: [], pending: [] };
    (managedBindings || []).filter((item) => (
      normalizeChannelAgentAccessMode(item.access_mode || item.entry_access_mode) === accessMode
      && normalizeChannel(item.channel || item.binding?.channel) === channel
      && (!selected?.id || Number(item.binding?.entry_id || item.entry_id || 0) === Number(selected.id))
    )).forEach((item) => {
      const status = item.status || item.binding?.status || '';
      if (status === 'approved' || status === 'active') buckets.approved.push(item);
      else if (status === 'rejected') buckets.rejected.push(item);
      else if (status === 'needs_login' || status === 'pending_login') buckets.needs_login.push(item);
      else if (status === 'pending' || status === 'pending_approval') buckets.pending.push(item);
    });
    return buckets;
  }, [managedBindings, accessMode, channel, selected]);

  useEffect(() => {
    if (
      accessTab === 'pending'
      && pendingRequests.length === 0
      && bindingsByStatus.pending.length === 0
      && (botFriends.length > 0 || bindingsByStatus.approved.length > 0)
    ) {
      setAccessTab('approved');
    }
  }, [accessTab, pendingRequests.length, bindingsByStatus.pending.length, botFriends.length, bindingsByStatus.approved.length]);

  const accessTabs = [
    ['pending', '待处理', pendingRequests.length + bindingsByStatus.pending.length],
    ['approved', '已授权', botFriends.length + bindingsByStatus.approved.length],
    ['rejected', '未通过', bindingsByStatus.rejected.length],
    ['needs_login', '待登录', bindingsByStatus.needs_login.length],
  ];

  return (
    <div className="oc-modal-overlay" onClick={onClose} style={{ zIndex: 1200 }}>
      <div className="oc-modal" onClick={e => e.stopPropagation()} style={{ width: 520, maxWidth: '94vw' }}>
        <div className="oc-modal-header" style={{ padding: '18px 22px', borderBottom: '1px solid var(--v3-border)' }}>
          <h3 style={{ margin: 0, fontSize: 17, color: 'var(--v3-text-name)', display: 'flex', alignItems: 'center', gap: 8 }}>
            <QrCode size={18} /> {bot.display_name} 入口码
          </h3>
          <button className="oc-btn-default" style={{ width: 28, height: 28, padding: 0, border: 'none', background: 'transparent' }} onClick={onClose}>×</button>
        </div>

        <div className="oc-modal-body" style={{ padding: 22 }}>
          <div style={{ display: 'flex', gap: 8, marginBottom: 18 }}>
            {CHANNEL_OPTIONS.map(({ value, label }) => (
              <button
                key={value}
                type="button"
                className={`oc-btn ${channel === value ? 'oc-btn-primary' : 'oc-btn-default'}`}
                style={{ flex: 1, padding: '9px 0', borderRadius: 8 }}
                onClick={() => setChannel(value)}
              >
                {label}
              </button>
            ))}
          </div>

          {!managedChannelAppID && (
            <div style={{ marginBottom: 16 }}>
              <label style={{ display: 'block', color: 'var(--v3-text-muted)', fontSize: 12, marginBottom: 8 }}>
                微信 AppID（可选）
              </label>
              <input
                value={channelAppId}
                onChange={(event) => setChannelAppIds((prev) => ({ ...prev, [channel]: event.target.value }))}
                className="oc-auth-input"
                placeholder="留空为通用入口码"
                style={{ width: '100%', padding: '10px 12px', fontSize: 13 }}
              />
            </div>
          )}

          <div style={{ marginBottom: 16 }}>
            <div style={{ color: 'var(--v3-text-muted)', fontSize: 12, marginBottom: 8 }}>访问方式</div>
            <div style={{ display: 'grid', gridTemplateColumns: 'repeat(2, minmax(0, 1fr))', gap: 8 }}>
              {[
                [CHANNEL_AGENT_ACCESS_MODES.APPROVAL_REQUIRED, '好友申请'],
                [CHANNEL_AGENT_ACCESS_MODES.PUBLIC, '公开访问'],
              ].map(([value, label]) => (
                <button
                  key={value}
                  type="button"
                  className={`oc-btn ${accessMode === value ? 'oc-btn-primary' : 'oc-btn-default'}`}
                  style={{ padding: '9px 0', borderRadius: 8 }}
                  onClick={() => setAccessMode(value)}
                >
                  {label}
                </button>
              ))}
            </div>
            <div style={{ color: 'var(--v3-text-muted)', fontSize: 12, lineHeight: 1.6, marginTop: 8 }}>
              {accessMode === CHANNEL_AGENT_ACCESS_MODES.PUBLIC
                ? '扫码后仍需登录 CatsCo 账号；账号验证通过后可直接对话，不需要管理员审批。设备操作只会使用申请人自己授权的设备。'
                : isFeishuChannel(channel)
                  ? '用户用飞书扫码后会打开该虚拟员工的飞书应用或机器人入口；首次进入会提交好友申请，通过并完成 CatsCo 账号绑定后，可直接在飞书对话并使用该账号已连接的设备。'
                  : isWeixinClawBotChannel(channel)
                    ? '用户用微信 ClawBot 扫码后会进入独立的 ClawBot 入口；首次进入会提交好友申请，通过后可直接在 ClawBot 对话。'
                    : '扫码后需要登录 CatsCo 并发送好友申请，通过后才能对话；设备操作只会使用申请人自己授权的设备。'}
            </div>
          </div>

          {error && <InlineFeedback tone="error" className="cc-agent-entry-feedback">{error}</InlineFeedback>}

          {loading ? (
            <div style={{ padding: 40, textAlign: 'center', color: 'var(--v3-text-muted)' }}>正在读取入口码...</div>
          ) : selected && needsWeixinConfig ? (
            <div style={{ padding: 24, border: '1px dashed var(--v3-border)', borderRadius: 8 }}>
              <div style={{ color: 'var(--v3-text-name)', fontWeight: 700, marginBottom: 10 }}>微信公众号入口码尚不可用</div>
              <div style={{ color: 'var(--v3-text-muted)', fontSize: 13, lineHeight: 1.7, marginBottom: 14 }}>
                配置公众号 AppID、AppSecret 和服务器回调后，这里会显示可扫码关注并绑定虚拟员工的公众号参数二维码。
              </div>
              <div style={{ background: 'var(--v3-bg-app)', border: '1px solid var(--v3-border)', borderRadius: 8, padding: 10, color: 'var(--v3-text-main)', fontSize: 12, lineHeight: 1.6, marginBottom: 14 }}>
                公众号后台 URL：/api/channels/weixin/events<br />
                Token：CATSCO_WEIXIN_EVENT_TOKEN<br />
                消息加解密：明文或兼容模式
              </div>
              <div style={{ display: 'flex', gap: 8 }}>
                <button type="button" className="oc-btn oc-btn-default" style={{ flex: 1, padding: '9px 0', borderRadius: 8, display: 'flex', alignItems: 'center', justifyContent: 'center', gap: 6 }} onClick={() => onCopy(`entry_${selected.id}`, entryUrl)}>
                  <Copy size={14} /> {copiedField === `entry_${selected.id}` ? 'Copied!' : '复制测试链接'}
                </button>
                <button type="button" className="oc-btn oc-btn-default" style={{ flex: 1, padding: '9px 0', borderRadius: 8, display: 'flex', alignItems: 'center', justifyContent: 'center', gap: 6 }} onClick={handleRegenerate} disabled={saving}>
                  <RefreshCw size={14} /> 重新生成
                </button>
              </div>
            </div>
          ) : selected && needsFeishuNativeConfig ? (
              <div style={{ padding: 24, border: '1px dashed var(--v3-border)', borderRadius: 8 }}>
              <div style={{ color: 'var(--v3-text-name)', fontWeight: 700, marginBottom: 10 }}>飞书原生入口尚未配置</div>
              <div style={{ color: 'var(--v3-text-muted)', fontSize: 13, lineHeight: 1.7, marginBottom: 14 }}>
                飞书扫码入口必须先打开飞书原生应用或机器人入口，并把 scene 带回 CatsCo 登录/申请流程。当前配置没有闭环，因此不会生成可投放二维码。
              </div>
              {feishuEntryReasons.length > 0 && (
                <div style={{ display: 'grid', gap: 6, marginBottom: 14 }}>
                  {feishuEntryReasons.map((reason, index) => (
                    <div key={`${reason}-${index}`} style={{ background: 'rgba(250,81,81,0.1)', color: '#fca5a5', border: '1px solid rgba(250,81,81,0.18)', borderRadius: 8, padding: '8px 10px', fontSize: 12, lineHeight: 1.45 }}>
                      {reason}
                    </div>
                  ))}
                </div>
              )}
              <div style={{ background: 'var(--v3-bg-app)', border: '1px solid var(--v3-border)', borderRadius: 8, padding: 10, color: 'var(--v3-text-main)', fontSize: 12, lineHeight: 1.6, marginBottom: 14 }}>
                必填环境变量：CATSCO_FEISHU_APP_ID、CATSCO_FEISHU_APP_SECRET、CATSCO_FEISHU_ENTRY_URL_TEMPLATE<br />
                模板建议把飞书原生入口最终指向 {'{landing_url_encoded}'}；也可以使用 {'{oauth_url_encoded}'} 或 {'{scene_key}'}。
              </div>
              {feishuEntryStatus?.oauth_callback_url && (
                <div style={{ background: 'var(--v3-bg-app)', border: '1px solid var(--v3-border)', borderRadius: 8, padding: 10, color: 'var(--v3-text-muted)', fontSize: 12, lineHeight: 1.6, marginBottom: 14, wordBreak: 'break-all' }}>
                  飞书 OAuth 回调：{feishuEntryStatus.oauth_callback_url}<br />
                  飞书事件回调：{feishuEntryStatus.events_callback_url || '/api/channels/feishu/events'}
                </div>
              )}
              {feishuOAuthUrl && (
                <div style={{ background: 'rgba(59,130,246,0.12)', color: '#93c5fd', padding: 10, borderRadius: 8, fontSize: 12, lineHeight: 1.5, marginBottom: 14 }}>
                  OAuth 链接可以用于调试身份绑定；正式二维码会优先使用短链版本，确保扫码先完成 CatsCo 绑定。
                </div>
              )}
              <div style={{ display: 'flex', gap: 8 }}>
                <button type="button" className="oc-btn oc-btn-default" style={{ flex: 1, padding: '9px 0', borderRadius: 8, display: 'flex', alignItems: 'center', justifyContent: 'center', gap: 6 }} onClick={() => onCopy(`feishu_oauth_${selected.id}`, feishuOAuthUrl)} disabled={!feishuOAuthUrl}>
                  <Copy size={14} /> {copiedField === `feishu_oauth_${selected.id}` ? 'Copied!' : '复制 OAuth 辅助链接'}
                </button>
                <button type="button" className="oc-btn oc-btn-default" style={{ flex: 1, padding: '9px 0', borderRadius: 8, display: 'flex', alignItems: 'center', justifyContent: 'center', gap: 6 }} onClick={handleRegenerate} disabled={saving}>
                  <RefreshCw size={14} /> 重新生成
                </button>
              </div>
            </div>
          ) : selected && needsClawBotConfig ? (
            <div style={{ padding: 24, border: '1px dashed var(--v3-border)', borderRadius: 8 }}>
              <div style={{ color: 'var(--v3-text-name)', fontWeight: 700, marginBottom: 10 }}>微信 ClawBot 授权码</div>
              <div style={{ color: 'var(--v3-text-muted)', fontSize: 13, lineHeight: 1.7, marginBottom: 14 }}>
                这里会为当前登录用户生成 iLink 一次性授权二维码；它不是可投放的长期入口码。扫码授权后，服务端会保存本次授权返回的 token，之后该用户可在微信 ClawBot 里继续和这个机器人对话。
              </div>
              {clawBotMobileLoading && (
                <div style={{ padding: 32, textAlign: 'center', color: 'var(--v3-text-muted)', border: '1px solid var(--v3-border)', borderRadius: 8, marginBottom: 14 }}>
                  正在生成微信 ClawBot 授权码...
                </div>
              )}
              {!clawBotMobileLoading && clawBotMobileError && (
                <div style={{ background: 'rgba(250,81,81,0.1)', color: '#fca5a5', border: '1px solid rgba(250,81,81,0.18)', borderRadius: 8, padding: '10px 12px', fontSize: 12, lineHeight: 1.5, marginBottom: 14 }}>
                  {clawBotMobileError}
                </div>
              )}
              {!clawBotMobileLoading && clawBotMobileError && (
                <div style={{ display: 'flex', gap: 8, marginBottom: 14 }}>
                  <button type="button" className="oc-btn oc-btn-default" style={{ flex: 1, padding: '9px 0', borderRadius: 8, display: 'flex', alignItems: 'center', justifyContent: 'center', gap: 6 }} onClick={() => onCopy(`entry_${selected.id}`, entryUrl)} disabled={!entryUrl}>
                    <Copy size={14} /> {copiedField === `entry_${selected.id}` ? 'Copied!' : '复制测试链接'}
                  </button>
                  <button type="button" className="oc-btn oc-btn-default" style={{ flex: 1, padding: '9px 0', borderRadius: 8, display: 'flex', alignItems: 'center', justifyContent: 'center', gap: 6 }} onClick={loadClawBotMobileLink}>
                    <RefreshCw size={14} /> 重试生成
                  </button>
                </div>
              )}
              {!clawBotMobileLoading && !clawBotMobileError && clawBotMobileQRValue && (
                <div style={{ display: 'grid', gridTemplateColumns: '196px 1fr', gap: 18, alignItems: 'center', marginBottom: 14 }}>
                  <QRCode value={clawBotMobileQRValue} size={205} />
                  <div style={{ minWidth: 0 }}>
                    <div style={{ fontSize: 12, color: 'var(--v3-text-muted)', marginBottom: 8 }}>微信 ClawBot 一次性授权码</div>
                    <div style={{ background: 'var(--v3-bg-app)', border: '1px solid var(--v3-border)', borderRadius: 8, padding: 10, color: 'var(--v3-text-main)', fontSize: 12, lineHeight: 1.5, wordBreak: 'break-all', marginBottom: 12 }}>
                      {clawBotMobileQRValue}
                    </div>
                    {clawBotMobileStatus?.qrcode_url && (
                      <div style={{ background: 'var(--v3-bg-app)', border: '1px solid var(--v3-border)', borderRadius: 8, padding: 10, color: 'var(--v3-text-muted)', fontSize: 12, lineHeight: 1.5, wordBreak: 'break-all', marginBottom: 12 }}>
                        ClawBot iLink 授权入口：{clawBotMobileStatus.qrcode_url}
                      </div>
                    )}
                    <div style={{
                      background: clawBotAuthStatus?.status === 'error' ? 'rgba(250,81,81,0.1)' : 'rgba(25,195,125,0.10)',
                      color: clawBotAuthStatus?.status === 'error' ? '#fca5a5' : '#19C37D',
                      border: clawBotAuthStatus?.status === 'error' ? '1px solid rgba(250,81,81,0.18)' : '1px solid rgba(25,195,125,0.18)',
                      borderRadius: 8,
                      padding: '9px 10px',
                      fontSize: 12,
                      lineHeight: 1.5,
                      marginBottom: 12,
                    }}>
                      {clawBotAuthStatus?.status === 'saved'
                        ? 'ClawBot 授权已保存；之后该用户可在微信 ClawBot 里继续和这个机器人对话。'
                        : clawBotAuthStatus?.status === 'error'
                          ? `正在重试授权状态检查：${clawBotAuthStatus.message}`
                          : '扫码后请保持这个窗口打开，授权确认后服务端会自动保存 token。'}
                    </div>
                    <div style={{ display: 'flex', gap: 8 }}>
                      <button type="button" className="oc-btn oc-btn-default" style={{ flex: 1, padding: '9px 0', borderRadius: 8, display: 'flex', alignItems: 'center', justifyContent: 'center', gap: 6 }} onClick={() => onCopy(`clawbot_mobile_${selected.id}`, clawBotMobileQRValue)}>
                        <Copy size={14} /> {copiedField === `clawbot_mobile_${selected.id}` ? 'Copied!' : '复制'}
                      </button>
                      <button type="button" className="oc-btn oc-btn-default" style={{ flex: 1, padding: '9px 0', borderRadius: 8, display: 'flex', alignItems: 'center', justifyContent: 'center', gap: 6 }} onClick={loadClawBotMobileLink} disabled={clawBotMobileLoading}>
                        <RefreshCw size={14} /> 重新生成
                      </button>
                    </div>
                  </div>
                </div>
              )}
              {!clawBotMobileLoading && !clawBotMobileError && !clawBotMobileQRValue && clawBotMobileReasons.length > 0 && (
                <div style={{ display: 'grid', gap: 6, marginBottom: 14 }}>
                  {clawBotMobileReasons.map((reason, index) => (
                    <div key={`${reason}-${index}`} style={{ background: 'rgba(250,81,81,0.1)', color: '#fca5a5', border: '1px solid rgba(250,81,81,0.18)', borderRadius: 8, padding: '8px 10px', fontSize: 12, lineHeight: 1.45 }}>
                      {reason}
                    </div>
                  ))}
                </div>
              )}
              {!clawBotMobileLoading && !clawBotMobileError && !clawBotMobileQRValue && (
                <div style={{ background: 'var(--v3-bg-app)', border: '1px solid var(--v3-border)', borderRadius: 8, padding: 10, color: 'var(--v3-text-main)', fontSize: 12, lineHeight: 1.6, marginBottom: 14 }}>
                可选环境变量：CATSCO_WEIXIN_CLAWBOT_ILINK_BASE_URL、CATSCO_WEIXIN_CLAWBOT_CDN_BASE_URL、CATSCO_WEIXIN_CLAWBOT_BOT_TYPE、CATSCO_WEIXIN_CLAWBOT_MEDIA_HOST_ALLOWLIST<br />
                默认使用 https://ilinkai.weixin.qq.com、https://novac2c.cdn.weixin.qq.com/c2c 和 bot_type=3。
                </div>
              )}
              {!clawBotMobileLoading && !clawBotMobileError && !clawBotMobileQRValue && (
                <div style={{ display: 'flex', gap: 8 }}>
                <button type="button" className="oc-btn oc-btn-default" style={{ flex: 1, padding: '9px 0', borderRadius: 8, display: 'flex', alignItems: 'center', justifyContent: 'center', gap: 6 }} onClick={() => onCopy(`entry_${selected.id}`, entryUrl)}>
                  <Copy size={14} /> {copiedField === `entry_${selected.id}` ? 'Copied!' : '复制测试链接'}
                </button>
                <button type="button" className="oc-btn oc-btn-default" style={{ flex: 1, padding: '9px 0', borderRadius: 8, display: 'flex', alignItems: 'center', justifyContent: 'center', gap: 6 }} onClick={loadClawBotMobileLink} disabled={clawBotMobileLoading}>
                  <RefreshCw size={14} /> 生成授权码
                </button>
              </div>
              )}
            </div>
          ) : selected ? (
            <>
              <div style={{ display: 'grid', gridTemplateColumns: '196px 1fr', gap: 18, alignItems: 'center' }}>
                {displayQrUrl && !qrImageError ? (
                  <img
                    src={displayQrUrl}
                    alt={`${channelLabel(channel)}入口码`}
                    width={196}
                    height={196}
                    onError={() => setQrImageError(true)}
                    style={{ borderRadius: 8, background: '#fff', border: '1px solid var(--v3-border)', objectFit: 'contain' }}
                  />
                ) : (
                  <QRCode value={displayUrl} size={205} />
                )}
                <div style={{ minWidth: 0 }}>
                  <div style={{ fontSize: 12, color: 'var(--v3-text-muted)', marginBottom: 8 }}>
                    {qrKind === 'feishu_oauth_entry' ? '飞书 OAuth 绑定入口' : qrKind === 'feishu_native_entry' ? '飞书应用入口码' : qrKind === 'weixin_clawbot_ilink_qr' ? '微信 ClawBot 授权码' : displayQrUrl ? '微信公众号参数二维码' : '网页入口链接'}
                  </div>
                  <div style={{ background: 'var(--v3-bg-app)', border: '1px solid var(--v3-border)', borderRadius: 8, padding: 10, color: 'var(--v3-text-main)', fontSize: 12, lineHeight: 1.5, wordBreak: 'break-all', marginBottom: 14 }}>
                    {displayUrl}
                  </div>
                  {isWeixinOfficialChannel(channel) && qrImageError && (
                    <div style={{ background: 'rgba(250,81,81,0.1)', color: '#FA5151', padding: 10, borderRadius: 8, fontSize: 12, lineHeight: 1.5, marginBottom: 14 }}>
                      微信二维码加载失败，请检查 AppID/AppSecret、公众号接口权限、服务器 IP 白名单和微信后台消息加解密模式。
                    </div>
                  )}
                  {isFeishuChannel(channel) && hasFeishuEntryQRCode && feishuEntryStatus?.native_url && (
                    <div style={{ background: 'var(--v3-bg-app)', border: '1px solid var(--v3-border)', borderRadius: 8, padding: 10, color: 'var(--v3-text-muted)', fontSize: 12, lineHeight: 1.5, wordBreak: 'break-all', marginBottom: 14 }}>
                      飞书原生入口：{feishuEntryStatus.native_url}
                    </div>
                  )}
                  {isWeixinClawBotChannel(channel) && clawBotEntryStatus?.qrcode_url && (
                    <div style={{ background: 'var(--v3-bg-app)', border: '1px solid var(--v3-border)', borderRadius: 8, padding: 10, color: 'var(--v3-text-muted)', fontSize: 12, lineHeight: 1.5, wordBreak: 'break-all', marginBottom: 14 }}>
                      ClawBot iLink 授权入口：{clawBotEntryStatus.qrcode_url}
                    </div>
                  )}
                  {usesLocalEntryUrl && (
                    <div style={{ background: 'rgba(245,158,11,0.12)', color: '#d97706', padding: 10, borderRadius: 8, fontSize: 12, lineHeight: 1.5, marginBottom: 14 }}>
                      当前入口链接不是公网 HTTPS 地址，手机扫码前需要配置公网访问地址。
                    </div>
                  )}
                  <div style={{ display: 'flex', gap: 8 }}>
                    <button type="button" className="oc-btn oc-btn-default" style={{ flex: 1, padding: '9px 0', borderRadius: 8, display: 'flex', alignItems: 'center', justifyContent: 'center', gap: 6 }} onClick={() => onCopy(`entry_${selected.id}`, displayUrl)}>
                      <Copy size={14} /> {copiedField === `entry_${selected.id}` ? 'Copied!' : '复制'}
                    </button>
                    <button type="button" className="oc-btn oc-btn-default" style={{ flex: 1, padding: '9px 0', borderRadius: 8, display: 'flex', alignItems: 'center', justifyContent: 'center', gap: 6 }} onClick={handleRegenerate} disabled={saving}>
                      <RefreshCw size={14} /> 重新生成
                    </button>
                  </div>
                </div>
              </div>
              <div style={{ marginTop: 18, borderTop: '1px solid var(--v3-border)', paddingTop: 16 }}>
                  <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', marginBottom: 10 }}>
                    <div style={{ color: 'var(--v3-text-name)', fontWeight: 700, fontSize: 13 }}>访问管理</div>
                    <button type="button" className="oc-btn oc-btn-default" style={{ padding: '5px 9px', borderRadius: 8, fontSize: 12 }} onClick={loadPendingRequests} disabled={pendingLoading}>
                      {pendingLoading ? '刷新中' : '刷新'}
                    </button>
                  </div>
                  <div style={{ display: 'grid', gridTemplateColumns: 'repeat(4, 1fr)', gap: 6, marginBottom: 10 }}>
                    {accessTabs.map(([value, label, count]) => (
                      <button
                        key={value}
                        type="button"
                        className={`oc-btn ${accessTab === value ? 'oc-btn-primary' : 'oc-btn-default'}`}
                        style={{ padding: '7px 0', borderRadius: 8, fontSize: 12 }}
                        onClick={() => setAccessTab(value)}
                      >
                        {label}{count ? ` ${count}` : ''}
                      </button>
                    ))}
                  </div>
                  {accessTab === 'pending' && (pendingRequests.length > 0 || bindingsByStatus.pending.length > 0) ? (
                    <div style={{ display: 'grid', gap: 8 }}>
                      {pendingRequests.map((request) => (
                        <div key={`${request.from_user_id}-${request.created_at || ''}`} style={{ display: 'grid', gridTemplateColumns: '1fr auto auto', alignItems: 'center', gap: 8, border: '1px solid var(--v3-border)', borderRadius: 8, padding: 10 }}>
                          <div style={{ minWidth: 0 }}>
                            <div style={{ color: 'var(--v3-text-name)', fontWeight: 700, fontSize: 13, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
                              {request.display_name || request.from_username || `用户 ${request.from_user_id}`}
                            </div>
                            <div style={{ color: 'var(--v3-text-muted)', fontSize: 12 }}>申请添加该虚拟员工</div>
                          </div>
                          <button type="button" className="oc-btn oc-btn-default" style={{ padding: '7px 9px', borderRadius: 8, display: 'flex', alignItems: 'center', gap: 5 }} onClick={() => handleReviewRequest(request, 'reject')} disabled={reviewingUID === request.from_user_id}>
                            <XCircle size={14} /> 拒绝
                          </button>
                          <button type="button" className="oc-btn oc-btn-primary" style={{ padding: '7px 9px', borderRadius: 8, display: 'flex', alignItems: 'center', gap: 5 }} onClick={() => handleReviewRequest(request, 'accept')} disabled={reviewingUID === request.from_user_id}>
                            <CheckCircle size={14} /> 通过
                          </button>
                        </div>
                      ))}
                      {bindingsByStatus.pending.map((item) => (
                        <BindingStatusRow key={`pending-${item.binding?.id}`} item={item} note="已绑定 CatsCo，等待好友申请通过" />
                      ))}
                    </div>
                  ) : accessTab === 'pending' ? (
                    <div style={{ color: 'var(--v3-text-muted)', fontSize: 12 }}>暂无待处理申请。</div>
                  ) : accessTab === 'approved' && (botFriends.length > 0 || bindingsByStatus.approved.length > 0) ? (
                    <div style={{ display: 'grid', gap: 8 }}>
                      {botFriends.map((friend) => (
                        <BotFriendRow
                          key={`friend-${friend.id || friend.uid}`}
                          friend={friend}
                          removing={removingFriendUID === (friend.id || friend.uid)}
                          onRemove={() => handleRemoveBotFriend(friend)}
                        />
                      ))}
                      {bindingsByStatus.approved.map((item) => (
                        <BindingStatusRow
                          key={`approved-${item.binding?.id}`}
                          item={item}
                          note="已通过，可从对应渠道对话"
                        />
                      ))}
                    </div>
                  ) : bindingsByStatus[accessTab]?.length ? (
                    <div style={{ display: 'grid', gap: 8 }}>
                      {bindingsByStatus[accessTab].map((item) => (
                        <BindingStatusRow
                          key={`${accessTab}-${item.binding?.id}`}
                          item={item}
                          note={accessTab === 'approved' ? '已通过，可从对应渠道对话' : accessTab === 'rejected' ? '已拒绝，不能继续对话' : '已扫码，等待登录 CatsCo'}
                        />
                      ))}
                    </div>
                  ) : (
                    <div style={{ color: 'var(--v3-text-muted)', fontSize: 12 }}>暂无记录。</div>
                  )}
              </div>
            </>
          ) : (
            <>
              <div style={{ padding: 36, textAlign: 'center', border: '1px dashed var(--v3-border)', borderRadius: 8 }}>
                <div style={{ color: 'var(--v3-text-name)', marginBottom: 12 }}>还没有该渠道的入口码</div>
                <button type="button" className="oc-btn oc-btn-primary" style={{ padding: '10px 18px', borderRadius: 8 }} onClick={handleGenerate} disabled={saving}>
                  {saving ? '正在生成...' : '生成入口码'}
                </button>
              </div>
              <div style={{ marginTop: 18, borderTop: '1px solid var(--v3-border)', paddingTop: 16 }}>
                <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', marginBottom: 10 }}>
                  <div style={{ color: 'var(--v3-text-name)', fontWeight: 700, fontSize: 13 }}>访问管理</div>
                  <button type="button" className="oc-btn oc-btn-default" style={{ padding: '5px 9px', borderRadius: 8, fontSize: 12 }} onClick={loadPendingRequests} disabled={pendingLoading}>
                    {pendingLoading ? '刷新中' : '刷新'}
                  </button>
                </div>
                {pendingRequests.length > 0 && (
                  <div style={{ display: 'grid', gap: 8, marginBottom: 10 }}>
                    {pendingRequests.map((request) => (
                      <div key={`${request.from_user_id}-${request.created_at || ''}`} style={{ display: 'grid', gridTemplateColumns: '1fr auto auto', alignItems: 'center', gap: 8, border: '1px solid var(--v3-border)', borderRadius: 8, padding: 10 }}>
                        <div style={{ minWidth: 0 }}>
                          <div style={{ color: 'var(--v3-text-name)', fontWeight: 700, fontSize: 13, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
                            {request.display_name || request.from_username || `用户 ${request.from_user_id}`}
                          </div>
                          <div style={{ color: 'var(--v3-text-muted)', fontSize: 12 }}>申请添加该虚拟员工</div>
                        </div>
                        <button type="button" className="oc-btn oc-btn-default" style={{ padding: '7px 9px', borderRadius: 8, display: 'flex', alignItems: 'center', gap: 5 }} onClick={() => handleReviewRequest(request, 'reject')} disabled={reviewingUID === request.from_user_id}>
                          <XCircle size={14} /> 拒绝
                        </button>
                        <button type="button" className="oc-btn oc-btn-primary" style={{ padding: '7px 9px', borderRadius: 8, display: 'flex', alignItems: 'center', gap: 5 }} onClick={() => handleReviewRequest(request, 'accept')} disabled={reviewingUID === request.from_user_id}>
                          <CheckCircle size={14} /> 通过
                        </button>
                      </div>
                    ))}
                  </div>
                )}
                {botFriends.length > 0 ? (
                  <div style={{ display: 'grid', gap: 8 }}>
                    {botFriends.map((friend) => (
                      <BotFriendRow
                        key={`friend-empty-entry-${friend.id || friend.uid}`}
                        friend={friend}
                        removing={removingFriendUID === (friend.id || friend.uid)}
                        onRemove={() => handleRemoveBotFriend(friend)}
                      />
                    ))}
                  </div>
                ) : (
                  <div style={{ color: 'var(--v3-text-muted)', fontSize: 12 }}>暂无已授权使用者。</div>
                )}
              </div>
            </>
          )}
        </div>
      </div>
    </div>
  );
}

function BotFriendRow({ friend, removing, onRemove }) {
  const uid = friend?.id || friend?.uid;
  const name = friend?.display_name || friend?.username || `用户 ${uid || ''}`;
  return (
    <div style={{ display: 'grid', gridTemplateColumns: '1fr auto', alignItems: 'center', gap: 8, border: '1px solid var(--v3-border)', borderRadius: 8, padding: 10 }}>
      <div style={{ minWidth: 0 }}>
        <div style={{ color: 'var(--v3-text-name)', fontWeight: 700, fontSize: 13, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
          {name}
        </div>
        <div style={{ color: 'var(--v3-text-muted)', fontSize: 12, lineHeight: 1.5 }}>
          CatsCo 好友 · UID {uid}
        </div>
      </div>
      <button
        type="button"
        className="oc-btn oc-btn-default"
        style={{ padding: '7px 9px', borderRadius: 8, display: 'flex', alignItems: 'center', gap: 5 }}
        onClick={onRemove}
        disabled={removing}
      >
        <XCircle size={14} /> {removing ? '移除中' : '移除'}
      </button>
    </div>
  );
}

function BindingStatusRow({ item, note }) {
  const binding = item?.binding || {};
  const user = item?.user || {};
  const name = user.display_name || user.username || binding.channel_user_id || `绑定 ${binding.id || ''}`;
  const channel = channelLabel(binding.channel || item?.channel);
  return (
    <div style={{ display: 'grid', gridTemplateColumns: '1fr auto', alignItems: 'center', gap: 8, border: '1px solid var(--v3-border)', borderRadius: 8, padding: 10 }}>
      <div style={{ minWidth: 0 }}>
        <div style={{ color: 'var(--v3-text-name)', fontWeight: 700, fontSize: 13, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
          {name}
        </div>
        <div style={{ color: 'var(--v3-text-muted)', fontSize: 12, lineHeight: 1.5 }}>
          {channel} · {note}
        </div>
      </div>
      <div style={{ color: 'var(--v3-text-muted)', fontSize: 12 }}>
        {item?.status || binding.status || ''}
      </div>
    </div>
  );
}

function isPotentiallyPrivateEntryUrl(value) {
  if (!value) return false;
  try {
    const url = new URL(value);
    const hostname = url.hostname.toLowerCase();
    return url.protocol !== 'https:'
      || hostname === 'localhost'
      || hostname === '0.0.0.0'
      || hostname === '::1'
      || hostname.startsWith('127.')
      || hostname.startsWith('10.')
      || hostname.startsWith('192.168.')
      || /^172\.(1[6-9]|2\d|3[0-1])\./.test(hostname)
      || !hostname.includes('.');
  } catch {
    return false;
  }
}
