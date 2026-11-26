import React, { useCallback, useEffect, useRef, useState } from 'react';
import { UsersRound } from 'lucide-react';
import { api } from '../api';
import { useFeedback } from '../components/feedback-system';

const SOURCES = {
  friends: async id => (await api.getBotFriends(id)).friends || [],
  requests: async id => (await api.getPendingRequests(id)).requests || [],
  bindings: async id => (await api.getAgentChannelBindings(id)).bindings || [],
};

function useDirectoryList(id, source) {
  const [state, setState] = useState({ rows: [], loaded: false, loading: true, failed: false });
  const sequence = useRef(0);
  const refresh = useCallback(async () => {
    const request = ++sequence.current;
    setState(previous => ({ ...previous, loading: true, failed: false }));
    try {
      const rows = await source(id);
      if (request === sequence.current) setState({ rows, loaded: true, loading: false, failed: false });
    } catch {
      if (request === sequence.current) setState(previous => ({ ...previous, loading: false, failed: true }));
    }
  }, [id, source]);
  useEffect(() => {
    void refresh();
    return () => { sequence.current += 1; };
  }, [refresh]);
  return { ...state, refresh };
}

function ListStatus({ list, name, empty }) {
  if (list.failed) return <p role="alert">{name}加载失败。 <button type="button" className="oc-btn oc-btn-default" onClick={list.refresh}>重试</button></p>;
  if (list.loading && !list.loaded) return <p role="status">正在读取{name}…</p>;
  return list.rows.length === 0 ? <p>{empty}</p> : null;
}

const statusNames = { approved: '已绑定', active: '已绑定', rejected: '未通过', pending: '待审批', pending_approval: '待审批', needs_login: '待登录', pending_login: '待登录' };

export default function AgentFriendAccess({ botId, onAccessChanged, channelLabel, children }) {
  const feedback = useFeedback();
  const friends = useDirectoryList(botId, SOURCES.friends);
  const requests = useDirectoryList(botId, SOURCES.requests);
  const bindings = useDirectoryList(botId, SOURCES.bindings);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const byUser = new Map();
  const friendIds = new Set(friends.rows.map(person => String(person.id || person.uid)));
  const otherBindings = [];
  for (const item of bindings.rows) {
    const uid = String(item.canonical_uid || item.user?.uid || item.binding?.canonical_uid || '');
    if (friendIds.has(uid)) {
      if (!byUser.has(uid)) byUser.set(uid, []);
      byUser.get(uid).push(item);
    } else {
      otherBindings.push(item);
    }
  }
  function bindingLabel(item) {
    const state = item.status || item.binding?.status;
    return channelLabel(item.channel || item.binding?.channel) + ' · ' + (statusNames[state] || state || '状态未知');
  }
  async function update(action, uid, name) {
    if (busy) return;
    if (action === 'remove' && !await feedback.confirm({
      title: `移除“${name}”的使用权限？`, message: '移除后，该用户将失去这个助手的好友访问权限。', confirmLabel: '移除权限', tone: 'danger',
    })) return;
    setBusy(true);
    setError('');
    try {
      if (action === 'remove') await api.removeBotFriend(botId, uid);
      else if (action === 'accept') await api.acceptAgentFriend(botId, uid);
      else await api.rejectAgentFriend(botId, uid);
      await Promise.all([friends.refresh(), requests.refresh(), bindings.refresh()]);
      onAccessChanged?.();
    } catch (err) {
      setError(err.message || '操作失败，请重试。');
    } finally {
      setBusy(false);
    }
  }
  return <section className="cc-agent-collaboration-section cc-agent-visibility-settings">
    <header className="cc-agent-collaboration-heading">
      <h3>
        <UsersRound size={16} strokeWidth={1.8} aria-hidden="true" />助手好友
        {friends.loaded && <small className="cc-collaboration-count">· {friendIds.size}</small>}
      </h3>
    </header>
    {children}
    <div className="cc-friend-directory">
      {error && <p role="alert">{error}</p>}
      {(!requests.loaded || requests.failed || requests.rows.length > 0) && <section>
        <h4>好友申请</h4>
        <ListStatus list={requests} name="好友申请" empty="暂无待处理申请" />
        {requests.rows.map(person => <div className="cc-friend-row" key={person.from_user_id}>
          <span>{person.display_name || person.from_username || `用户 ${person.from_user_id}`}</span>
          <div className="cc-friend-actions">
            <button type="button" className="oc-btn oc-btn-default" disabled={busy} onClick={() => update('reject', person.from_user_id)}>拒绝</button>
            <button type="button" className="oc-btn oc-btn-primary" disabled={busy} onClick={() => update('accept', person.from_user_id)}>通过</button>
          </div>
        </div>)}
      </section>}
      <section aria-label="已添加的好友">
        <ListStatus list={friends} name="好友列表" empty="还没有好友添加这个助手" />
        {friends.rows.map(person => {
          const uid = person.id || person.uid;
          const name = person.display_name || person.username || `用户 ${uid}`;
          const channels = [...new Set((byUser.get(String(uid)) || []).map(bindingLabel))];
          return <div className="cc-friend-row" key={uid}>
            <div>
              <div className="cc-friend-identity"><span>{name}</span><small>UID {uid}</small></div>
              {channels.length > 0 && <small>{channels.join(' / ')}</small>}
            </div>
            <button type="button" className="cc-collaboration-text-action" aria-label={`移除 ${name}`} disabled={busy} onClick={() => update('remove', uid, name)}>移除</button>
          </div>;
        })}
      </section>
      {bindings.failed && <ListStatus list={bindings} name="渠道记录" />}
      {otherBindings.length > 0 && <details><summary>其他渠道记录（{otherBindings.length}）</summary>
        {otherBindings.map(item => <div className="cc-friend-row" key={item.binding.id}>
          <span>{item.user?.display_name || item.user?.username || item.binding.channel_user_id || '渠道用户'}</span><small>{bindingLabel(item)}</small>
        </div>)}
      </details>}
    </div>
  </section>;
}
