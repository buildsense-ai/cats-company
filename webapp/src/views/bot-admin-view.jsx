import React, { useState, useEffect } from 'react';
import { api, getWebSocketURL } from '../api';
import t from '../i18n';

const CREATE_MODES = {
  SELF_HOSTED: 'self_hosted',
  MANAGED: 'managed',
};

const initialForm = {
  display_name: '',
};

export default function BotAdminView({ onBack, user }) {
  const [bots, setBots] = useState([]);
  const [loading, setLoading] = useState(true);
  const [showCreate, setShowCreate] = useState(false);
  const [createForm, setCreateForm] = useState(initialForm);
  const [createMode, setCreateMode] = useState(CREATE_MODES.SELF_HOSTED);
  const [isSubmitting, setIsSubmitting] = useState(false);
  const [error, setError] = useState('');
  const [createdBot, setCreatedBot] = useState(null);
  const [createdMode, setCreatedMode] = useState(CREATE_MODES.SELF_HOSTED);
  const [friendStatus, setFriendStatus] = useState('');
  const [friendSuccess, setFriendSuccess] = useState(false);
  const [copiedField, setCopiedField] = useState('');

  useEffect(() => {
    loadBots();
  }, []);

  const loadBots = async () => {
    try {
      setLoading(true);
      const botsRes = await api.getMyBots();
      setBots(botsRes.bots || []);
    } catch (e) {
      console.error('Load bots error:', e);
      setError(e.message || t('error_server'));
    } finally {
      setLoading(false);
    }
  };

  const handleCreate = async (e) => {
    e.preventDefault();
    const displayName = createForm.display_name.trim();
    if (!displayName) {
      setError(t('bot_create_name_required'));
      return;
    }

    const username = buildBotUsername(displayName);

    try {
      setError('');
      setIsSubmitting(true);
      setFriendStatus('');
      setFriendSuccess(false);
      const result = await api.createBot(
        { username, display_name: displayName },
        createMode === CREATE_MODES.MANAGED
      );
      const fullResult = {
        ...result,
        display_name: result.display_name || displayName,
      };
      setCreatedBot(fullResult);
      setCreatedMode(createMode);
      setShowCreate(false);
      setCreateForm(initialForm);
      await loadBots();
      await autoAddFriend(fullResult);
    } catch (e) {
      setError(e.message || t('error_server'));
    } finally {
      setIsSubmitting(false);
    }
  };

  const handleToggleVisibility = async (bot) => {
    try {
      const next = bot.visibility === 'public' ? 'private' : 'public';
      await api.setBotVisibility(bot.id, next);
      await loadBots();
    } catch (e) {
      setError(e.message || t('error_server'));
    }
  };

  const handleDelete = async (bot) => {
    if (!window.confirm(t('bot_delete_confirm', { name: bot.display_name || bot.username }))) {
      return;
    }
    try {
      await api.deleteBot(bot.id);
      setBots((prev) => prev.filter((item) => item.id !== bot.id));
    } catch (e) {
      setError(e.message || t('error_server'));
    }
  };

  const autoAddFriend = async (bot) => {
    if (!user?.uid || !bot?.api_key) {
      setFriendStatus(t('bot_friend_manual'));
      setFriendSuccess(false);
      return;
    }

    try {
      await api.sendFriendRequest(bot.uid);
      await api.acceptFriendAsBot(bot.api_key, user.uid);
      setFriendStatus(t('bot_friend_success'));
      setFriendSuccess(true);
    } catch (e) {
      console.error('Auto add friend failed:', e);
      setFriendStatus(`${t('bot_friend_failed')}: ${e.message}`);
      setFriendSuccess(false);
    }
  };

  const handleCopy = async (field, value) => {
    try {
      await navigator.clipboard.writeText(value);
      setCopiedField(field);
      window.setTimeout(() => {
        setCopiedField((current) => (current === field ? '' : current));
      }, 2000);
    } catch (e) {
      console.error('Copy failed:', e);
    }
  };

  const wsUrl = getWebSocketURL();

  if (loading) {
    return <div className="oc-loading">{t('loading') || 'Loading...'}</div>;
  }

  return (
    <div className="oc-bot-admin">
      <div className="oc-header">
        <button className="oc-back-btn" onClick={onBack}>←</button>
        {t('bot_admin')}
        <button className="oc-header-action" onClick={() => {
          setError('');
          setShowCreate(true);
        }}>+</button>
      </div>

      {createdBot && (
        <div className="oc-api-key-banner">
          <div className="oc-api-key-stack">
            <div className="oc-api-key-label">{t('bot_created_title')}</div>
            <div className="oc-api-key-meta">
              <span>{createdBot.display_name || createdBot.username}</span>
              <span>@{createdBot.username}</span>
              {createdMode === CREATE_MODES.MANAGED && createdBot.tenant_name && (
                <span>{t('bot_tenant_label')}: {createdBot.tenant_name}</span>
              )}
            </div>
            <div className="oc-credential-row">
              <span>{t('bot_api_key')}</span>
              <code className="oc-api-key-value">{createdBot.api_key}</code>
              <button className="oc-btn oc-btn-default" onClick={() => handleCopy('api_key', createdBot.api_key)}>
                {copiedField === 'api_key' ? t('bot_copied') : t('bot_copy')}
              </button>
            </div>
            <div className="oc-credential-row">
              <span>{t('bot_ws_url')}</span>
              <code className="oc-api-key-value">{wsUrl}</code>
              <button className="oc-btn oc-btn-default" onClick={() => handleCopy('ws_url', wsUrl)}>
                {copiedField === 'ws_url' ? t('bot_copied') : t('bot_copy')}
              </button>
            </div>
            {friendStatus && (
              <div className={`oc-bot-inline-note ${friendSuccess ? 'success' : 'warning'}`}>
                {friendStatus}
              </div>
            )}
            <div className="oc-bot-inline-note warning">
              {createdMode === CREATE_MODES.MANAGED ? t('bot_managed_note') : t('bot_api_key_note')}
            </div>
          </div>
          <button className="oc-banner-close" onClick={() => {
            setCreatedBot(null);
            setFriendStatus('');
          }}>×</button>
        </div>
      )}

      {error && (
        <div className="oc-bot-error">{error}</div>
      )}

      <div className="oc-bot-list">
        {bots.length === 0 ? (
          <div className="oc-empty-state">
            <div className="oc-empty-icon">CPU</div>
            <div>{t('no_bots')}</div>
            <div className="oc-empty-subtitle">{t('bot_empty_hint')}</div>
          </div>
        ) : (
          bots.map((bot) => (
            <div key={bot.id} className="oc-bot-card">
              <div className="oc-bot-header">
                <div className="oc-bot-avatar">B</div>
                <div className="oc-bot-info">
                  <div className="oc-bot-name">{bot.display_name || bot.username}</div>
                  <div className="oc-bot-username">
                    @{bot.username}
                    {bot.tenant_name && (
                      <span className="oc-bot-badge managed">{t('bot_managed_badge')}</span>
                    )}
                  </div>
                </div>
                <div className={`oc-bot-status ${bot.visibility === 'public' ? 'enabled' : 'disabled'}`}>
                  {bot.visibility === 'public' ? t('bot_visibility_public') : t('bot_visibility_private')}
                </div>
              </div>

              <div className="oc-bot-details">
                <div className="oc-bot-detail-row">
                  <span>{t('bot_connection_mode')}</span>
                  <strong>{bot.tenant_name ? t('bot_connection_managed') : t('bot_connection_self_hosted')}</strong>
                </div>
                {bot.tenant_name && (
                  <div className="oc-bot-detail-row">
                    <span>{t('bot_tenant_label')}</span>
                    <code>{bot.tenant_name}</code>
                  </div>
                )}
              </div>

              <div className="oc-bot-actions">
                <button
                  className="oc-btn oc-btn-default"
                  onClick={() => handleToggleVisibility(bot)}
                >
                  {bot.visibility === 'public' ? t('bot_make_private') : t('bot_make_public')}
                </button>
                <button
                  className="oc-btn oc-btn-danger"
                  onClick={() => handleDelete(bot)}
                >
                  {t('delete')}
                </button>
              </div>
            </div>
          ))
        )}
      </div>

      {showCreate && (
        <div className="oc-modal-overlay" onClick={() => setShowCreate(false)}>
          <div className="oc-modal" onClick={(e) => e.stopPropagation()}>
            <div className="oc-modal-header">
              <h3>{t('register_bot')}</h3>
              <button onClick={() => setShowCreate(false)}>×</button>
            </div>
            <form className="oc-modal-body" onSubmit={handleCreate}>
              <div className="oc-mode-switch">
                <button
                  type="button"
                  className={`oc-mode-option ${createMode === CREATE_MODES.SELF_HOSTED ? 'active' : ''}`}
                  onClick={() => setCreateMode(CREATE_MODES.SELF_HOSTED)}
                >
                  <span>{t('bot_mode_self_hosted')}</span>
                  <small>{t('bot_mode_self_hosted_desc')}</small>
                </button>
                <button
                  type="button"
                  className={`oc-mode-option ${createMode === CREATE_MODES.MANAGED ? 'active' : ''}`}
                  onClick={() => setCreateMode(CREATE_MODES.MANAGED)}
                >
                  <span>{t('bot_mode_managed')}</span>
                  <small>{t('bot_mode_managed_desc')}</small>
                </button>
              </div>
              <div className="oc-form-group">
                <label>{t('bot_display_name')}</label>
                <input
                  type="text"
                  value={createForm.display_name}
                  onChange={(e) => setCreateForm({ ...createForm, display_name: e.target.value })}
                  placeholder={t('bot_display_name_placeholder')}
                  required
                />
              </div>
              <div className="oc-bot-inline-note">
                {createMode === CREATE_MODES.MANAGED ? t('bot_mode_managed_desc') : t('bot_mode_self_hosted_desc')}
              </div>
              {error && (
                <div className="oc-bot-error compact">{error}</div>
              )}
              <div className="oc-bot-inline-note">
                {t('bot_username_hint')}
              </div>
              <div className="oc-modal-footer">
                <button type="button" className="oc-btn oc-btn-default" onClick={() => setShowCreate(false)}>
                  {t('cancel')}
                </button>
                <button type="submit" className="oc-btn oc-btn-primary" disabled={isSubmitting}>
                  {isSubmitting ? t('loading') : t('bot_create_submit')}
                </button>
              </div>
            </form>
          </div>
        </div>
      )}
    </div>
  );
}

function buildBotUsername(displayName) {
  const slug = displayName
    .trim()
    .toLowerCase()
    .replace(/\s+/g, '-')
    .replace(/[^a-z0-9-]/g, '')
    .replace(/-+/g, '-')
    .replace(/^-|-$/g, '');
  const base = (slug || 'bot').slice(0, 16);
  const suffix = Math.floor(Math.random() * 9000) + 1000;
  return `bot-${base}-${suffix}`;
}
