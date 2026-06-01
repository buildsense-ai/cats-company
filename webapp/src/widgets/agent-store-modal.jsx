import React, { useEffect, useRef, useState } from 'react';
import { api, getWebSocketURL } from '../api';
import t from '../i18n';
import { Zap, Bot, Upload } from 'lucide-react';
import Avatar from './avatar';

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
  const [accessRecords, setAccessRecords] = useState([]);
  const [accessLoading, setAccessLoading] = useState(false);
  const [inviteForm, setInviteForm] = useState({ target: '', permission: 'use' });
  const [accessSaving, setAccessSaving] = useState(false);
  const avatarFileRef = useRef(null);
  const [avatarUploading, setAvatarUploading] = useState(false);

  useEffect(() => { loadBots(); }, []);
  useEffect(() => {
    if (tab === 'manage' && editingBot?.id) {
      loadAgentAccess(editingBot.id);
    }
  }, [tab, editingBot?.id]);

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
    if (!window.confirm(`Are you sure you want to permanently delete ${bot.display_name}?`)) return;
    try {
      await api.deleteBot(bot.id);
      await loadBots({ silent: true });
      if (onBotsChanged) onBotsChanged();
      setTab('hub');
    } catch (e) {
      setError(e.message || t('error_server'));
    }
  };

  const loadAgentAccess = async (agentId) => {
    if (!agentId) return;
    try {
      setAccessLoading(true);
      const result = await api.getAgentAccess(agentId);
      setAccessRecords(result.access || []);
    } catch (e) {
      setError(e.message || 'Failed to load access list');
    } finally {
      setAccessLoading(false);
    }
  };

  const handleInviteAccess = async () => {
    if (!editingBot?.id) return;
    const target = inviteForm.target.trim();
    if (!target) {
      setError('Enter a teammate username or user ID');
      return;
    }
    const payload = /^\d+$/.test(target)
      ? { target_user_id: Number(target), permission: inviteForm.permission }
      : { target_username: target, permission: inviteForm.permission };
    try {
      setError('');
      setAccessSaving(true);
      await api.inviteAgentAccess(editingBot.id, payload);
      setInviteForm({ target: '', permission: 'use' });
      await loadAgentAccess(editingBot.id);
      if (onBotsChanged) onBotsChanged();
      window.dispatchEvent(new Event('cc:data-changed'));
    } catch (e) {
      setError(e.message || 'Failed to invite teammate');
    } finally {
      setAccessSaving(false);
    }
  };

  const handleUpdateAccess = async (record, patch) => {
    if (!editingBot?.id || !record?.id) return;
    try {
      setError('');
      setAccessSaving(true);
      await api.updateAgentAccess(editingBot.id, record.id, {
        permission: patch.permission || record.permission,
        status: patch.status || record.status,
      });
      await loadAgentAccess(editingBot.id);
      window.dispatchEvent(new Event('cc:data-changed'));
    } catch (e) {
      setError(e.message || 'Failed to update access');
    } finally {
      setAccessSaving(false);
    }
  };

  const handleRevokeAccess = async (record) => {
    if (!editingBot?.id || !record?.id) return;
    try {
      setError('');
      setAccessSaving(true);
      await api.revokeAgentAccess(editingBot.id, record.id);
      await loadAgentAccess(editingBot.id);
      window.dispatchEvent(new Event('cc:data-changed'));
    } catch (e) {
      setError(e.message || 'Failed to revoke access');
    } finally {
      setAccessSaving(false);
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
              <Zap size={20} style={{marginRight: 8, color: 'var(--v3-primary)'}} fill="currentColor" /> Agent Workspace
            </h3>
            <div style={{ display: 'flex', gap: 16 }}>
              <button
                style={{ background: 'none', border: 'none', color: tab === 'hub' ? 'var(--v3-text-name)' : 'var(--v3-text-muted)', fontWeight: tab === 'hub' ? 600 : 400, cursor: 'pointer', outline: 'none' }}
                onClick={() => setTab('hub')}
              >
                My Agents
              </button>
              <button
                style={{ background: 'none', border: 'none', color: tab === 'create' ? 'var(--v3-text-name)' : 'var(--v3-text-muted)', fontWeight: tab === 'create' ? 600 : 400, cursor: 'pointer', outline: 'none' }}
                onClick={() => setTab('create')}
              >
                Create New
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
                <div style={{ padding: 40, textAlign: 'center', color: 'var(--v3-text-muted)' }}>Retrieving agents...</div>
              ) : bots.length === 0 ? (
                <div style={{ padding: 60, textAlign: 'center', display: 'flex', flexDirection: 'column', alignItems: 'center', gap: 16 }}>
                  <div style={{ color: 'var(--v3-text-muted)' }}><Bot size={48} strokeWidth={1.5} /></div>
                  <div style={{ color: 'var(--v3-text-main)' }}>Your workspace has no active agents.</div>
                  <button className="oc-btn oc-btn-primary" style={{ padding: '8px 16px', borderRadius: 8 }} onClick={() => setTab('create')}>Deploy First Agent</button>
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
                        {bot.tenant_name ? 'Cloud Managed' : 'Self-hosted (API Key)'}
                      </div>
                      <div className="v3-agent-actions" style={{ display: 'flex', gap: 8 }}>
                        <button className="oc-btn oc-btn-default" style={{ flex: 1, padding: '8px 0', borderRadius: 8 }} onClick={() => {
                          setEditingBot({ ...bot, newDisplayName: bot.display_name, newAvatarUrl: bot.avatar_url || '' });
                          setTab('manage');
                        }}>
                          Manage
                        </button>
                        {!bot.tenant_name && (
                          <button
                            className="oc-btn oc-btn-default"
                            style={{ padding: '8px 12px', borderRadius: 8 }}
                            onClick={() => handleCopyBotAPIKey(bot, `api_${bot.id}`)}
                            disabled={copyingBotKey === bot.id}
                          >
                            {copiedField === `api_${bot.id}` ? 'Copied!' : copyingBotKey === bot.id ? 'Loading...' : 'Copy Key'}
                          </button>
                        )}
                        <button className="oc-btn oc-btn-default" style={{ padding: '8px 16px', borderRadius: 8, borderColor: 'rgba(250,81,81,0.3)' }} onClick={() => handleDelete(bot)}>
                          <span style={{ color: '#FA5151' }}>Del</span>
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
                <h2 style={{ margin: '0 0 8px 0', fontSize: 20, color: 'var(--v3-text-name)' }}>Issue new API Key</h2>
                <p style={{ margin: 0, color: 'var(--v3-text-muted)', fontSize: 14 }}>Deploy a new intelligent agent to the workspace.</p>
              </div>

              <div className="oc-mode-switch" style={{ marginBottom: 24, display: 'flex', gap: 12 }}>
                <div
                  className={`oc-mode-option ${createMode === CREATE_MODES.SELF_HOSTED ? 'active' : ''}`}
                  onClick={() => setCreateMode(CREATE_MODES.SELF_HOSTED)}
                  style={{ flex: 1, padding: 16, border: createMode === CREATE_MODES.SELF_HOSTED ? '1px solid var(--v3-primary)' : '1px solid var(--v3-border)', borderRadius: 8, cursor: 'pointer', background: createMode === CREATE_MODES.SELF_HOSTED ? 'rgba(16,185,129,0.05)' : 'var(--v3-bg-app)' }}
                >
                  <div style={{ fontWeight: 600, color: 'var(--v3-text-name)', marginBottom: 4 }}>Self-Hosted</div>
                  <div style={{ fontSize: 12, color: 'var(--v3-text-muted)' }}>You bring your own server. We provide the API Key and WebSocket tunnel.</div>
                </div>
                <div
                  className={`oc-mode-option ${createMode === CREATE_MODES.MANAGED ? 'active' : ''}`}
                  onClick={() => setCreateMode(CREATE_MODES.MANAGED)}
                  style={{ flex: 1, padding: 16, border: '1px solid var(--v3-border)', borderRadius: 8, cursor: 'pointer', opacity: 0.5, background: 'var(--v3-bg-app)' }}
                >
                  <div style={{ fontWeight: 600, color: 'var(--v3-text-name)', marginBottom: 4 }}>Cloud Managed</div>
                  <div style={{ fontSize: 12, color: 'var(--v3-text-muted)' }}>Auto-deployed stateless agents. (Coming soon)</div>
                </div>
              </div>

              <div className="oc-form-group" style={{ marginBottom: 24 }}>
                <label style={{ display: 'block', marginBottom: 8, fontSize: 13, color: 'var(--v3-text-muted)' }}>AGENT DISPLAY NAME</label>
                <input
                  type="text"
                  value={createForm.display_name}
                  onChange={(e) => setCreateForm({ ...createForm, display_name: e.target.value })}
                  placeholder="e.g. Code Reviewer Bot"
                  className="oc-auth-input"
                  style={{ width: '100%', padding: '12px 16px', fontSize: 15 }}
                  required
                  disabled={isSubmitting}
                />
              </div>

              <button type="submit" className="oc-btn oc-btn-primary" style={{ width: '100%', padding: '14px 0', fontSize: 15, borderRadius: 8 }} disabled={isSubmitting || createMode === CREATE_MODES.MANAGED}>
                {isSubmitting ? 'Generating Identity...' : 'Generate API Key & Deploy'}
              </button>
            </form>
          )}

          {/* SUCCESS (API KEY) TAB */}
          {tab === 'success' && createdBot && (
            <div style={{ maxWidth: 460, margin: '0 auto', textAlign: 'center' }}>
              <div style={{ width: 64, height: 64, background: 'rgba(16, 185, 129, 0.1)', color: 'var(--v3-primary)', borderRadius: '50%', display: 'flex', alignItems: 'center', justifyContent: 'center', fontSize: 32, margin: '0 auto 20px' }}>✓</div>
              <h2 style={{ margin: '0 0 8px 0', color: 'var(--v3-text-name)' }}>Agent Provisioned</h2>
              <p style={{ margin: '0 0 24px 0', color: 'var(--v3-text-muted)', fontSize: 14 }}>Your self-hosted agent <b style={{color: 'var(--v3-text-name)'}}>{createdBot.display_name}</b> is ready to connect.</p>

              <div style={{ textAlign: 'left', background: 'var(--v3-bg-app)', border: '1px solid var(--v3-border)', borderRadius: 8, padding: 16, marginBottom: 16 }}>
                <div style={{ fontSize: 11, color: 'var(--v3-text-muted)', marginBottom: 8, letterSpacing: 0.5 }}>API KEY</div>
                <div style={{ display: 'flex', gap: 8 }}>
                  <code style={{ flex: 1, background: '#111', padding: '10px 12px', borderRadius: 6, color: 'var(--v3-primary)', fontFamily: 'monospace', fontSize: 13, userSelect: 'all' }}>
                    {createdBot.api_key}
                  </code>
                  <button className="oc-btn oc-btn-default" onClick={() => handleCopy('api', createdBot.api_key)}>
                    {copiedField === 'api' ? 'Copied!' : 'Copy'}
                  </button>
                </div>
              </div>

              <div style={{ textAlign: 'left', background: 'var(--v3-bg-app)', border: '1px solid var(--v3-border)', borderRadius: 8, padding: 16, marginBottom: 24 }}>
                <div style={{ fontSize: 11, color: 'var(--v3-text-muted)', marginBottom: 8, letterSpacing: 0.5 }}>WEBSOCKET TUNNEL URL</div>
                <div style={{ display: 'flex', gap: 8 }}>
                  <code style={{ flex: 1, background: '#111', padding: '10px 12px', borderRadius: 6, color: 'var(--v3-text-main)', fontFamily: 'monospace', fontSize: 13, userSelect: 'all' }}>
                    {wsUrl}
                  </code>
                  <button className="oc-btn oc-btn-default" onClick={() => handleCopy('ws', wsUrl)}>
                    {copiedField === 'ws' ? 'Copied!' : 'Copy'}
                  </button>
                </div>
              </div>

              <button className="oc-btn oc-btn-default" style={{ width: '100%', padding: '12px 0', borderRadius: 8 }} onClick={() => setTab('hub')}>
                Return to Hub
              </button>
            </div>
          )}

          {/* MANAGE / EDIT TAB */}
          {tab === 'manage' && editingBot && (
            <form onSubmit={handleSaveEdit} style={{ maxWidth: 620, margin: '0 auto' }}>
              <h2 style={{ margin: '0 0 24px 0', fontSize: 20, color: 'var(--v3-text-name)' }}>Manage Configuration</h2>

              <div className="oc-form-group" style={{ marginBottom: 16 }}>
                <label style={{ display: 'block', marginBottom: 8, fontSize: 13, color: 'var(--v3-text-muted)' }}>Display Name</label>
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
                <label style={{ display: 'block', marginBottom: 8, fontSize: 13, color: 'var(--v3-text-muted)' }}>AGENT AVATAR</label>
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
                      {avatarUploading ? 'Uploading...' : 'Choose Avatar'}
                    </button>
                    {editingBot.newAvatarUrl && (
                      <button
                        type="button"
                        style={{ background: 'none', border: 'none', color: 'var(--v3-text-muted)', fontSize: 12, cursor: 'pointer', textAlign: 'left', padding: 0 }}
                        onClick={() => setEditingBot({ ...editingBot, newAvatarUrl: '' })}
                      >
                        Remove avatar
                      </button>
                    )}
                  </div>
                  <input
                    ref={avatarFileRef}
                    type="file"
                    accept="image/*"
                    style={{ display: 'none' }}
                    onChange={async (event) => {
                      const file = event.target.files?.[0];
                      if (!file) return;
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
                  <div style={{ fontSize: 11, color: 'var(--v3-text-muted)', marginBottom: 8, letterSpacing: 0.5 }}>AUTHORIZATION (API KEY)</div>
                  <div style={{ display: 'flex', gap: 8, marginBottom: 16 }}>
                    <code style={{ flex: 1, background: '#111', padding: '10px 12px', borderRadius: 6, color: editingBot.api_key ? 'var(--v3-primary)' : 'var(--v3-text-muted)', fontFamily: 'monospace', fontSize: 13, userSelect: 'all' }}>
                      {editingBot.api_key || 'Click Copy to load API Key'}
                    </code>
                    <button 
                      type="button" 
                      className="oc-btn oc-btn-default" 
                      onClick={() => handleCopyBotAPIKey(editingBot, 'api_edit')}
                      disabled={copyingBotKey === editingBot.id}
                    >
                      {copiedField === 'api_edit' ? 'Copied!' : copyingBotKey === editingBot.id ? 'Loading...' : 'Copy'}
                    </button>
                  </div>

                  <div style={{ fontSize: 11, color: 'var(--v3-text-muted)', marginBottom: 8, letterSpacing: 0.5 }}>WEBSOCKET TUNNEL URL</div>
                  <div style={{ display: 'flex', gap: 8 }}>
                    <code style={{ flex: 1, background: '#111', padding: '10px 12px', borderRadius: 6, color: 'var(--v3-text-main)', fontFamily: 'monospace', fontSize: 13, userSelect: 'all' }}>
                      {wsUrl}
                    </code>
                    <button type="button" className="oc-btn oc-btn-default" onClick={() => handleCopy('ws_edit', wsUrl)}>
                      {copiedField === 'ws_edit' ? 'Copied!' : 'Copy'}
                    </button>
                  </div>
                </div>
              )}

              <div style={{ border: '1px solid var(--v3-border)', borderRadius: 8, padding: 16, marginBottom: 24 }}>
                <div style={{ display: 'flex', justifyContent: 'space-between', gap: 12, alignItems: 'center', marginBottom: 12 }}>
                  <div>
                    <div style={{ fontSize: 13, color: 'var(--v3-text-name)', fontWeight: 600 }}>Teammate Access</div>
                    <div style={{ fontSize: 12, color: 'var(--v3-text-muted)', marginTop: 4 }}>Invite teammates and control who can use this agent.</div>
                  </div>
                  <button type="button" className="oc-btn oc-btn-default" style={{ padding: '6px 10px', borderRadius: 8 }} onClick={() => loadAgentAccess(editingBot.id)} disabled={accessLoading}>
                    {accessLoading ? 'Loading...' : 'Refresh'}
                  </button>
                </div>

                <div style={{ display: 'grid', gridTemplateColumns: 'minmax(0, 1fr) 120px 96px', gap: 8, marginBottom: 14 }}>
                  <input
                    type="text"
                    value={inviteForm.target}
                    onChange={(e) => setInviteForm({ ...inviteForm, target: e.target.value })}
                    placeholder="Username or user ID"
                    className="oc-auth-input"
                    style={{ padding: '10px 12px', fontSize: 14 }}
                    disabled={accessSaving}
                  />
                  <select
                    value={inviteForm.permission}
                    onChange={(e) => setInviteForm({ ...inviteForm, permission: e.target.value })}
                    className="oc-auth-input"
                    style={{ padding: '10px 12px', fontSize: 14 }}
                    disabled={accessSaving}
                  >
                    <option value="use">Use</option>
                    <option value="view">View</option>
                    <option value="manage">Manage</option>
                  </select>
                  <button type="button" className="oc-btn oc-btn-primary" style={{ borderRadius: 8 }} onClick={handleInviteAccess} disabled={accessSaving}>
                    Invite
                  </button>
                </div>

                {accessLoading ? (
                  <div style={{ padding: 20, textAlign: 'center', color: 'var(--v3-text-muted)', fontSize: 13 }}>Loading access list...</div>
                ) : accessRecords.length === 0 ? (
                  <div style={{ padding: 16, color: 'var(--v3-text-muted)', fontSize: 13, borderTop: '1px solid var(--v3-border)' }}>No teammates have been invited yet.</div>
                ) : (
                  <div style={{ display: 'flex', flexDirection: 'column', borderTop: '1px solid var(--v3-border)' }}>
                    {accessRecords.map((record) => (
                      <div key={record.id} style={{ display: 'grid', gridTemplateColumns: 'minmax(0, 1fr) 108px 132px 72px', gap: 8, alignItems: 'center', padding: '10px 0', borderBottom: '1px solid var(--v3-border)' }}>
                        <div style={{ minWidth: 0 }}>
                          <div style={{ color: 'var(--v3-text-name)', fontSize: 13, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
                            {record.display_name || record.username || `User ${record.user_uid}`}
                          </div>
                          <div style={{ color: 'var(--v3-text-muted)', fontSize: 12 }}>@{record.username || record.user_uid}</div>
                        </div>
                        <select
                          value={record.permission}
                          onChange={(e) => handleUpdateAccess(record, { permission: e.target.value })}
                          className="oc-auth-input"
                          style={{ padding: '8px 10px', fontSize: 13 }}
                          disabled={accessSaving}
                        >
                          <option value="use">Use</option>
                          <option value="view">View</option>
                          <option value="manage">Manage</option>
                        </select>
                        <select
                          value={record.status}
                          onChange={(e) => handleUpdateAccess(record, { status: e.target.value })}
                          className="oc-auth-input"
                          style={{ padding: '8px 10px', fontSize: 13 }}
                          disabled={accessSaving}
                        >
                          <option value="pending_accept">Pending</option>
                          <option value="active">Ready</option>
                          <option value="blocked">Blocked</option>
                          <option value="revoked">Revoked</option>
                        </select>
                        <button type="button" className="oc-btn oc-btn-default" style={{ padding: '8px 0', borderRadius: 8, color: '#FA5151' }} onClick={() => handleRevokeAccess(record)} disabled={accessSaving}>
                          Revoke
                        </button>
                      </div>
                    ))}
                  </div>
                )}
              </div>

              <div style={{ display: 'flex', gap: 12 }}>
                <button type="button" className="oc-btn oc-btn-default" style={{ flex: 1, padding: '14px 0', borderRadius: 8 }} onClick={() => setTab('hub')}>
                  Cancel
                </button>
                <button type="submit" className="oc-btn oc-btn-primary" style={{ flex: 1, padding: '14px 0', borderRadius: 8 }}>
                  Save Changes
                </button>
              </div>
            </form>
          )}

        </div>
      </div>
    </div>
  );
}
