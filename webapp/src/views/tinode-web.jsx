import React, { useState, useEffect, useCallback } from 'react';
import { api, setToken, getToken, connectWS, disconnectWS } from '../api';
import t from '../i18n';
import ChatListView from './sidepanel-view';
import FriendsView from './friends-view';
import MessagesView from './messages-view';
import ProfileEditor from '../widgets/profile-editor';
import FeedbackModal from '../widgets/feedback-modal';
import CatsCoDownloadModal from '../widgets/catsco-download-modal';
import RelayAccessModal from '../widgets/relay-access-modal';
import PasswordResetForm from '../widgets/password-reset-form';
import Avatar from '../widgets/avatar';
import { Bug, Download, KeyRound, Settings, LogOut, Eye, EyeOff } from 'lucide-react';
import CatOrb from '../components/CatOrb/CatOrb';
import '../css/openchat-theme.css';

const TABS = {
  CHATS: 'chats'
};

function normalizeUserProfile(raw) {
  if (!raw) return null;
  const username = raw.username || '';
  return {
    uid: raw.uid || raw.id,
    username,
    email: raw.email || '',
    display_name: raw.display_name || username,
    avatar_url: raw.avatar_url || '',
    account_type: raw.account_type || 'human',
  };
}

function getInitialUser() {
  const token = getToken();
  if (!token) return null;

  try {
    const saved = localStorage.getItem('oc_user');
    return saved ? normalizeUserProfile(JSON.parse(saved)) : null;
  } catch (error) {
    console.warn('Failed to restore saved user from localStorage:', error);
    localStorage.removeItem('oc_user');
    return null;
  }
}

function lastTopicStorageKey(uid) {
  return uid ? `v3_last_topic:${uid}` : 'v3_last_topic';
}

function normalizeActiveTopic(value) {
  if (!value) return null;

  if (typeof value === 'string') {
    if (!value || value === '[object Object]') return null;
    return { topicId: value, name: '' };
  }

  if (typeof value === 'object' && value.topicId) {
    return {
      topicId: value.topicId,
      name: value.name || '',
      isGroup: Boolean(value.isGroup),
      groupId: value.groupId,
      avatar_url: value.avatar_url || '',
      friendId: value.friendId,
    };
  }

  return null;
}

function readStoredTopic(uid) {
  const keys = [lastTopicStorageKey(uid), 'v3_last_topic'];
  for (const key of keys) {
    const raw = localStorage.getItem(key);
    if (!raw) continue;

    try {
      const parsed = JSON.parse(raw);
      const topic = normalizeActiveTopic(parsed);
      if (topic) return topic;
    } catch (error) {
      const topic = normalizeActiveTopic(raw);
      if (topic) return topic;
    }
  }

  return null;
}

function writeStoredTopic(uid, topic) {
  const key = lastTopicStorageKey(uid);
  const normalized = normalizeActiveTopic(topic);
  if (!normalized) {
    localStorage.removeItem(key);
    localStorage.removeItem('v3_last_topic');
    return;
  }

  localStorage.setItem(key, JSON.stringify(normalized));
  localStorage.setItem('v3_last_topic', JSON.stringify(normalized));
}

export default function TinodeWeb() {
  const [user, setUser] = useState(() => getInitialUser());
  const [activeTab, setActiveTab] = useState(TABS.CHATS);
  const [activeTopic, _setActiveTopic] = useState(null);

  const setActiveTopic = useCallback((nextValue) => {
    _setActiveTopic((prev) => {
      const next = typeof nextValue === 'function' ? nextValue(prev) : nextValue;
      const normalized = normalizeActiveTopic(next);
      writeStoredTopic(user?.uid, normalized);
      return normalized;
    });
  }, [user?.uid]);
  const [authMode, setAuthMode] = useState('login');
  const [onlineUsers, setOnlineUsers] = useState({});
  const [wsStatus, setWsStatus] = useState('disconnected');
  const [showProfileEditor, setShowProfileEditor] = useState(false);
  const [showProfilePopover, setShowProfilePopover] = useState(false);
  const [showFeedbackModal, setShowFeedbackModal] = useState(false);
  const [showDownloadModal, setShowDownloadModal] = useState(false);
  const [showRelayModal, setShowRelayModal] = useState(false);



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

  useEffect(() => {
    if (!user?.uid) {
      _setActiveTopic(null);
      return;
    }

    _setActiveTopic(readStoredTopic(user.uid));
  }, [user?.uid]);

  useEffect(() => {
    if (!user?.uid) return;

    let cancelled = false;
    api.getMe()
      .then((profile) => {
        if (!cancelled) {
          const normalized = normalizeUserProfile(profile);
          if (normalized) persistUser(normalized);
        }
      })
      .catch((error) => {
        console.warn('Failed to refresh current user profile:', error);
      });

    return () => {
      cancelled = true;
    };
  }, [user?.uid, persistUser]);

  useEffect(() => {
    if (!user?.uid) return;

    const params = new URLSearchParams(window.location.search);
    if (params.get('relay_login') !== '1') return;

    let cancelled = false;
    const fallBackToRelayPanel = () => {
      params.delete('relay_login');
      const nextSearch = params.toString();
      const nextUrl = `${window.location.pathname}${nextSearch ? `?${nextSearch}` : ''}${window.location.hash}`;
      window.history.replaceState(null, '', nextUrl);
      setShowRelayModal(true);
    };

    api.createRelaySession()
      .then((session) => {
        if (!cancelled && session?.url) {
          window.location.href = session.url;
        } else if (!cancelled) {
          fallBackToRelayPanel();
        }
      })
      .catch((error) => {
        console.warn('Failed to create relay login session:', error);
        if (!cancelled) {
          fallBackToRelayPanel();
        }
      });

    return () => {
      cancelled = true;
    };
  }, [user?.uid]);

  const handleLogin = async (account, password) => {
    const res = await api.login({ account, password });
    setToken(res.token);
    persistUser(normalizeUserProfile(res));
  };

  const handleRegister = async (email, password, loginName, code) => {
    const username = loginName.trim();
    if (!username) {
      throw new Error('请输入登录名称');
    }
    if (username.length < 3) {
      throw new Error('登录名称至少 3 个字符');
    }
    await api.register({
      email,
      username,
      password,
      code,
    });
    await handleLogin(email, password);
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
    persistUser(normalizeUserProfile(nextUser));
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
          <div className="v3-brand-title" style={{fontSize: 20, fontWeight: 700}}>CatsCo</div>
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
            <div className="v3-popover-item" onClick={() => { setShowProfilePopover(false); setShowFeedbackModal(true); }}>
              <Bug size={16} style={{marginRight: 10}} /> 问题反馈与建议
            </div>
            <div className="v3-popover-item" onClick={() => { setShowProfilePopover(false); setShowDownloadModal(true); }}>
              <Download size={16} style={{marginRight: 10}} /> 下载 CatsCo 桌面端
            </div>
            <div className="v3-popover-item" onClick={() => { setShowProfilePopover(false); setShowRelayModal(true); }}>
              <KeyRound size={16} style={{marginRight: 10}} /> CatsCo 中转站
            </div>
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
          onOpenRelay={() => setShowRelayModal(true)}
        />
      )}

      {showFeedbackModal && (
        <FeedbackModal user={user} onClose={() => setShowFeedbackModal(false)} />
      )}

      {showDownloadModal && (
        <CatsCoDownloadModal onClose={() => setShowDownloadModal(false)} />
      )}

      {showRelayModal && (
        <RelayAccessModal onClose={() => setShowRelayModal(false)} />
      )}
    </div>
  );
}

function SidebarContent({ activeTopic, onSelectTopic, user, onlineUsers }) {
  return <ChatListView activeTopic={activeTopic} onSelectTopic={onSelectTopic} user={user} onlineUsers={onlineUsers} />;
}

function ProfileFooter({ user, wsStatus, onTogglePopover }) {
  const statusClass = wsStatus === 'connected' ? 'online' : 'offline';
  const displayName = user.display_name || user.username;
  return (
    <div className="v3-profile-footer" onClick={onTogglePopover} style={{cursor: 'pointer'}}>
      <Avatar name={displayName} src={user.avatar_url} size={32} className="v3-profile-avatar" />
      <div className="v3-profile-info">
        <div className="v3-profile-name">{displayName}</div>
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

function formatAuthError(message) {
  const text = String(message || '').toLowerCase();
  if (text.includes('user not found')) return '账号不存在，请检查用户名或邮箱';
  if (text.includes('password mismatch')) return '密码错误，请重试';
  if (text.includes('username taken')) return '登录名称已被占用，请换一个';
  if (text.includes('email already')) return '该邮箱已经注册，请直接登录';
  if (text.includes('invalid or expired verification code')) return '验证码无效或已过期';
  if (text.includes('username min 3')) return '登录名称至少 3 个字符';
  if (text.includes('password min 6')) return '密码至少 6 位';
  if (text.includes('failed to send verification code')) return '发送验证码失败，请稍后再试';
  return message || '操作失败，请稍后再试';
}

function AuthView({ mode, setMode, onLogin, onRegister }) {
  const [username, setUsername] = useState('');
  const [email, setEmail] = useState('');
  const [password, setPassword] = useState('');
  const [showPassword, setShowPassword] = useState(false);
  const [loginName, setLoginName] = useState('');
  const [code, setCode] = useState('');
  const [error, setError] = useState('');
  const [codeSent, setCodeSent] = useState(false);
  const [countdown, setCountdown] = useState(0);

  useEffect(() => {
    if (countdown > 0) {
      const timer = setTimeout(() => setCountdown(countdown - 1), 1000);
      return () => clearTimeout(timer);
    }
  }, [countdown]);

  const handleSendCode = async () => {
    if (!email || !/^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(email)) {
      setError('请输入有效的邮箱地址');
      return;
    }
    try {
      await api.sendVerificationCode(email);
      setCodeSent(true);
      setCountdown(60);
      setError('');
    } catch (err) {
      setError(err.message || '发送验证码失败，请稍后再试');
    }
  };

  const handleSubmit = async (e) => {
    e.preventDefault();
    setError('');
    try {
      if (mode === 'login') {
        await onLogin(username, password);
      } else {
        await onRegister(email, password, loginName, code);
      }
    } catch (err) {
      setError(formatAuthError(err.message));
    }
  };

  const authShell = (content) => (
    <div className="oc-auth">
      <div className="oc-auth-cat">
        <CatOrb hue={0} backgroundColor="#050505" hoverIntensity={0.3} rotateOnHover={false} />
      </div>
      {content}
    </div>
  );

  if (mode === 'reset') {
    return authShell(
      <div className="oc-auth-card">
        <div className="oc-auth-logo">CatsCo</div>
        <div className="oc-settings-secondary" style={{ marginBottom: 14 }}>
          输入注册邮箱，验证后设置新密码。
        </div>
        <PasswordResetForm />
        <div className="oc-auth-link">
          <span>想起密码了？<a href="#" onClick={(e) => { e.preventDefault(); setMode('login'); }}>返回登录</a></span>
        </div>
      </div>
    );
  }

  return authShell(
    <form className="oc-auth-card" onSubmit={handleSubmit}>
      <div className="oc-auth-logo">CatsCo</div>
      {error && <div style={{ color: '#FA5151', marginBottom: 12, fontSize: 13 }}>{error}</div>}

      {mode === 'login' ? (
        <>
          <input
            className="oc-auth-input"
            placeholder={t('username')}
            value={username}
            onChange={(e) => setUsername(e.target.value)}
          />
          <div style={{ position: 'relative' }}>
            <input
              className="oc-auth-input"
              type={showPassword ? 'text' : 'password'}
              placeholder={t('password')}
              value={password}
              onChange={(e) => setPassword(e.target.value)}
              style={{ paddingRight: 48 }}
            />
            <span
              onClick={() => setShowPassword(!showPassword)}
              style={{ position: 'absolute', right: 12, top: '40%', transform: 'translateY(-50%)', cursor: 'pointer', color: '#888', userSelect: 'none', display: 'flex', alignItems: 'center' }}
            >
              {showPassword ? <EyeOff size={16} /> : <Eye size={16} />}
            </span>
          </div>
        </>
      ) : (
        <>
          <input
            className="oc-auth-input"
            type="email"
            placeholder="邮箱地址"
            value={email}
            onChange={(e) => setEmail(e.target.value)}
          />
          <div style={{ display: 'flex', gap: '8px' }}>
            <input
              className="oc-auth-input"
              placeholder="邮箱验证码"
              value={code}
              onChange={(e) => setCode(e.target.value)}
              style={{ flex: 1 }}
            />
            <button
              type="button"
              className="oc-auth-btn"
              onClick={handleSendCode}
              disabled={countdown > 0}
              style={{ width: '120px', fontSize: '13px' }}
            >
              {countdown > 0 ? `${countdown}秒` : '发送验证码'}
            </button>
          </div>
          <input
            className="oc-auth-input"
            placeholder="登录名称（可用于登录）"
            value={loginName}
            onChange={(e) => setLoginName(e.target.value)}
          />
          <div style={{ position: 'relative' }}>
            <input
              className="oc-auth-input"
              type={showPassword ? 'text' : 'password'}
              placeholder="设置密码（至少6位）"
              value={password}
              onChange={(e) => setPassword(e.target.value)}
              style={{ paddingRight: 48 }}
            />
            <span
              onClick={() => setShowPassword(!showPassword)}
              style={{ position: 'absolute', right: 12, top: '40%', transform: 'translateY(-50%)', cursor: 'pointer', color: '#888', userSelect: 'none', display: 'flex', alignItems: 'center' }}
            >
              {showPassword ? <EyeOff size={16} /> : <Eye size={16} />}
            </span>
          </div>
        </>
      )}

      <button className="oc-auth-btn" type="submit">
        {mode === 'login' ? t('login') : t('register')}
      </button>
      <div className="oc-auth-link">
        {mode === 'login' ? (
          <>
            <span>还没有账号？<a href="#" onClick={(e) => { e.preventDefault(); setMode('register'); }}>立即注册</a></span>
            <span style={{ marginLeft: 12 }}>
              <a href="#" onClick={(e) => { e.preventDefault(); setMode('reset'); }}>忘记密码？</a>
            </span>
          </>
        ) : (
          <span>已有账号？<a href="#" onClick={(e) => { e.preventDefault(); setMode('login'); }}>立即登录</a></span>
        )}
      </div>
    </form>
  );
}

function parseUid(uidStr) {
  if (!uidStr) return 0;
  if (uidStr.startsWith('usr')) {
    return parseInt(uidStr.slice(3), 10) || 0;
  }
  return parseInt(uidStr, 10) || 0;
}
