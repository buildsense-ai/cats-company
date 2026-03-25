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
  const [editingBot, setEditingBot] = useState(null);
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
            <form onSubmit={handleSaveEdit} style={{ maxWidth: 460, margin: '0 auto' }}>
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
                      {editingBot.api_key || '••••••••••••••••••••••••••••'}
                    </code>
                    <button 
                      type="button" 
                      className="oc-btn oc-btn-default" 
                      onClick={() => editingBot.api_key && handleCopy('api_edit', editingBot.api_key)}
                      disabled={!editingBot.api_key}
                      title={!editingBot.api_key ? "API Key is hidden by the server after creation for security" : ""}
                      style={{ opacity: !editingBot.api_key ? 0.5 : 1, cursor: !editingBot.api_key ? 'not-allowed' : 'pointer' }}
                    >
                      {copiedField === 'api_edit' ? 'Copied!' : 'Copy'}
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
