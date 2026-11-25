import React, { act } from 'react';
import { createRoot } from 'react-dom/client';
import MCPServersSection from './mcp-servers-section';

// vi.mock 会被提升到文件顶部，mock 本体必须用 vi.hoisted 同步提升。
const apiMock = vi.hoisted(() => ({
  listMCPServers: vi.fn(),
  createMCPServer: vi.fn(),
  updateMCPServer: vi.fn(),
  deleteMCPServer: vi.fn(),
  startMCPServerOAuth: vi.fn(),
}));

vi.mock('../api', () => ({ api: apiMock }));

describe('MCPServersSection', () => {
  let container;
  let root;

  beforeEach(() => {
    global.IS_REACT_ACT_ENVIRONMENT = true;
    vi.clearAllMocks();
    apiMock.listMCPServers.mockResolvedValue({ servers: [] });
    container = document.createElement('div');
    document.body.appendChild(container);
    root = createRoot(container);
  });

  afterEach(async () => {
    await act(async () => root.unmount());
    container.remove();
  });

  const render = async () => {
    await act(async () => {
      root.render(<MCPServersSection />);
      await Promise.resolve();
    });
  };

  const expand = async () => {
    const toggle = container.querySelector('.v3-service-agents-mcp-servers-toggle');
    await act(async () => {
      toggle.dispatchEvent(new MouseEvent('click', { bubbles: true }));
      await Promise.resolve();
      await Promise.resolve();
    });
  };

  it('loads and renders registered servers with masked credential state', async () => {
    apiMock.listMCPServers.mockResolvedValue({
      servers: [{
        id: 7, name: '日历 MCP', endpoint: 'https://mcp.example.com/mcp',
        transport: 'streamable_http', enabled: true, auth_type: 'oauth21',
        has_client_secret: true,
        credential: { token_type: 'Bearer', scope: 'calendar.read', updated_at: '2026-11-25T00:00:00Z' },
      }],
    });
    await render();
    await expand();
    expect(apiMock.listMCPServers).toHaveBeenCalled();
    const text = container.textContent;
    expect(text).toContain('日历 MCP');
    expect(text).toContain('https://mcp.example.com/mcp');
    expect(text).toContain('已授权');
    expect(text).toContain('已托管 secret');
    // 任何密钥字面量都不该出现在界面上。
    expect(text).not.toContain('access_token');
    expect(text).not.toContain('client_secret');
  });

  it('creates a server through the form', async () => {
    apiMock.createMCPServer.mockResolvedValue({ id: 1 });
    await render();
    await expand();
    const addButton = container.querySelector('.v3-service-agents-mcp-servers-add');
    await act(async () => {
      addButton.dispatchEvent(new MouseEvent('click', { bubbles: true }));
      await Promise.resolve();
    });
    const form = container.querySelector('.v3-service-agents-mcp-server-form');
    expect(form).toBeTruthy();
    const nameInput = form.querySelector('input');
    await act(async () => {
      const setter = Object.getOwnPropertyDescriptor(window.HTMLInputElement.prototype, 'value').set;
      setter.call(nameInput, '测试 MCP');
      nameInput.dispatchEvent(new Event('input', { bubbles: true }));
    });
    const urlInput = form.querySelector('input[type="url"]');
    await act(async () => {
      const setter = Object.getOwnPropertyDescriptor(window.HTMLInputElement.prototype, 'value').set;
      setter.call(urlInput, 'https://mcp.example.com/mcp');
      urlInput.dispatchEvent(new Event('input', { bubbles: true }));
    });
    await act(async () => {
      form.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }));
      await Promise.resolve();
      await Promise.resolve();
    });
    expect(apiMock.createMCPServer).toHaveBeenCalled();
    const payload = apiMock.createMCPServer.mock.calls[0][0];
    expect(payload.name).toBe('测试 MCP');
    expect(payload.endpoint).toBe('https://mcp.example.com/mcp');
    // 无认证模式下不提交任何 secret/oauth 字段垃圾。
    expect(payload.client_secret).toBeUndefined();
  });

  it('submits client_secret only when filled and never echoes it back', async () => {
    apiMock.listMCPServers.mockResolvedValue({
      servers: [{
        id: 3, name: 'X', endpoint: 'https://mcp.example.com',
        transport: 'streamable_http', enabled: true, auth_type: 'oauth21', has_client_secret: true,
      }],
    });
    apiMock.updateMCPServer.mockResolvedValue({ id: 3 });
    await render();
    await expand();
    const editButton = container.querySelector('button[aria-label="编辑 X"]');
    await act(async () => {
      editButton.dispatchEvent(new MouseEvent('click', { bubbles: true }));
      await Promise.resolve();
    });
    const form = container.querySelector('.v3-service-agents-mcp-server-form');
    expect(form).toBeTruthy();
    // secret 输入框必须为空（不回填已托管的）。
    const secretInput = form.querySelector('input[type="password"]');
    expect(secretInput).toBeTruthy();
    expect(secretInput.value).toBe('');
    // 直接提交：不带 client_secret 字段（不动已托管的）。
    await act(async () => {
      form.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }));
      await Promise.resolve();
      await Promise.resolve();
    });
    expect(apiMock.updateMCPServer).toHaveBeenCalled();
    const payload = apiMock.updateMCPServer.mock.calls[0][1];
    expect(payload.client_secret).toBeUndefined();
  });

  it('deletes a server after confirmation', async () => {
    apiMock.listMCPServers.mockResolvedValue({
      servers: [{
        id: 5, name: '废弃 MCP', endpoint: 'https://mcp.example.com',
        transport: 'streamable_http', enabled: true, auth_type: 'none',
      }],
    });
    apiMock.deleteMCPServer.mockResolvedValue({ deleted: true });
    vi.spyOn(window, 'confirm').mockReturnValue(true);
    await render();
    await expand();
    const deleteButton = container.querySelector('button[aria-label="删除 废弃 MCP"]');
    await act(async () => {
      deleteButton.dispatchEvent(new MouseEvent('click', { bubbles: true }));
      await Promise.resolve();
      await Promise.resolve();
    });
    expect(apiMock.deleteMCPServer).toHaveBeenCalledWith(5);
  });

  it('redirects to the authorize url when starting oauth', async () => {
    apiMock.listMCPServers.mockResolvedValue({
      servers: [{
        id: 9, name: 'OAuth MCP', endpoint: 'https://mcp.example.com',
        transport: 'streamable_http', enabled: true, auth_type: 'oauth21',
      }],
    });
    apiMock.startMCPServerOAuth.mockResolvedValue({ authorize_url: 'https://auth.example.com/authorize?state=abc' });
    const assignSpy = vi.fn();
    const originalLocation = window.location;
    delete window.location;
    window.location = { ...originalLocation, assign: assignSpy };
    await render();
    await expand();
    const connectButton = Array.from(container.querySelectorAll('button')).find((b) => b.textContent.includes('连接授权'));
    expect(connectButton).toBeTruthy();
    await act(async () => {
      connectButton.dispatchEvent(new MouseEvent('click', { bubbles: true }));
      await Promise.resolve();
      await Promise.resolve();
    });
    expect(apiMock.startMCPServerOAuth).toHaveBeenCalledWith(9);
    expect(assignSpy).toHaveBeenCalledWith('https://auth.example.com/authorize?state=abc');
    window.location = originalLocation;
  });
});
