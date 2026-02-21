import React, { useState, useEffect } from 'react';
import { api } from '../api';
import t from '../i18n';

export default function BotAdminView({ onBack }) {
  const [bots, setBots] = useState([]);
  const [stats, setStats] = useState({});
  const [loading, setLoading] = useState(true);
  const [showRegister, setShowRegister] = useState(false);
  const [registerForm, setRegisterForm] = useState({
    username: '',
    display_name: '',
    password: '',
    model: 'gpt-3.5-turbo',
  });
  const [newApiKey, setNewApiKey] = useState(null);

  useEffect(() => {
    loadBots();
  }, []);

  const loadBots = async () => {
    try {
      const [botsRes, statsRes] = await Promise.all([
        fetch(`${process.env.REACT_APP_API_BASE || ''}/api/admin/bots`, {
          headers: { Authorization: `Bearer ${localStorage.getItem('oc_token')}` },
        }).then(r => r.json()),
        fetch(`${process.env.REACT_APP_API_BASE || ''}/api/admin/bots/stats`, {
          headers: { Authorization: `Bearer ${localStorage.getItem('oc_token')}` },
        }).then(r => r.json()),
      ]);
      setBots(botsRes.bots || []);
      setStats(statsRes.bots || {});
      setLoading(false);
    } catch (e) {
      console.error('Load bots error:', e);
      setLoading(false);
    }
  };

  const handleRegister = async (e) => {
    e.preventDefault();
    try {
      const res = await fetch(`${process.env.REACT_APP_API_BASE || ''}/api/admin/bots/register`, {
        method: 'POST',
        headers: {
          'Content-Type': 'application/json',
          Authorization: `Bearer ${localStorage.getItem('oc_token')}`,
        },
        body: JSON.stringify(registerForm),
      });
      const data = await res.json();
      if (!res.ok) throw new Error(data.error);
      setNewApiKey(data.api_key);
      setShowRegister(false);
      setRegisterForm({ username: '', display_name: '', password: '', model: 'gpt-3.5-turbo' });
      loadBots();
    } catch (e) {
      alert('Registration failed: ' + e.message);
    }
  };

  const handleToggle = async (uid) => {
    try {
      await fetch(`${process.env.REACT_APP_API_BASE || ''}/api/admin/bots/toggle?uid=${uid}`, {
        method: 'POST',
        headers: { Authorization: `Bearer ${localStorage.getItem('oc_token')}` },
      });
      loadBots();
    } catch (e) {
      alert('Toggle failed');
    }
  };

  const handleRotateKey = async (uid) => {
    if (!window.confirm('Rotate API key? The old key will be invalidated.')) return;
    try {
      const res = await fetch(`${process.env.REACT_APP_API_BASE || ''}/api/admin/bots/rotate-key?uid=${uid}`, {
        method: 'POST',
        headers: { Authorization: `Bearer ${localStorage.getItem('oc_token')}` },
      });
      const data = await res.json();
      if (!res.ok) throw new Error(data.error);
      setNewApiKey(data.api_key);
      alert('New API Key generated! Copy it now - it won\'t be shown again.');
    } catch (e) {
      alert('Rotate failed: ' + e.message);
    }
  };

  if (loading) {
    return <div className="oc-loading">{t('loading') || 'Loading...'}</div>;
  }

  return (
    <div className="oc-bot-admin">
      <div className="oc-header">
        <button className="oc-back-btn" onClick={onBack}>←</button>
        {t('bot_admin') || 'Bot Management'}
        <button className="oc-header-action" onClick={() => setShowRegister(true)}>+</button>
      </div>

      {newApiKey && (
        <div className="oc-api-key-banner">
          <div className="oc-api-key-label">New API Key (copy now!):</div>
          <code className="oc-api-key-value">{newApiKey}</code>
          <button onClick={() => { navigator.clipboard.writeText(newApiKey); }}>Copy</button>
          <button onClick={() => setNewApiKey(null)}>×</button>
        </div>
      )}

      <div className="oc-bot-list">
        {bots.length === 0 ? (
          <div className="oc-empty-state">
            {t('no_bots') || 'No bots registered yet'}
          </div>
        ) : (
          bots.map((bot) => (
            <div key={bot.user_id} className="oc-bot-card">
              <div className="oc-bot-header">
                <div className="oc-bot-avatar">🤖</div>
                <div className="oc-bot-info">
                  <div className="oc-bot-name">{bot.display_name || bot.username}</div>
                  <div className="oc-bot-username">@{bot.username}</div>
                </div>
                <div className={`oc-bot-status ${bot.enabled ? 'enabled' : 'disabled'}`}>
                  {bot.enabled ? 'Enabled' : 'Disabled'}
                </div>
              </div>

              <div className="oc-bot-stats">
                <div className="oc-stat">
                  <span className="oc-stat-label">Messages</span>
                  <span className="oc-stat-value">{stats[bot.user_id]?.messages_sent || 0}</span>
                </div>
                <div className="oc-stat">
                  <span className="oc-stat-label">Model</span>
                  <span className="oc-stat-value">{bot.model || '-'}</span>
                </div>
              </div>

              <div className="oc-bot-actions">
                <button
                  className={`oc-btn ${bot.enabled ? 'oc-btn-warning' : 'oc-btn-success'}`}
                  onClick={() => handleToggle(bot.user_id)}
                >
                  {bot.enabled ? 'Disable' : 'Enable'}
                </button>
                <button
                  className="oc-btn oc-btn-default"
                  onClick={() => handleRotateKey(bot.user_id)}
                >
                  Rotate Key
                </button>
              </div>

              <div className="oc-bot-api-key">
                <span className="oc-api-key-label">API Key:</span>
                <code>{bot.api_key ? `${bot.api_key.slice(0, 12)}...` : 'Not set'}</code>
              </div>
            </div>
          ))
        )}
      </div>

      {showRegister && (
        <div className="oc-modal-overlay" onClick={() => setShowRegister(false)}>
          <div className="oc-modal" onClick={(e) => e.stopPropagation()}>
            <div className="oc-modal-header">
              <h3>{t('register_bot') || 'Register New Bot'}</h3>
              <button onClick={() => setShowRegister(false)}>×</button>
            </div>
            <form className="oc-modal-body" onSubmit={handleRegister}>
              <div className="oc-form-group">
                <label>Username</label>
                <input
                  type="text"
                  value={registerForm.username}
                  onChange={(e) => setRegisterForm({ ...registerForm, username: e.target.value })}
                  placeholder="my_bot"
                  required
                  minLength={3}
                />
              </div>
              <div className="oc-form-group">
                <label>Display Name</label>
                <input
                  type="text"
                  value={registerForm.display_name}
                  onChange={(e) => setRegisterForm({ ...registerForm, display_name: e.target.value })}
                  placeholder="My Bot"
                />
              </div>
              <div className="oc-form-group">
                <label>Password</label>
                <input
                  type="password"
                  value={registerForm.password}
                  onChange={(e) => setRegisterForm({ ...registerForm, password: e.target.value })}
                  placeholder="Min 6 characters"
                  required
                  minLength={6}
                />
              </div>
              <div className="oc-form-group">
                <label>LLM Model</label>
                <input
                  type="text"
                  value={registerForm.model}
                  onChange={(e) => setRegisterForm({ ...registerForm, model: e.target.value })}
                  placeholder="gpt-3.5-turbo"
                />
              </div>
              <div className="oc-modal-footer">
                <button type="button" className="oc-btn oc-btn-default" onClick={() => setShowRegister(false)}>
                  Cancel
                </button>
                <button type="submit" className="oc-btn oc-btn-primary">
                  Register
                </button>
              </div>
            </form>
          </div>
        </div>
      )}
    </div>
  );
}
