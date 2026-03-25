import React, { useState, useEffect, useCallback } from 'react';
import { api, setToken, getToken, connectWS, disconnectWS } from '../api';
import t from '../i18n';
import ChatListView from './sidepanel-view';
import FriendsView from './friends-view';
import MessagesView from './messages-view';
import ProfileEditor from '../widgets/profile-editor';
import { Settings, LogOut } from 'lucide-react';
import '../css/openchat-theme.css';

const TABS = {
  CHATS: 'chats'
};

export default function TinodeWeb() {
  const [user, setUser] = useState(null);
  const [activeTab, setActiveTab] = useState(TABS.CHATS);
  const [activeTopic, _setActiveTopic] = useState(() => localStorage.getItem('v3_last_topic') || null);

  const setActiveTopic = useCallback((topicId) => {
    _setActiveTopic(topicId);
    if (topicId) {
      localStorage.setItem('v3_last_topic', topicId);
    } else {
      localStorage.removeItem('v3_last_topic');
    }
  }, []);
  const [authMode, setAuthMode] = useState('login');
  const [onlineUsers, setOnlineUsers] = useState({});
  const [wsStatus, setWsStatus] = useState('disconnected');
  const [showProfileEditor, setShowProfileEditor] = useState(false);
  const [showProfilePopover, setShowProfilePopover] = useState(false);

  // Restore session
  useEffect(() => {
    const token = getToken();
    if (token) {
      const saved = localStorage.getItem('oc_user');
      if (saved) setUser(JSON.parse(saved));
    }
  }, []);

  const persistUser = useCallback((nextUser) => {
    localStorage.setItem('oc_user', JSON.stringify(nextUser));
    setUser(nextUser);
  }, []);

  // WebSocket message handler
  const handleWSMessage = useCallback((msg) => {
    if (msg._type === 'ws_open') {
      setWsStatus('connected');
      return;
    }
    if (msg._type === 'ws_close') {
      setWsStatus('reconnecting');
      return;
    }

    if (msg.meta && msg.meta.sub) {
      const online = {};
      for (const u of msg.meta.sub) {
        if (u.uid && u.online) {
          online[u.uid] = true;
        }
      }
      setOnlineUsers((prev) => ({ ...prev, ...online }));
    }

    if (msg.pres) {
      const uid = parseUid(msg.pres.src);
      if (uid > 0) {
        setOnlineUsers((prev) => {
          const next = { ...prev };
          if (msg.pres.what === 'on') {
            next[uid] = true;
          } else if (msg.pres.what === 'off') {
            delete next[uid];
          }
          return next;
        });
      }
    }
  }, []);

  useEffect(() => {
    if (user) {
      connectWS(handleWSMessage);
    }
    return () => {
      if (user) disconnectWS();
    };
  }, [user, handleWSMessage]);

  const handleLogin = async (username, password) => {
    const res = await api.login({ username, password });
    setToken(res.token);
    persistUser({
      uid: res.uid,
      username: res.username,
      display_name: res.display_name || res.username,
      avatar_url: res.avatar_url || '',
      account_type: res.account_type || 'human',
    });
  };

  const handleRegister = async (username, password, displayName) => {
    await api.register({ username, password, display_name: displayName });
    await handleLogin(username, password);
  };

  const handleLogout = () => {
    disconnectWS();
    setToken(null);
    localStorage.removeItem('oc_user');
    setUser(null);
    setOnlineUsers({});
    setActiveTopic(null);
  };

  const handleUserUpdated = (nextUser) => {
    persistUser({
      uid: nextUser.uid,
      username: nextUser.username,
      display_name: nextUser.display_name || nextUser.username,
      avatar_url: nextUser.avatar_url || '',
      account_type: nextUser.account_type || 'human',
    });
    window.dispatchEvent(new Event('cc:data-changed'));
  };

  const handleTopicUpdated = (nextTopic) => {
    setActiveTopic((prev) => {
      if (!prev || prev.topicId !== nextTopic.topicId) return prev;
      return { ...prev, ...nextTopic };
    });
  };

  if (!user) {
    return <AuthView mode={authMode} setMode={setAuthMode} onLogin={handleLogin} onRegister={handleRegister} />;
  }

  return (
    <div className="v3-app">
      <div className="v3-sidebar">
        <div className="v3-sidebar-header">
          <div className="v3-brand-title">CatsCo</div>
        </div>
        
        <SidebarContent
          activeTopic={activeTopic ? activeTopic.topicId : null}
          onSelectTopic={(topic) => {
            setActiveTopic(topic);
          }}
          user={user}
          onlineUsers={onlineUsers}
        />
        
        <ProfileFooter 
          user={user} 
          wsStatus={wsStatus} 
          onTogglePopover={() => setShowProfilePopover(!showProfilePopover)}
        />

        {showProfilePopover && (
          <div className="v3-profile-popover">
            <div className="v3-popover-item" onClick={() => { setShowProfilePopover(false); setShowProfileEditor(true); }}>
              <Settings size={16} style={{marginRight: 10}} /> Settings & Profile
            </div>
            <div className="v3-popover-item danger" onClick={() => { localStorage.clear(); window.location.reload(); }}>
              <LogOut size={16} style={{marginRight: 10}} /> Sign Out
            </div>
          </div>
        )}
      </div>
      
      <div className="v3-main">
        {activeTopic ? (
          <MessagesView
            topic={activeTopic.topicId}
            topicName={activeTopic.name}
            user={user}
            isGroup={activeTopic.isGroup || (activeTopic.topicId && activeTopic.topicId.startsWith('grp_'))}
            groupId={activeTopic.groupId}
            topicAvatarUrl={activeTopic.avatar_url}
            onTopicUpdated={handleTopicUpdated}
          />
        ) : (
          <div style={{ flex: 1, display: 'flex', alignItems: 'center', justifyContent: 'center', color: '#888' }}>
            {t('chats_empty')}
          </div>
        )}
      </div>

      {showProfileEditor && (
        <ProfileEditor
          user={user}
          onClose={() => setShowProfileEditor(false)}
          onSaved={handleUserUpdated}
        />
      )}
    </div>
  );
}

function SidebarContent({ activeTopic, onSelectTopic, user, onlineUsers }) {
  return <ChatListView activeTopic={activeTopic} onSelectTopic={onSelectTopic} user={user} onlineUsers={onlineUsers} />;
}

function ProfileFooter({ user, wsStatus, onTogglePopover }) {
  const statusClass = wsStatus === 'connected' ? 'online' : 'offline';
  return (
    <div className="v3-profile-footer" onClick={onTogglePopover} style={{cursor: 'pointer'}}>
      <div className="v3-profile-avatar">
        {user.display_name ? user.display_name.charAt(0).toUpperCase() : 'U'}
      </div>
      <div className="v3-profile-info">
        <div className="v3-profile-name">{user.display_name || user.username}</div>
        <div className="v3-profile-roles">
           <span className={`v3-status-dot ${statusClass}`} style={{marginLeft: 0, marginRight: 6}}></span>
           {wsStatus === 'connected' ? 'Online' : 'Offline'}
        </div>
      </div>
      <div className="v3-profile-settings" style={{color: '#888'}}>
        <Settings size={18} />
      </div>
    </div>
  );
}

function AuthView({ mode, setMode, onLogin, onRegister }) {
  const [username, setUsername] = useState('');
  const [password, setPassword] = useState('');
  const [displayName, setDisplayName] = useState('');
  const [error, setError] = useState('');

  const handleSubmit = async (e) => {
    e.preventDefault();
    setError('');
    try {
      if (mode === 'login') {
        await onLogin(username, password);
      } else {
        await onRegister(username, password, displayName);
      }
    } catch (err) {
      setError(err.message);
    }
  };

  return (
    <div className="oc-auth">
      <form className="oc-auth-card" onSubmit={handleSubmit}>
        <div className="oc-auth-logo">CatsCo</div>
        {error && <div style={{ color: '#FA5151', marginBottom: 12, fontSize: 13 }}>{error}</div>}
        <input
          className="oc-auth-input"
          placeholder={t('username')}
          value={username}
          onChange={(e) => setUsername(e.target.value)}
        />
        {mode === 'register' && (
          <input
            className="oc-auth-input"
            placeholder={t('display_name')}
            value={displayName}
            onChange={(e) => setDisplayName(e.target.value)}
          />
        )}
        <input
          className="oc-auth-input"
          type="password"
          placeholder={t('password')}
          value={password}
          onChange={(e) => setPassword(e.target.value)}
        />
        <button className="oc-auth-btn" type="submit">
          {mode === 'login' ? t('login') : t('register')}
        </button>
        <div className="oc-auth-link">
          {mode === 'login' ? (
            <span>{t('register')} <a href="#" onClick={(e) => { e.preventDefault(); setMode('register'); }}>{t('register')}</a></span>
          ) : (
            <span>{t('login')} <a href="#" onClick={(e) => { e.preventDefault(); setMode('login'); }}>{t('login')}</a></span>
          )}
        </div>
      </form>
    </div>
  );
}

function parseUid(uidStr) {
  if (!uidStr) return 0;
  if (uidStr.startsWith('usr')) {
    return parseInt(uidStr.slice(3), 10) || 0;
  }
  return parseInt(uidStr, 10) || 0;
}
