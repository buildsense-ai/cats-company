import { api, setToken } from './api';

describe('System Prompt API', () => {
  beforeEach(() => {
    localStorage.clear();
    setToken('owner-token');
    global.fetch = vi.fn().mockResolvedValue({
      ok: true,
      status: 200,
      json: vi.fn().mockResolvedValue({ configured: true, revision: 4 }),
    });
  });

  afterEach(() => {
    setToken(null);
    vi.restoreAllMocks();
  });

  it('reads the canonical owner BotDefinition', async () => {
    await api.getBotDefinitionPrompt('bot 42');

    expect(global.fetch).toHaveBeenCalledWith(
      '/api/bots/definition?uid=bot%2042',
      expect.objectContaining({
        method: 'GET',
        headers: expect.objectContaining({ Authorization: 'Bearer owner-token' }),
      }),
    );
  });

  it('reads the field-level prompt viewer response for owners and friends', async () => {
    await api.getAgentPrompt('bot 42');

    expect(global.fetch).toHaveBeenCalledWith(
      '/api/agents/prompt?uid=bot%2042',
      expect.objectContaining({
        method: 'GET',
        headers: expect.objectContaining({ Authorization: 'Bearer owner-token' }),
      }),
    );
  });

  it('saves Prompt with the expected whole-definition revision', async () => {
    await api.updateBotDefinitionPrompt('42', 4, {
      selected: 'custom',
      customSystemPrompt: 'Stay precise.',
    });

    const [url, options] = global.fetch.mock.calls[0];
    expect(url).toBe('/api/bots/definition/prompt?uid=42');
    expect(options.method).toBe('PATCH');
    expect(JSON.parse(options.body)).toEqual({
      revision: 4,
      prompt: {
        selected: 'custom',
        customSystemPrompt: 'Stay precise.',
      },
    });
  });

  it('updates prompt visibility without changing the definition revision', async () => {
    await api.updateBotPromptVisibility('42', 'friends');

    const [url, options] = global.fetch.mock.calls[0];
    expect(url).toBe('/api/bots/definition/prompt-visibility?uid=42');
    expect(options.method).toBe('PATCH');
    expect(JSON.parse(options.body)).toEqual({ prompt_visibility: 'friends' });
  });
});
