import React, { useEffect, useRef, useState } from 'react';
import { api, getWebSocketURL } from '../api';
import t from '../i18n';
import { Copy, QrCode, RefreshCw, Zap, Bot, Upload } from 'lucide-react';
import Avatar from './avatar';
import QRCode from './qr-code';
import { IMAGE_UPLOAD_ACCEPT, validateImageUpload } from '../utils/upload-rules';

const CREATE_MODES = {
  SELF_HOSTED: 'self_hosted',
  MANAGED: 'managed',
};

const initialForm = {
  display_name: '',
};

export default function AgentStoreModal({ onClose, user, onBotsChanged }) {
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
      const botsRes = await api.getMyBots();
      setBots(botsRes.bots || []);
    } catch (e) {
      console.error('Load bots error:', e);
      setError(e.message || t('error_server'));
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
    if (!window.confirm(`确定要永久删除 ${bot.display_name} 吗？`)) return;
    try {
      await api.deleteBot(bot.id);
      await loadBots({ silent: true });
      if (onBotsChanged) onBotsChanged();
      setTab('hub');
    } catch (e) {
      setError(e.message || t('error_server'));
    }
  };

  const handleSaveEdit = async (e) => {
    e.preventDefault();
    if (!editingBot) return;
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

  const wsUrl = getWebSocketURL();

  return (
    <div className="oc-modal-overlay" onClick={onClose} style={{ zIndex: 1000 }}>
      {/* Removed arbitrary background hardcoding to allow inheritance from the global .oc-modal V3 matrix */}
      <div className="oc-modal" onClick={e => e.stopPropagation()} style={{ width: 700, maxWidth: '95vw', minHeight: 400 }}>

        <div className="oc-modal-header" style={{ padding: '20px 24px', borderBottom: '1px solid var(--v3-border)' }}>
          <div style={{ display: 'flex', gap: 24, alignItems: 'center' }}>
            <h3 style={{ margin: 0, fontSize: 18, fontWeight: 600, display: 'flex', alignItems: 'center', color: 'var(--v3-text-name)' }}>
              <Zap size={20} style={{marginRight: 8, color: 'var(--v3-primary)'}} fill="currentColor" /> AI 助手管理
            </h3>
            <div style={{ display: 'flex', gap: 16 }}>
              <button
                style={{ background: 'none', border: 'none', color: tab === 'hub' ? 'var(--v3-text-name)' : 'var(--v3-text-muted)', fontWeight: tab === 'hub' ? 600 : 400, cursor: 'pointer', outline: 'none' }}
                onClick={() => setTab('hub')}
              >
                我的助手
              </button>
              <button
                style={{ background: 'none', border: 'none', color: tab === 'create' ? 'var(--v3-text-name)' : 'var(--v3-text-muted)', fontWeight: tab === 'create' ? 600 : 400, cursor: 'pointer', outline: 'none' }}
                onClick={() => setTab('create')}
              >
                创建新助手
              </button>
            </div>
          </div>
          <button className="oc-btn-default" style={{ width: 28, height: 28, padding: 0, display: 'flex', alignItems: 'center', justifyContent: 'center', borderRadius: '50%', border: 'none', background: 'transparent' }} onClick={onClose}>×</button>
        </div>

        <div className="oc-modal-body" style={{ padding: '24px', position: 'relative' }}>

          {error && <div style={{ background: 'rgba(250,81,81,0.1)', color: '#FA5151', padding: 12, borderRadius: 8, marginBottom: 16 }}>{error}</div>}

          {/* HUB TAB */}
          {tab === 'hub' && (
            <>
              {loading ? (
                <div style={{ padding: 40, textAlign: 'center', color: 'var(--v3-text-muted)' }}>加载中...</div>
              ) : bots.length === 0 ? (
                <div style={{ padding: 60, textAlign: 'center', display: 'flex', flexDirection: 'column', alignItems: 'center', gap: 16 }}>
                  <div style={{ color: 'var(--v3-text-muted)' }}><Bot size={48} strokeWidth={1.5} /></div>
                  <div style={{ color: 'var(--v3-text-main)' }}>还没有 AI 助手</div>
                  <button className="oc-btn oc-btn-primary" style={{ padding: '8px 16px', borderRadius: 8 }} onClick={() => setTab('create')}>创建第一个助手</button>
                </div>
              ) : (
                <div className="v3-agent-grid" style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fill, minmax(280px, 1fr))', gap: 16 }}>
                  {bots.map(bot => (
                    <div key={bot.id} className="v3-agent-card" style={{ background: 'var(--v3-bg-app)', border: '1px solid var(--v3-border)', padding: 16, borderRadius: 12 }}>
                      <div className="v3-agent-header" style={{ display: 'flex', alignItems: 'center', gap: 12 }}>
                        <div className="v3-agent-avatar" style={{ width: 48, height: 48, borderRadius: 8, background: 'var(--v3-bg-sidebar)', display: 'flex', alignItems: 'center', justifyContent: 'center', fontSize: 20, color: 'var(--v3-primary)' }}>
                          {bot.display_name.charAt(0).toUpperCase()}
                        </div>
                        <div className="v3-agent-info" style={{ flex: 1, minWidth: 0 }}>
                          <h4 style={{ margin: '0 0 4px 0', fontSize: 16, color: 'var(--v3-text-name)' }}>{bot.display_name}</h4>
                          <span style={{ fontSize: 13, color: 'var(--v3-text-muted)' }}>@{bot.username}</span>
                        </div>
                      </div>
                      <div style={{ fontSize: 12, color: 'var(--v3-text-muted)', marginBottom: 16, marginTop: 12 }}>
                        {bot.tenant_name ? '云托管' : '自托管 (API Key)'}
                      </div>
                      <div className="v3-agent-actions" style={{ display: 'flex', gap: 8 }}>
                        <button className="oc-btn oc-btn-default" style={{ flex: 1, padding: '8px 0', borderRadius: 8 }} onClick={() => {
                          setEditingBot({ ...bot, newDisplayName: bot.display_name, newAvatarUrl: bot.avatar_url || '' });
                          setTab('manage');
                        }}>
                          管理
                        </button>
                        <button
                          className="oc-btn oc-btn-default"
                          style={{ padding: '8px 12px', borderRadius: 8, display: 'flex', alignItems: 'center', gap: 6 }}
                          onClick={() => setEntryBot(bot)}
                          title="入口码"
                        >
                          <QrCode size={14} />
                          入口码
                        </button>
                        {!bot.tenant_name && (
                          <button
                            className="oc-btn oc-btn-default"
                            style={{ padding: '8px 12px', borderRadius: 8 }}
                            onClick={() => handleCopyBotAPIKey(bot, `api_${bot.id}`)}
                            disabled={copyingBotKey === bot.id}
                          >
                            {copiedField === `api_${bot.id}` ? '已复制' : copyingBotKey === bot.id ? '加载中...' : '复制 Key'}
                          </button>
                        )}
                        <button className="oc-btn oc-btn-default" style={{ padding: '8px 16px', borderRadius: 8, borderColor: 'rgba(250,81,81,0.3)' }} onClick={() => handleDelete(bot)}>
                          <span style={{ color: '#FA5151' }}>删除</span>
                        </button>
                      </div>
                    </div>
                  ))}
                </div>
              )}
            </>
          )}

          {/* CREATE TAB */}
          {tab === 'create' && (
            <form onSubmit={handleCreate} style={{ maxWidth: 460, margin: '0 auto' }}>
              <div style={{ textAlign: 'center', marginBottom: 24, color: 'var(--v3-primary)' }}>
                <Zap size={32} fill="currentColor" style={{ marginBottom: 8 }} />
                <h2 style={{ margin: '0 0 8px 0', fontSize: 20, color: 'var(--v3-text-name)' }}>创建 AI 助手</h2>
                <p style={{ margin: 0, color: 'var(--v3-text-muted)', fontSize: 14 }}>创建一个新的 AI 助手并获取 API Key。</p>
              </div>

              <div className="oc-mode-switch" style={{ marginBottom: 24, display: 'flex', gap: 12 }}>
                <div
                  className={`oc-mode-option ${createMode === CREATE_MODES.SELF_HOSTED ? 'active' : ''}`}
                  onClick={() => setCreateMode(CREATE_MODES.SELF_HOSTED)}
                  style={{ flex: 1, padding: 16, border: createMode === CREATE_MODES.SELF_HOSTED ? '1px solid var(--v3-primary)' : '1px solid var(--v3-border)', borderRadius: 8, cursor: 'pointer', background: createMode === CREATE_MODES.SELF_HOSTED ? 'rgba(16,185,129,0.05)' : 'var(--v3-bg-app)' }}
                >
                  <div style={{ fontWeight: 600, color: 'var(--v3-text-name)', marginBottom: 4 }}>自托管</div>
                  <div style={{ fontSize: 12, color: 'var(--v3-text-muted)' }}>自行部署服务器，通过 API Key 和 WebSocket 连接。</div>
                </div>
                <div
                  className={`oc-mode-option ${createMode === CREATE_MODES.MANAGED ? 'active' : ''}`}
                  onClick={() => setCreateMode(CREATE_MODES.MANAGED)}
                  style={{ flex: 1, padding: 16, border: '1px solid var(--v3-border)', borderRadius: 8, cursor: 'pointer', opacity: 0.5, background: 'var(--v3-bg-app)' }}
                >
                  <div style={{ fontWeight: 600, color: 'var(--v3-text-name)', marginBottom: 4 }}>云托管</div>
                  <div style={{ fontSize: 12, color: 'var(--v3-text-muted)' }}>自动部署无状态助手（即将推出）</div>
                </div>
              </div>

              <div className="oc-form-group" style={{ marginBottom: 24 }}>
                <label style={{ display: 'block', marginBottom: 8, fontSize: 13, color: 'var(--v3-text-muted)' }}>助手名称</label>
                <input
                  type="text"
                  value={createForm.display_name}
                  onChange={(e) => setCreateForm({ ...createForm, display_name: e.target.value })}
                  placeholder="例如：代码审查助手"
                  className="oc-auth-input"
                  style={{ width: '100%', padding: '12px 16px', fontSize: 15 }}
                  required
                  disabled={isSubmitting}
                />
              </div>

              <button type="submit" className="oc-btn oc-btn-primary" style={{ width: '100%', padding: '14px 0', fontSize: 15, borderRadius: 8 }} disabled={isSubmitting || createMode === CREATE_MODES.MANAGED}>
                {isSubmitting ? '创建中...' : '生成 API Key 并创建'}
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
      </div>
      {entryBot && (
        <AgentEntryModal
          bot={entryBot}
          onClose={() => setEntryBot(null)}
          onCopy={handleCopy}
          copiedField={copiedField}
        />
      )}
    </div>
  );
}

function AgentEntryModal({ bot, onClose, onCopy, copiedField }) {
  const [channel, setChannel] = useState('weixin');
  const [channelAppIds, setChannelAppIds] = useState({ weixin: '', feishu: '' });
  const [entries, setEntries] = useState([]);
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState('');
  const [qrImageError, setQrImageError] = useState(false);
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

  const channelAppId = channelAppIds[channel] || '';
  const managedChannelAppID = channel === 'feishu' || channel === 'weixin';
  const normalizedChannelAppId = managedChannelAppID ? '' : channelAppId.trim();
  const entryScopeMatches = (entry, targetChannel = channel, targetAppId = normalizedChannelAppId) => (
    entry.channel === targetChannel
    && (targetChannel === 'feishu' || targetChannel === 'weixin' || (entry.channel_app_id || '') === targetAppId)
  );
  const selected = entries.find((entry) => entryScopeMatches(entry));
  const entryUrl = selected?.entry_url || '';
  const channelQrUrl = selected?.channel_qr_url || '';
  const displayQrUrl = channel === 'weixin' && channelQrUrl ? channelQrUrl : '';
  const displayUrl = displayQrUrl || entryUrl;
  const usesLocalEntryUrl = isPotentiallyPrivateEntryUrl(displayUrl);
  const needsWeixinConfig = channel === 'weixin' && selected && !displayQrUrl;

  useEffect(() => {
    setQrImageError(false);
  }, [displayQrUrl]);

  const handleGenerate = async () => {
    try {
      setSaving(true);
      setError('');
      const res = await api.createAgentEntry(botId, channel, normalizedChannelAppId);
      const next = res.entry;
      setEntries((prev) => [next, ...prev.filter((entry) => (
        !entryScopeMatches(entry, next.channel, next.channel === 'feishu' || next.channel === 'weixin' ? '' : (next.channel_app_id || ''))
      ))]);
    } catch (err) {
      setError(err.message || 'Failed to generate entry code');
    } finally {
      setSaving(false);
    }
  };

  const handleRegenerate = async () => {
    if (!selected) return;
    if (!window.confirm('重新生成后，旧入口码会失效。继续吗？')) return;
    try {
      setSaving(true);
      setError('');
      const res = await api.regenerateAgentEntry(selected.id);
      const next = res.entry;
      setEntries((prev) => [next, ...prev.filter((entry) => (
        entry.id !== selected.id
        && !entryScopeMatches(entry, next.channel, next.channel === 'feishu' || next.channel === 'weixin' ? '' : (next.channel_app_id || ''))
      ))]);
    } catch (err) {
      setError(err.message || 'Failed to regenerate entry code');
    } finally {
      setSaving(false);
    }
  };

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
            {[
              ['weixin', '微信公众号'],
              ['feishu', '飞书'],
            ].map(([value, label]) => (
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

          {error && (
            <div style={{ background: 'rgba(250,81,81,0.1)', color: '#FA5151', padding: 12, borderRadius: 8, marginBottom: 16, fontSize: 13 }}>
              {error}
            </div>
          )}

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
          ) : selected ? (
            <div style={{ display: 'grid', gridTemplateColumns: '196px 1fr', gap: 18, alignItems: 'center' }}>
              {displayQrUrl && !qrImageError ? (
                <img
                  src={displayQrUrl}
                  alt="微信入口码"
                  width={196}
                  height={196}
                  onError={() => setQrImageError(true)}
                  style={{ borderRadius: 8, background: '#fff', border: '1px solid var(--v3-border)', objectFit: 'contain' }}
                />
              ) : (
                <QRCode value={entryUrl} size={196} />
              )}
              <div style={{ minWidth: 0 }}>
                <div style={{ fontSize: 12, color: 'var(--v3-text-muted)', marginBottom: 8 }}>
                  {displayQrUrl ? '二维码图片跳转地址' : '入口链接'}
                </div>
                <div style={{ background: 'var(--v3-bg-app)', border: '1px solid var(--v3-border)', borderRadius: 8, padding: 10, color: 'var(--v3-text-main)', fontSize: 12, lineHeight: 1.5, wordBreak: 'break-all', marginBottom: 14 }}>
                  {displayUrl}
                </div>
                {channel === 'weixin' && qrImageError && (
                  <div style={{ background: 'rgba(250,81,81,0.1)', color: '#FA5151', padding: 10, borderRadius: 8, fontSize: 12, lineHeight: 1.5, marginBottom: 14 }}>
                    微信二维码加载失败，请检查 AppID/AppSecret、公众号接口权限、服务器 IP 白名单和微信后台消息加解密模式。
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
          ) : (
            <div style={{ padding: 36, textAlign: 'center', border: '1px dashed var(--v3-border)', borderRadius: 8 }}>
              <div style={{ color: 'var(--v3-text-name)', marginBottom: 12 }}>还没有该渠道的入口码</div>
              <button type="button" className="oc-btn oc-btn-primary" style={{ padding: '10px 18px', borderRadius: 8 }} onClick={handleGenerate} disabled={saving}>
                {saving ? '正在生成...' : '生成入口码'}
              </button>
            </div>
          )}
        </div>
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
