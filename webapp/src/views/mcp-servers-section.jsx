import React, { useCallback, useEffect, useState } from 'react';
import { ChevronDown, Pencil, Plus, Trash2, X } from 'lucide-react';
import { api } from '../api';

// 控制台 MCP Server 配置（CRUD + OAuth 2.1 授权发起）。
//
// 密钥纪律与面板其它部分一致：client_secret 只在写入时提交一次，永不回显；
// 列表只显示 has_client_secret / credential 的托管状态摘要。
// OAuth 授权是浏览器跳转：点「连接授权」拿到 authorize_url 后整页跳过去，
// 授权服务器回调回 /api/mcp/oauth/callback 完成托管。

const EMPTY_FORM = {
  name: '',
  endpoint: '',
  transport: 'streamable_http',
  enabled: true,
  auth_type: 'none',
  client_id: '',
  authorize_url: '',
  token_url: '',
  scopes: '',
  client_secret: '',
  clear_secret: false,
};

export default function MCPServersSection() {
  const [expanded, setExpanded] = useState(false);
  const [servers, setServers] = useState([]);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState('');
  const [notice, setNotice] = useState('');
  const [editingId, setEditingId] = useState(null); // null=未编辑, 0=新建, >0=编辑该 id
  const [form, setForm] = useState(EMPTY_FORM);
  const [submitting, setSubmitting] = useState(false);
  const [deleting, setDeleting] = useState(() => new Set());

  const load = useCallback(async () => {
    setLoading(true);
    setError('');
    try {
      const data = await api.listMCPServers();
      setServers(Array.isArray(data?.servers) ? data.servers : []);
    } catch (err) {
      setError(err?.message || '读取 MCP server 配置失败');
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    if (expanded) load();
  }, [expanded, load]);

  const openCreate = () => {
    setEditingId(0);
    setForm(EMPTY_FORM);
    setError('');
    setNotice('');
  };

  const openEdit = (server) => {
    setEditingId(server.id);
    setForm({
      name: server.name || '',
      endpoint: server.endpoint || '',
      transport: server.transport || 'streamable_http',
      enabled: !!server.enabled,
      auth_type: server.auth_type || 'none',
      client_id: server.client_id || '',
      authorize_url: server.authorize_url || '',
      token_url: server.token_url || '',
      scopes: server.scopes || '',
      // 已托管的 secret 不回填（也拿不到明文）——留空=不动，想换就填新的。
      client_secret: '',
      clear_secret: false,
    });
    setError('');
    setNotice('');
  };

  const closeEditor = () => {
    setEditingId(null);
    setForm(EMPTY_FORM);
  };

  const patchForm = (patch) => setForm((prev) => ({ ...prev, ...patch }));

  const submit = async (event) => {
    event.preventDefault();
    setSubmitting(true);
    setError('');
    try {
      const payload = {
        name: form.name.trim(),
        endpoint: form.endpoint.trim(),
        transport: form.transport,
        enabled: form.enabled,
        auth_type: form.auth_type,
        client_id: form.client_id.trim(),
        authorize_url: form.authorize_url.trim(),
        token_url: form.token_url.trim(),
        scopes: form.scopes.trim(),
      };
      if (form.client_secret) payload.client_secret = form.client_secret;
      if (form.clear_secret) payload.clear_secret = true;
      if (editingId === 0) {
        await api.createMCPServer(payload);
        setNotice('已创建 MCP server 配置');
      } else {
        await api.updateMCPServer(editingId, payload);
        setNotice('已更新 MCP server 配置');
      }
      closeEditor();
      await load();
    } catch (err) {
      setError(err?.message || '保存失败');
    } finally {
      setSubmitting(false);
    }
  };

  const remove = async (server) => {
    if (!window.confirm(`删除 MCP server「${server.name}」？已托管的授权凭据会一并清除。`)) return;
    setDeleting((prev) => new Set(prev).add(server.id));
    setError('');
    try {
      await api.deleteMCPServer(server.id);
      setNotice(`已删除 ${server.name}`);
      await load();
    } catch (err) {
      setError(err?.message || '删除失败');
    } finally {
      setDeleting((prev) => {
        const next = new Set(prev);
        next.delete(server.id);
        return next;
      });
    }
  };

  const startOAuth = async (server) => {
    setError('');
    try {
      const data = await api.startMCPServerOAuth(server.id);
      const url = data?.authorize_url;
      if (!url) throw new Error('服务端没有返回授权地址');
      // 整页跳转：回调落在 /api/mcp/oauth/callback，回来后配置视图里出现 credential 摘要。
      window.location.assign(url);
    } catch (err) {
      setError(err?.message || '发起授权失败');
    }
  };

  return (
    <div className="v3-service-agents-mcp-servers">
      <button
        type="button"
        className="v3-service-agents-mcp-servers-toggle"
        onClick={() => setExpanded((prev) => !prev)}
        aria-expanded={expanded}
      >
        <ChevronDown
          size={14}
          aria-hidden="true"
          style={{ transform: expanded ? 'rotate(180deg)' : 'none', transition: 'transform 120ms' }}
        />
        外部 MCP Servers（OAuth 2.1 托管）
      </button>
      {!expanded ? null : (
        <div className="v3-service-agents-mcp-servers-body">
          <p className="v3-service-agents-hint">
            登记你要用的外部 MCP server。需要授权的走 OAuth 2.1（PKCE）：
            授权完成后令牌加密托管在服务端，界面上永远看不到明文。
          </p>
          {notice ? <div className="v3-service-agents-notice" role="status">{notice}</div> : null}
          {error ? <div className="v3-service-agents-error" role="alert">{error}</div> : null}
          {loading ? <div className="v3-service-agents-empty">读取中…</div> : null}
          {!loading && servers.length === 0 && editingId === null ? (
            <div className="v3-service-agents-empty">还没有登记任何 MCP server。</div>
          ) : null}
          <ul className="v3-service-agents-mcp-servers-list">
            {servers.map((server) => (
              <li key={server.id} className="v3-service-agents-mcp-server">
                <div className="v3-service-agents-mcp-server-main">
                  <strong>{server.name}</strong>
                  <code>{server.endpoint}</code>
                  <span className="v3-service-agents-mcp-server-meta">
                    {server.enabled ? '启用' : '停用'} · {server.transport}
                    {server.auth_type === 'oauth21'
                      ? server.credential
                        ? ' · 已授权'
                        : ' · 未授权'
                      : server.auth_type === 'bearer'
                        ? ' · bearer'
                        : ''}
                    {server.has_client_secret ? ' · 已托管 secret' : ''}
                  </span>
                </div>
                <div className="v3-service-agents-mcp-server-actions">
                  {server.auth_type === 'oauth21' ? (
                    <button type="button" onClick={() => startOAuth(server)}>
                      {server.credential ? '重新授权' : '连接授权'}
                    </button>
                  ) : null}
                  <button type="button" onClick={() => openEdit(server)} aria-label={`编辑 ${server.name}`}>
                    <Pencil size={14} aria-hidden="true" />
                  </button>
                  <button
                    type="button"
                    onClick={() => remove(server)}
                    disabled={deleting.has(server.id)}
                    aria-label={`删除 ${server.name}`}
                  >
                    <Trash2 size={14} aria-hidden="true" />
                  </button>
                </div>
              </li>
            ))}
          </ul>
          {editingId === null ? (
            <button type="button" className="v3-service-agents-mcp-servers-add" onClick={openCreate}>
              <Plus size={14} aria-hidden="true" /> 添加 MCP server
            </button>
          ) : (
            <form className="v3-service-agents-mcp-server-form" onSubmit={submit}>
              <div className="v3-service-agents-mcp-server-form-head">
                <strong>{editingId === 0 ? '添加 MCP server' : '编辑 MCP server'}</strong>
                <button type="button" onClick={closeEditor} aria-label="关闭">
                  <X size={14} aria-hidden="true" />
                </button>
              </div>
              <label>
                名称
                <input
                  required
                  value={form.name}
                  onChange={(e) => patchForm({ name: e.target.value })}
                  placeholder="例：日历 MCP"
                />
              </label>
              <label>
                Endpoint URL
                <input
                  required
                  type="url"
                  value={form.endpoint}
                  onChange={(e) => patchForm({ endpoint: e.target.value })}
                  placeholder="https://mcp.example.com/mcp"
                />
              </label>
              <label>
                传输
                <select value={form.transport} onChange={(e) => patchForm({ transport: e.target.value })}>
                  <option value="streamable_http">streamable_http</option>
                  <option value="sse">sse</option>
                </select>
              </label>
              <label className="v3-service-agents-mcp-server-inline">
                <input
                  type="checkbox"
                  checked={form.enabled}
                  onChange={(e) => patchForm({ enabled: e.target.checked })}
                />
                启用
              </label>
              <label>
                认证方式
                <select value={form.auth_type} onChange={(e) => patchForm({ auth_type: e.target.value })}>
                  <option value="none">无认证</option>
                  <option value="oauth21">OAuth 2.1</option>
                  <option value="bearer">Bearer token</option>
                </select>
              </label>
              {form.auth_type === 'oauth21' ? (
                <>
                  <label>
                    Client ID
                    <input
                      required
                      value={form.client_id}
                      onChange={(e) => patchForm({ client_id: e.target.value })}
                    />
                  </label>
                  <label>
                    Authorize URL
                    <input
                      required
                      type="url"
                      value={form.authorize_url}
                      onChange={(e) => patchForm({ authorize_url: e.target.value })}
                      placeholder="https://auth.example.com/authorize"
                    />
                  </label>
                  <label>
                    Token URL
                    <input
                      required
                      type="url"
                      value={form.token_url}
                      onChange={(e) => patchForm({ token_url: e.target.value })}
                      placeholder="https://auth.example.com/token"
                    />
                  </label>
                  <label>
                    Scopes（空格分隔，可空）
                    <input value={form.scopes} onChange={(e) => patchForm({ scopes: e.target.value })} />
                  </label>
                  <label>
                    Client Secret（留空 = 不改动已托管的；填入即重新加密托管）
                    <input
                      type="password"
                      value={form.client_secret}
                      onChange={(e) => patchForm({ client_secret: e.target.value, clear_secret: false })}
                      autoComplete="new-password"
                    />
                  </label>
                  {editingId > 0 ? (
                    <label className="v3-service-agents-mcp-server-inline">
                      <input
                        type="checkbox"
                        checked={form.clear_secret}
                        onChange={(e) => patchForm({ clear_secret: e.target.checked, client_secret: '' })}
                      />
                      清除已托管的 client secret
                    </label>
                  ) : null}
                </>
              ) : null}
              <div className="v3-service-agents-mcp-server-form-actions">
                <button type="submit" disabled={submitting}>
                  {submitting ? '保存中…' : editingId === 0 ? '创建' : '保存'}
                </button>
                <button type="button" onClick={closeEditor}>取消</button>
              </div>
            </form>
          )}
        </div>
      )}
    </div>
  );
}
